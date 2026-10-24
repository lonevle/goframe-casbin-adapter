package gadapter

import (
	"context"
	"fmt"

	"github.com/casbin/casbin/v3/model"
	"github.com/casbin/casbin/v3/persist"
	"github.com/gogf/gf/v2/database/gdb"
)

// loadPolicy 从数据库加载全部策略到 casbin 模型。
// 公共实现：普通链路与事务链路共用
func loadPolicy(link dbLink, ctx context.Context, m model.Model) error {
	if m == nil {
		return ErrNilModel
	}
	if err := link.checkActive(); err != nil {
		return err
	}
	names := link.Fields()
	// 按主键排序保证加载顺序稳定
	records, err := link.Model(ctx).OrderAsc(names.ID).All()
	if err != nil {
		return fmt.Errorf("g-adapter: failed to load policy: %w", err)
	}
	for _, record := range records {
		rule, ok := recordToRule(record, names)
		if !ok {
			continue // 跳过无效行
		}
		if err = persist.LoadPolicyArray(rule, m); err != nil {
			return fmt.Errorf("g-adapter: failed to load policy rule %v: %w", rule, err)
		}
	}
	return nil
}

// savePolicy 将 casbin 模型中的全部策略写入数据库。
// 在事务中先清空旧策略再全量写入，保证原子性
func savePolicy(link dbLink, ctx context.Context, m model.Model) error {
	if m == nil {
		return ErrNilModel
	}
	if err := link.checkActive(); err != nil {
		return err
	}
	list, err := modelToData(link.Fields(), m)
	if err != nil {
		return err
	}
	return link.transaction(ctx, func(ctx context.Context, _ gdb.TX) error {
		// GoFrame 出于安全考虑拒绝执行不带 WHERE 的 DELETE，
		// 因此追加恒真条件 1=1 实现全表清空；
		// 不使用 TRUNCATE：它属于 DDL，会隐式提交事务、无法回滚，
		// 与此处"清空 + 重写"需要原子性的语义冲突
		if _, err := link.Model(ctx).Where("1=1").Delete(); err != nil {
			return fmt.Errorf("g-adapter: failed to clear policy: %w", err)
		}
		if len(list) == 0 {
			return nil
		}
		if _, err := link.Model(ctx).Data(list).Batch(batchSize).Insert(); err != nil {
			return fmt.Errorf("g-adapter: failed to save policy: %w", err)
		}
		return nil
	})
}

// addPolicy 添加单条策略到数据库。
// 规则为空、尾部为空串或超过 6 个字段时拒绝（写读对称约束）
func addPolicy(link dbLink, ctx context.Context, sec, ptype string, rule []string) error {
	if err := link.checkActive(); err != nil {
		return err
	}
	if err := validateRule(rule); err != nil {
		return fmt.Errorf("g-adapter: ptype %q: %w", ptype, err)
	}
	data := ruleToData(link.Fields(), ptype, rule)
	if _, err := link.Model(ctx).Data(data).Insert(); err != nil {
		return fmt.Errorf("g-adapter: failed to add policy %v: %w", rule, err)
	}
	return nil
}

// removePolicy 删除单条策略（精确匹配，尾部不足的字段按空串匹配）
func removePolicy(link dbLink, ctx context.Context, sec, ptype string, rule []string) error {
	if err := link.checkActive(); err != nil {
		return err
	}
	if err := validateRule(rule); err != nil {
		return fmt.Errorf("g-adapter: ptype %q: %w", ptype, err)
	}
	m := applyRuleWhere(link.Model(ctx), link.Fields(), ptype, rule)
	if _, err := m.Delete(); err != nil {
		return fmt.Errorf("g-adapter: failed to remove policy %v: %w", rule, err)
	}
	return nil
}

// removeFilteredPolicy 按过滤条件删除策略：从第 fieldIndex 个字段起匹配 fieldValues；
// fieldIndex 为 -1 时忽略 fieldValues，删除该 ptype 的全部策略（官方兼容语义）
func removeFilteredPolicy(link dbLink, ctx context.Context, sec, ptype string, fieldIndex int, fieldValues ...string) error {
	if fieldIndex != -1 && (fieldIndex < 0 || fieldIndex >= ruleFieldCount) {
		return ErrInvalidFieldIndex
	}
	if err := link.checkActive(); err != nil {
		return err
	}
	m := applyFilterWhere(link.Model(ctx), link.Fields(), ptype, fieldIndex, fieldValues)
	if _, err := m.Delete(); err != nil {
		return fmt.Errorf("g-adapter: failed to remove filtered policy: %w", err)
	}
	return nil
}

// ==================== Adapter 基础接口实现 ====================

// LoadPolicy 实现 persist.Adapter：加载全部策略
func (a *Adapter) LoadPolicy(m model.Model) error {
	return a.LoadPolicyCtx(context.Background(), m)
}

// LoadPolicyCtx 实现 persist.ContextAdapter：带 context 加载全部策略
func (a *Adapter) LoadPolicyCtx(ctx context.Context, m model.Model) error {
	err := loadPolicy(a, ctx, m)
	if err == nil {
		a.isFiltered.Store(false) // 全量加载后清除过滤标记
	}
	return err
}

// SavePolicy 实现 persist.Adapter：保存全部策略
func (a *Adapter) SavePolicy(m model.Model) error {
	return a.SavePolicyCtx(context.Background(), m)
}

// SavePolicyCtx 实现 persist.ContextAdapter：带 context 保存全部策略
func (a *Adapter) SavePolicyCtx(ctx context.Context, m model.Model) error {
	return savePolicy(a, ctx, m)
}

// AddPolicy 实现 persist.Adapter：添加单条策略
func (a *Adapter) AddPolicy(sec string, ptype string, rule []string) error {
	return a.AddPolicyCtx(context.Background(), sec, ptype, rule)
}

// AddPolicyCtx 实现 persist.ContextAdapter：带 context 添加单条策略
func (a *Adapter) AddPolicyCtx(ctx context.Context, sec string, ptype string, rule []string) error {
	return addPolicy(a, ctx, sec, ptype, rule)
}

// RemovePolicy 实现 persist.Adapter：删除单条策略
func (a *Adapter) RemovePolicy(sec string, ptype string, rule []string) error {
	return a.RemovePolicyCtx(context.Background(), sec, ptype, rule)
}

// RemovePolicyCtx 实现 persist.ContextAdapter：带 context 删除单条策略
func (a *Adapter) RemovePolicyCtx(ctx context.Context, sec string, ptype string, rule []string) error {
	return removePolicy(a, ctx, sec, ptype, rule)
}

// RemoveFilteredPolicy 实现 persist.Adapter：按过滤条件删除策略
func (a *Adapter) RemoveFilteredPolicy(sec string, ptype string, fieldIndex int, fieldValues ...string) error {
	return a.RemoveFilteredPolicyCtx(context.Background(), sec, ptype, fieldIndex, fieldValues...)
}

// RemoveFilteredPolicyCtx 实现 persist.ContextAdapter：带 context 按过滤条件删除策略
func (a *Adapter) RemoveFilteredPolicyCtx(ctx context.Context, sec string, ptype string, fieldIndex int, fieldValues ...string) error {
	return removeFilteredPolicy(a, ctx, sec, ptype, fieldIndex, fieldValues...)
}

// ==================== 事务链路基础接口实现 ====================
// 非 Ctx 版本统一使用事务开启时保存的 ctx，
// 保证 trace、超时等信息在事务内不丢失

// LoadPolicy 事务链路：加载全部策略
func (ta *txAdapter) LoadPolicy(m model.Model) error {
	return ta.LoadPolicyCtx(ta.ctx, m)
}

// LoadPolicyCtx 事务链路：带 context 加载全部策略
func (ta *txAdapter) LoadPolicyCtx(ctx context.Context, m model.Model) error {
	err := loadPolicy(ta, ctx, m)
	if err == nil {
		ta.isFiltered.Store(false)
	}
	return err
}

// SavePolicy 事务链路：保存全部策略
func (ta *txAdapter) SavePolicy(m model.Model) error {
	return ta.SavePolicyCtx(ta.ctx, m)
}

// SavePolicyCtx 事务链路：带 context 保存全部策略
func (ta *txAdapter) SavePolicyCtx(ctx context.Context, m model.Model) error {
	return savePolicy(ta, ctx, m)
}

// AddPolicy 事务链路：添加单条策略
func (ta *txAdapter) AddPolicy(sec string, ptype string, rule []string) error {
	return ta.AddPolicyCtx(ta.ctx, sec, ptype, rule)
}

// AddPolicyCtx 事务链路：带 context 添加单条策略
func (ta *txAdapter) AddPolicyCtx(ctx context.Context, sec string, ptype string, rule []string) error {
	return addPolicy(ta, ctx, sec, ptype, rule)
}

// RemovePolicy 事务链路：删除单条策略
func (ta *txAdapter) RemovePolicy(sec string, ptype string, rule []string) error {
	return ta.RemovePolicyCtx(ta.ctx, sec, ptype, rule)
}

// RemovePolicyCtx 事务链路：带 context 删除单条策略
func (ta *txAdapter) RemovePolicyCtx(ctx context.Context, sec string, ptype string, rule []string) error {
	return removePolicy(ta, ctx, sec, ptype, rule)
}

// RemoveFilteredPolicy 事务链路：按过滤条件删除策略
func (ta *txAdapter) RemoveFilteredPolicy(sec string, ptype string, fieldIndex int, fieldValues ...string) error {
	return ta.RemoveFilteredPolicyCtx(ta.ctx, sec, ptype, fieldIndex, fieldValues...)
}

// RemoveFilteredPolicyCtx 事务链路：带 context 按过滤条件删除策略
func (ta *txAdapter) RemoveFilteredPolicyCtx(ctx context.Context, sec string, ptype string, fieldIndex int, fieldValues ...string) error {
	return removeFilteredPolicy(ta, ctx, sec, ptype, fieldIndex, fieldValues...)
}
