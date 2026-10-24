package gadapter

import (
	"context"
	"fmt"

	"github.com/gogf/gf/v2/database/gdb"
)

// updatePolicy 将单条旧规则更新为新规则。
// 新规则不足 6 个字段时，尾部字段写为空串，确保旧行残留值被清空；
// 新旧规则为空、尾部为空串或超过 6 个字段时拒绝；
// 旧规则在数据库中不存在时返回 ErrPolicyNotFound（内存与数据库失联）
func updatePolicy(link dbLink, ctx context.Context, sec, ptype string, oldRule, newRule []string) error {
	if err := link.checkActive(); err != nil {
		return err
	}
	if err := validateRule(oldRule); err != nil {
		return fmt.Errorf("g-adapter: ptype %q old rule: %w", ptype, err)
	}
	if err := validateRule(newRule); err != nil {
		return fmt.Errorf("g-adapter: ptype %q new rule: %w", ptype, err)
	}
	names := link.Fields()
	data := ruleToFullData(names, ptype, newRule)
	m := applyRuleWhere(link.Model(ctx), names, ptype, oldRule)
	res, err := m.Data(data).Update()
	if err != nil {
		return fmt.Errorf("g-adapter: failed to update policy %v to %v: %w", oldRule, newRule, err)
	}
	// 新旧规则存在差异时，命中行必然发生变化，0 行受影响说明旧规则
	// 不在库中；新旧完全相同时，部分驱动（如 MySQL 默认配置）对
	// 无变化 UPDATE 报告 0 行受影响，属正常，不视为缺失。
	// RowsAffected 自身出错时（驱动不支持等罕见场景）跳过缺失检查，
	// 避免 0 值被误判为规则不存在
	if !samePaddedRule(oldRule, newRule) {
		if affected, rerr := res.RowsAffected(); rerr == nil && affected == 0 {
			return fmt.Errorf("%w: %v", ErrPolicyNotFound, oldRule)
		}
	}
	return nil
}

// updatePolicies 批量更新策略：新旧规则一一对应，事务中逐条更新，任一条失败整体回滚。
// 保持逐条 UPDATE 的原因：每条的新值不同，合并为单条 SQL 需要 CASE WHEN
// 复杂化（官方 gorm 适配器同样逐条），事务已保证原子性
func updatePolicies(link dbLink, ctx context.Context, sec, ptype string, oldRules, newRules [][]string) error {
	if len(oldRules) != len(newRules) {
		return ErrRulesLengthMismatch
	}
	if len(oldRules) == 0 {
		return nil
	}
	if err := link.checkActive(); err != nil {
		return err
	}
	for i := range oldRules {
		if err := validateRule(oldRules[i]); err != nil {
			return fmt.Errorf("g-adapter: ptype %q old rule #%d: %w", ptype, i, err)
		}
		if err := validateRule(newRules[i]); err != nil {
			return fmt.Errorf("g-adapter: ptype %q new rule #%d: %w", ptype, i, err)
		}
	}
	return link.transaction(ctx, func(ctx context.Context, _ gdb.TX) error {
		for i := range oldRules {
			if err := updatePolicy(link, ctx, sec, ptype, oldRules[i], newRules[i]); err != nil {
				return err
			}
		}
		return nil
	})
}

// updateFilteredPolicies 按过滤条件替换策略：删除所有匹配旧规则并写入新规则，
// 返回被删除的旧规则（供 casbin 回滚使用），整个替换在事务中完成；
// fieldIndex 为 -1 时忽略 fieldValues，替换该 ptype 的全部策略；
// 新规则为空、尾部为空串或超过 6 个字段时拒绝
func updateFilteredPolicies(link dbLink, ctx context.Context, sec, ptype string, newRules [][]string, fieldIndex int, fieldValues ...string) ([][]string, error) {
	if fieldIndex != -1 && (fieldIndex < 0 || fieldIndex >= ruleFieldCount) {
		return nil, ErrInvalidFieldIndex
	}
	if err := link.checkActive(); err != nil {
		return nil, err
	}
	for i, rule := range newRules {
		if err := validateRule(rule); err != nil {
			return nil, fmt.Errorf("g-adapter: ptype %q new rule #%d: %w", ptype, i, err)
		}
	}
	names := link.Fields()
	var oldRules [][]string
	err := link.transaction(ctx, func(ctx context.Context, _ gdb.TX) error {
		// 查询将被替换的旧规则
		m := applyFilterWhere(link.Model(ctx), names, ptype, fieldIndex, fieldValues)
		records, err := m.OrderAsc(names.ID).All()
		if err != nil {
			return fmt.Errorf("g-adapter: failed to query old policies: %w", err)
		}
		for _, record := range records {
			if rule, ok := recordToRule(record, names); ok {
				// 去掉首位的 ptype，返回纯规则值
				oldRules = append(oldRules, rule[1:])
			}
		}
		// 按原过滤条件直接 DELETE：条件参数固定且极少，改为按主键
		// 删除反而要应对主键集合的参数上限分块（removePolicies 的做法）
		m = applyFilterWhere(link.Model(ctx), names, ptype, fieldIndex, fieldValues)
		if _, err := m.Delete(); err != nil {
			return fmt.Errorf("g-adapter: failed to delete old policies: %w", err)
		}
		// 写入新规则
		if err := addPolicies(link, ctx, sec, ptype, newRules); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return oldRules, nil
}

// ==================== Adapter 更新接口实现 ====================

// UpdatePolicy 实现 persist.UpdatableAdapter：更新单条策略
func (a *Adapter) UpdatePolicy(sec string, ptype string, oldRule, newRule []string) error {
	return a.UpdatePolicyCtx(context.Background(), sec, ptype, oldRule, newRule)
}

// UpdatePolicyCtx 实现 persist.ContextUpdatableAdapter：带 context 更新单条策略
func (a *Adapter) UpdatePolicyCtx(ctx context.Context, sec string, ptype string, oldRule, newRule []string) error {
	return updatePolicy(a, ctx, sec, ptype, oldRule, newRule)
}

// UpdatePolicies 实现 persist.UpdatableAdapter：批量更新策略
func (a *Adapter) UpdatePolicies(sec string, ptype string, oldRules, newRules [][]string) error {
	return a.UpdatePoliciesCtx(context.Background(), sec, ptype, oldRules, newRules)
}

// UpdatePoliciesCtx 实现 persist.ContextUpdatableAdapter：带 context 批量更新策略
func (a *Adapter) UpdatePoliciesCtx(ctx context.Context, sec string, ptype string, oldRules, newRules [][]string) error {
	return updatePolicies(a, ctx, sec, ptype, oldRules, newRules)
}

// UpdateFilteredPolicies 实现 persist.UpdatableAdapter：按条件替换策略
func (a *Adapter) UpdateFilteredPolicies(sec string, ptype string, newRules [][]string, fieldIndex int, fieldValues ...string) ([][]string, error) {
	return a.UpdateFilteredPoliciesCtx(context.Background(), sec, ptype, newRules, fieldIndex, fieldValues...)
}

// UpdateFilteredPoliciesCtx 实现 persist.ContextUpdatableAdapter：带 context 按条件替换策略
func (a *Adapter) UpdateFilteredPoliciesCtx(ctx context.Context, sec string, ptype string, newRules [][]string, fieldIndex int, fieldValues ...string) ([][]string, error) {
	return updateFilteredPolicies(a, ctx, sec, ptype, newRules, fieldIndex, fieldValues...)
}

// ==================== 事务链路更新接口实现 ====================
// 非 Ctx 版本统一使用事务开启时保存的 ctx

// UpdatePolicy 事务链路：更新单条策略
func (ta *txAdapter) UpdatePolicy(sec string, ptype string, oldRule, newRule []string) error {
	return ta.UpdatePolicyCtx(ta.ctx, sec, ptype, oldRule, newRule)
}

// UpdatePolicyCtx 事务链路：带 context 更新单条策略
func (ta *txAdapter) UpdatePolicyCtx(ctx context.Context, sec string, ptype string, oldRule, newRule []string) error {
	return updatePolicy(ta, ctx, sec, ptype, oldRule, newRule)
}

// UpdatePolicies 事务链路：批量更新策略
func (ta *txAdapter) UpdatePolicies(sec string, ptype string, oldRules, newRules [][]string) error {
	return ta.UpdatePoliciesCtx(ta.ctx, sec, ptype, oldRules, newRules)
}

// UpdatePoliciesCtx 事务链路：带 context 批量更新策略
func (ta *txAdapter) UpdatePoliciesCtx(ctx context.Context, sec string, ptype string, oldRules, newRules [][]string) error {
	return updatePolicies(ta, ctx, sec, ptype, oldRules, newRules)
}

// UpdateFilteredPolicies 事务链路：按条件替换策略
func (ta *txAdapter) UpdateFilteredPolicies(sec string, ptype string, newRules [][]string, fieldIndex int, fieldValues ...string) ([][]string, error) {
	return ta.UpdateFilteredPoliciesCtx(ta.ctx, sec, ptype, newRules, fieldIndex, fieldValues...)
}

// UpdateFilteredPoliciesCtx 事务链路：带 context 按条件替换策略
func (ta *txAdapter) UpdateFilteredPoliciesCtx(ctx context.Context, sec string, ptype string, newRules [][]string, fieldIndex int, fieldValues ...string) ([][]string, error) {
	return updateFilteredPolicies(ta, ctx, sec, ptype, newRules, fieldIndex, fieldValues...)
}
