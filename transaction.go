package gadapter

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/casbin/casbin/v3/persist"
	"github.com/gogf/gf/v2/database/gdb"
)

// txAdapter 事务链路适配器：所有数据库操作经由事务执行。
// 通过 dbLink 抽象复用全部公共 CRUD 实现，与 Adapter 完整实现相同接口，
// 确保事务内的任何操作都不会逃逸到事务外
type txAdapter struct {
	tx  gdb.TX          // 事务实例
	ctx context.Context // 事务开启时的 context：
	// casbin 的非 Ctx 接口方法不携带 context，
	// 用它兜底，避免 trace、超时信息在事务内丢失
	options    Options      // 表名与字段名配置，与所属 Adapter 保持一致
	isFiltered atomic.Bool  // 过滤标记（事务链路内独立维护）
	finished   *atomic.Bool // 与 TransactionContext 共享的结束标记：
	// Commit/Rollback 成功后置位，
	// 后续经 GetAdapter() 发起的操作被 checkActive 拦截
}

// txState 事务上下文的生命周期状态，用于防止重复提交或回滚
type txState uint32

const (
	txStateActive     txState = iota // 进行中，允许提交或回滚
	txStateCommitted                 // 已提交，任何后续操作被拒绝
	txStateRolledBack                // 已回滚，任何后续操作被拒绝
)

// TransactionContext 实现 persist.TransactionContext：
// 封装数据库事务的提交、回滚以及事务内适配器的获取
type TransactionContext struct {
	adapter  *txAdapter   // 事务链路适配器
	finished *atomic.Bool // 事务结束标记，与 adapter.finished 共享同一实例
	mu       sync.Mutex   // 保护 state 的并发访问
	state    txState      // 当前事务状态
}

// BeginTransaction 实现 persist.TransactionalAdapter：开启数据库事务，
// 返回事务上下文，其 GetAdapter() 得到的适配器全部操作均在该事务内执行
func (a *Adapter) BeginTransaction(ctx context.Context) (persist.TransactionContext, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	tx, err := a.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("g-adapter: failed to begin transaction: %w", err)
	}
	finished := &atomic.Bool{}
	return &TransactionContext{
		adapter:  &txAdapter{tx: tx, ctx: ctx, options: a.options, finished: finished},
		finished: finished,
	}, nil
}

// Commit 提交事务；事务已结束（提交或回滚过）时返回 ErrTransactionFinished。
// 提交失败时状态保持进行中，调用方仍可尝试 Rollback 清理连接
func (tc *TransactionContext) Commit() error {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	if tc.state != txStateActive {
		return ErrTransactionFinished
	}
	if err := tc.adapter.tx.Commit(); err != nil {
		return err
	}
	tc.state = txStateCommitted
	tc.finished.Store(true) // 同步置位共享标记，拦截后续适配器操作
	return nil
}

// Rollback 回滚事务；事务已结束时返回 ErrTransactionFinished
func (tc *TransactionContext) Rollback() error {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	if tc.state != txStateActive {
		return ErrTransactionFinished
	}
	if err := tc.adapter.tx.Rollback(); err != nil {
		return err
	}
	tc.state = txStateRolledBack
	tc.finished.Store(true) // 同步置位共享标记，拦截后续适配器操作
	return nil
}

// GetAdapter 返回事务内使用的适配器，
// 调用方可断言回具体接口类型（如 persist.ContextBatchAdapter）使用；
// 事务提交或回滚后，该适配器的任何数据库操作都会返回 ErrTransactionFinished
func (tc *TransactionContext) GetAdapter() persist.Adapter {
	return tc.adapter
}

// Model 实现 dbLink：事务链路基于事务实例
func (ta *txAdapter) Model(ctx context.Context) *gdb.Model {
	return ta.tx.Ctx(ctx).Model(ta.options.TableName)
}

// Fields 实现 dbLink：返回字段名配置
func (ta *txAdapter) Fields() *FieldName {
	return ta.options.FieldNames
}

// Transaction 实现 dbLink：事务链路上开启嵌套事务（保存点）
func (ta *txAdapter) Transaction(ctx context.Context, fn func(ctx context.Context, tx gdb.TX) error) error {
	return ta.tx.Transaction(ctx, fn)
}

// checkActive 实现 dbLink：事务已提交或回滚后拒绝操作，
// 返回明确的 ErrTransactionFinished 而非晦涩的驱动层错误
// （如 "sql: transaction has already been committed or rolled back"）
func (ta *txAdapter) checkActive() error {
	if ta.finished != nil && ta.finished.Load() {
		return ErrTransactionFinished
	}
	return nil
}
