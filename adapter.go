// package gadapter 提供基于 GoFrame ORM（gdb）的 casbin v3 策略适配器，
// 支持 MySQL、SQL Server 等数据库。策略表需预先创建，建表 SQL 见 README
package gadapter

import (
	"context"
	"fmt"
	"sync/atomic"

	"github.com/casbin/casbin/v3/persist"
	"github.com/gogf/gf/v2/database/gdb"
)

// 编译期接口检查：确保 Adapter 完整实现 casbin v3 全部持久化扩展接口
var (
	_ persist.Adapter                 = (*Adapter)(nil)
	_ persist.ContextAdapter          = (*Adapter)(nil)
	_ persist.BatchAdapter            = (*Adapter)(nil)
	_ persist.ContextBatchAdapter     = (*Adapter)(nil)
	_ persist.UpdatableAdapter        = (*Adapter)(nil)
	_ persist.ContextUpdatableAdapter = (*Adapter)(nil)
	_ persist.FilteredAdapter         = (*Adapter)(nil)
	_ persist.ContextFilteredAdapter  = (*Adapter)(nil)
	_ persist.TransactionalAdapter    = (*Adapter)(nil)

	// 事务链路适配器同样完整实现全部接口，保证事务内操作不会逃逸到事务外
	_ persist.Adapter                 = (*txAdapter)(nil)
	_ persist.ContextAdapter          = (*txAdapter)(nil)
	_ persist.BatchAdapter            = (*txAdapter)(nil)
	_ persist.ContextBatchAdapter     = (*txAdapter)(nil)
	_ persist.UpdatableAdapter        = (*txAdapter)(nil)
	_ persist.ContextUpdatableAdapter = (*txAdapter)(nil)
	_ persist.FilteredAdapter         = (*txAdapter)(nil)
	_ persist.ContextFilteredAdapter  = (*txAdapter)(nil)
)

// dbLink 数据链路抽象：屏蔽普通查询与事务查询的差异。
// 全部公共 CRUD 逻辑均基于该接口编写，普通链路与事务链路复用同一套实现，
// 事务适配器因此天然将所有操作导向事务，不存在遗漏
type dbLink interface {
	// Model 返回绑定策略表的 ORM 模型
	Model(ctx context.Context) *gdb.Model
	// Fields 返回策略表字段名配置
	Fields() *FieldName
	// Transaction 在事务中执行 fn，fn 返回错误时自动回滚；
	// 事务链路上为嵌套事务（保存点）
	Transaction(ctx context.Context, fn func(ctx context.Context, tx gdb.TX) error) error
	// checkActive 校验链路是否仍可用：普通链路恒可用；
	// 事务链路在提交或回滚后返回 ErrTransactionFinished。
	// 各公共 CRUD 函数入口统一调用，避免用户在事务结束后
	// 经 GetAdapter() 继续操作时收到晦涩的驱动层错误
	checkActive() error
}

// Adapter 基于 GoFrame ORM 的 casbin 策略适配器。
// 支持自定义表名与字段名，策略表需按 README 中的建表 SQL 预先创建
type Adapter struct {
	options    Options     // 表名与字段名配置
	db         gdb.DB      // 数据库实例
	isFiltered atomic.Bool // 标记当前加载的策略是否经过过滤；
	// 使用原子类型，LoadFilteredPolicy 写与
	// IsFiltered 读可能来自不同 goroutine
}

// NewAdapterByDB 基于已有数据库实例创建适配器，
// opts 可覆盖默认表名（casbin_rule）或字段名配置
func NewAdapterByDB(db gdb.DB, opts ...Option) (*Adapter, error) {
	if db == nil {
		return nil, ErrDBRequired
	}
	return &Adapter{
		options: newOptionsWithDefaults(opts...),
		db:      db,
	}, nil
}

// NewAdapterByGroup 基于 GoFrame 配置分组创建适配器，
// 分组来自项目配置文件 database 节点（如 config.yaml 的 database.group 节点）
func NewAdapterByGroup(group string, opts ...Option) (*Adapter, error) {
	db, err := gdb.NewByGroup(group)
	if err != nil {
		return nil, fmt.Errorf("g-adapter: failed to create db from group %q: %w", group, err)
	}
	return NewAdapterByDB(db, opts...)
}

// NewFilteredAdapterByDB 创建带过滤标记的适配器：初始 isFiltered 即为 true。
// casbin 在初始化 Enforcer 时会检查 IsFiltered()，为 true 则跳过自动全量加载，
// 因此该构造函数是多租户等过滤加载场景的正确入口：
//
//	a, _ := adapter.NewFilteredAdapterByDB(g.DB())
//	e, _ := casbin.NewEnforcer(model, a) // 不会自动加载任何策略
//	e.LoadFilteredPolicy(&adapter.Filter{P: []string{"tenant-1", "", ""}})
//
// 若使用普通 NewAdapterByDB，Enforcer 构造时会自动全量加载，
// 过滤条件来不及生效，所有租户的策略都会进入内存
func NewFilteredAdapterByDB(db gdb.DB, opts ...Option) (*Adapter, error) {
	a, err := NewAdapterByDB(db, opts...)
	if err != nil {
		return nil, err
	}
	a.isFiltered.Store(true)
	return a, nil
}

// NewFilteredAdapterByGroup 基于配置分组创建带过滤标记的适配器，
// 行为说明见 NewFilteredAdapterByDB
func NewFilteredAdapterByGroup(group string, opts ...Option) (*Adapter, error) {
	a, err := NewAdapterByGroup(group, opts...)
	if err != nil {
		return nil, err
	}
	a.isFiltered.Store(true)
	return a, nil
}

// GetDB 返回底层数据库实例，便于业务侧复用同一连接
func (a *Adapter) GetDB() gdb.DB { return a.db }

// GetTableName 返回策略表名
func (a *Adapter) GetTableName() string { return a.options.TableName }

// Model 实现 dbLink：普通链路直接基于数据库实例
func (a *Adapter) Model(ctx context.Context) *gdb.Model {
	return a.db.Ctx(ctx).Model(a.options.TableName)
}

// Fields 实现 dbLink：返回字段名配置
func (a *Adapter) Fields() *FieldName { return a.options.FieldNames }

// Transaction 实现 dbLink：基于数据库实例开启事务
func (a *Adapter) Transaction(ctx context.Context, fn func(ctx context.Context, tx gdb.TX) error) error {
	return a.db.Transaction(ctx, fn)
}

// checkActive 实现 dbLink：普通链路不存在生命周期问题，恒可用
func (a *Adapter) checkActive() error { return nil }
