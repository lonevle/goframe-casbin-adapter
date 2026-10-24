package gadapter

import (
	"context"
	"fmt"

	"github.com/gogf/gf/v2/database/gdb"
)

// addPolicies 批量添加策略：在事务中批量写入，任一批失败整体回滚。
// 公共实现：普通链路与事务链路共用。
// 必须包事务的原因：GoFrame 对字段不齐的批量数据会按字段集合
// 拆分为多条 INSERT 分别执行（如 p 规则 3 字段与 g 规则 2 字段混批），
// 不加事务时部分失败会留下半截数据。
// 任一规则为空、尾部为空串或超过 6 个字段时拒绝写入
// （ErrEmptyRule / ErrTrailingEmptyField / ErrTooManyRuleFields）
func addPolicies(link dbLink, ctx context.Context, sec, ptype string, rules [][]string) error {
	if len(rules) == 0 {
		return nil
	}
	if err := link.checkActive(); err != nil {
		return err
	}
	names := link.Fields()
	list := make(gdb.List, 0, len(rules))
	for _, rule := range rules {
		if err := validateRule(rule); err != nil {
			return fmt.Errorf("g-adapter: ptype %q: %w", ptype, err)
		}
		list = append(list, ruleToData(names, ptype, rule))
	}
	return link.transaction(ctx, func(ctx context.Context, _ gdb.TX) error {
		if _, err := link.Model(ctx).Data(list).Batch(batchSize).Insert(); err != nil {
			return fmt.Errorf("g-adapter: failed to add %d policies: %w", len(rules), err)
		}
		return nil
	})
}

// removePolicies 批量删除策略：在事务中先按精确匹配查出目标行主键，
// 再按主键集合一次性删除，每批 batchSize 条规则只需 2 条 SQL。
// 匹配语义与单条 removePolicy 完全一致：尾部不足字段按空串匹配，
// 不会误伤同前缀的其他规则。
// 按 batchSize 分块执行的原因：每条规则元组携带 7 个参数，
// SQL Server 单语句参数上限为 2100，不分块时约 300 条规则即越界；
// 唯一索引保证每个元组至多命中一行，主键集合大小同样以批为界。
// 不用行值 IN（(ptype,v0..) IN ((...))）的原因：SQL Server 不支持该语法；
// 不用单条 OR 条件直接 DELETE 的原因：自定义表若含软删除字段，
// 追加的软删除条件与 OR 的优先级组合存在边缘风险，先查 id 再删则无此问题
func removePolicies(link dbLink, ctx context.Context, sec, ptype string, rules [][]string) error {
	if len(rules) == 0 {
		return nil
	}
	if err := link.checkActive(); err != nil {
		return err
	}
	for _, rule := range rules {
		if err := validateRule(rule); err != nil {
			return fmt.Errorf("g-adapter: ptype %q: %w", ptype, err)
		}
	}
	names := link.Fields()
	return link.transaction(ctx, func(ctx context.Context, _ gdb.TX) error {
		for start := 0; start < len(rules); start += batchSize {
			chunk := rules[start : start+min(batchSize, len(rules)-start)]
			// 第一步：一条 SELECT 精确匹配该批待删规则的主键。
			// 每条规则为一个完整元组条件，用 OR 连接
			// （ruleCondition 自带括号，GoFrame 构建结果形如 (r1) OR (r2) ...）
			m := link.Model(ctx).Where(ruleCondition(names), ruleArgs(names, ptype, chunk[0])...)
			for _, rule := range chunk[1:] {
				m = m.WhereOr(ruleCondition(names), ruleArgs(names, ptype, rule)...)
			}
			records, err := m.All()
			if err != nil {
				return fmt.Errorf("g-adapter: failed to query policies to remove: %w", err)
			}
			if len(records) == 0 {
				continue // 该批没有匹配行，无需删除
			}
			// 第二步：一条 DELETE 按主键集合删除
			idArgs := make([]any, len(records))
			for i, record := range records {
				idArgs[i] = record[names.ID].Val()
			}
			if _, err = link.Model(ctx).WhereIn(names.ID, idArgs).Delete(); err != nil {
				return fmt.Errorf("g-adapter: failed to remove %d policies: %w", len(idArgs), err)
			}
		}
		return nil
	})
}

// ruleCondition 生成单条规则的精确匹配 SQL 条件（ptype 与 v0-v5 全等比较，
// 不足 6 个字段的部分按空串匹配），与 applyRuleWhere 的匹配语义一致，
// 用于批量删除时的 OR 拼接
func ruleCondition(names *FieldName) string {
	cond := names.PType + "=?"
	for _, name := range names.ValueNames() {
		cond += " AND " + name + "=?"
	}
	return "(" + cond + ")"
}

// ruleArgs 生成与 ruleCondition 占位符一一对应的参数（共 7 个）
func ruleArgs(names *FieldName, ptype string, rule []string) []any {
	args := make([]any, 0, ruleFieldCount+1)
	args = append(args, ptype)
	for i := 0; i < ruleFieldCount; i++ {
		if i < len(rule) {
			args = append(args, rule[i])
		} else {
			args = append(args, "") // 尾部不足按空串精确匹配
		}
	}
	return args
}

// ==================== Adapter 批量接口实现 ====================

// AddPolicies 实现 persist.BatchAdapter：批量添加策略
func (a *Adapter) AddPolicies(sec string, ptype string, rules [][]string) error {
	return a.AddPoliciesCtx(context.Background(), sec, ptype, rules)
}

// AddPoliciesCtx 实现 persist.ContextBatchAdapter：带 context 批量添加策略
func (a *Adapter) AddPoliciesCtx(ctx context.Context, sec string, ptype string, rules [][]string) error {
	return addPolicies(a, ctx, sec, ptype, rules)
}

// RemovePolicies 实现 persist.BatchAdapter：批量删除策略
func (a *Adapter) RemovePolicies(sec string, ptype string, rules [][]string) error {
	return a.RemovePoliciesCtx(context.Background(), sec, ptype, rules)
}

// RemovePoliciesCtx 实现 persist.ContextBatchAdapter：带 context 批量删除策略
func (a *Adapter) RemovePoliciesCtx(ctx context.Context, sec string, ptype string, rules [][]string) error {
	return removePolicies(a, ctx, sec, ptype, rules)
}

// ==================== 事务链路批量接口实现 ====================
// 非 Ctx 版本统一使用事务开启时保存的 ctx

// AddPolicies 事务链路：批量添加策略
func (ta *txAdapter) AddPolicies(sec string, ptype string, rules [][]string) error {
	return ta.AddPoliciesCtx(ta.ctx, sec, ptype, rules)
}

// AddPoliciesCtx 事务链路：带 context 批量添加策略
func (ta *txAdapter) AddPoliciesCtx(ctx context.Context, sec string, ptype string, rules [][]string) error {
	return addPolicies(ta, ctx, sec, ptype, rules)
}

// RemovePolicies 事务链路：批量删除策略
func (ta *txAdapter) RemovePolicies(sec string, ptype string, rules [][]string) error {
	return ta.RemovePoliciesCtx(ta.ctx, sec, ptype, rules)
}

// RemovePoliciesCtx 事务链路：带 context 批量删除策略
func (ta *txAdapter) RemovePoliciesCtx(ctx context.Context, sec string, ptype string, rules [][]string) error {
	return removePolicies(ta, ctx, sec, ptype, rules)
}
