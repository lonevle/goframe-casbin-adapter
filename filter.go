package gadapter

import (
	"context"
	"fmt"

	"github.com/casbin/casbin/v3/model"
	"github.com/casbin/casbin/v3/persist"
)

// Filter 策略过滤条件，用于 LoadFilteredPolicy 的子集加载。
// P 为 p 分区的过滤值，G 为 g 分区的过滤值，
// 每个元素依次对应 v0、v1、v2...，空字符串表示通配。
//
// 注意：nil 或空切片同样表示"该分区不过滤"（全量加载该分区），
// 与官方 gorm 适配器行为一致。多租户场景若只想按 G 过滤而 P 留空，
// 会加载全部租户的 p 策略——需要限定分区时必须显式传过滤值
type Filter struct {
	P []string // p 策略过滤条件
	G []string // g 策略过滤条件
}

// loadFilteredPolicy 加载符合过滤条件的策略到 casbin 模型。
// 分别按 P、G 条件过滤 p 分区（ptype 以 p 开头）与 g 分区（ptype 以 g 开头）。
// filter 为 nil 或 *Filter 类型的空指针时返回 ErrInvalidFilter
// （显式报错而非全量加载：nil 全量加载会与 isFiltered 标记语义矛盾——
// "过滤加载"却加载了全量；官方 gorm 适配器对 nil 同样报错）
func loadFilteredPolicy(link dbLink, ctx context.Context, m model.Model, filter interface{}) error {
	if m == nil {
		return ErrNilModel
	}
	if err := link.checkActive(); err != nil {
		return err
	}
	// 类型断言对值为 nil 的 *Filter 同样成功（typed-nil），
	// 必须显式判空，否则后续 f.P 取值会 panic
	f, ok := filter.(*Filter)
	if !ok || f == nil {
		return ErrInvalidFilter
	}
	// 依次加载 p 分区与 g 分区的过滤结果（固定顺序，行为可预期）
	sections := []struct {
		sec    string
		values []string
	}{
		{sectionPolicy, f.P},
		{sectionGroup, f.G},
	}
	for _, s := range sections {
		if err := loadFilteredSection(link, ctx, m, s.sec, s.values); err != nil {
			return err
		}
	}
	return nil
}

// loadFilteredSection 加载指定分区中符合过滤条件的策略。
// ptype 按分区前缀匹配（p 分区包含 p、p2 等），过滤值从 v0 起对应
func loadFilteredSection(link dbLink, ctx context.Context, m model.Model, sec string, values []string) error {
	names := link.Fields()
	mdl := applySectionWhere(link.Model(ctx), names, sec, values)
	records, err := mdl.OrderAsc(names.ID).All()
	if err != nil {
		return fmt.Errorf("g-adapter: failed to load filtered %s policies: %w", sec, err)
	}
	for _, record := range records {
		rule, ok := recordToRule(record, names)
		if !ok {
			continue // 跳过无效行
		}
		if err = persist.LoadPolicyArray(rule, m); err != nil {
			return fmt.Errorf("g-adapter: failed to load filtered policy rule %v: %w", rule, err)
		}
	}
	return nil
}

// ==================== Adapter 过滤接口实现 ====================

// LoadFilteredPolicy 实现 persist.FilteredAdapter：加载符合过滤条件的策略子集
func (a *Adapter) LoadFilteredPolicy(m model.Model, filter interface{}) error {
	return a.LoadFilteredPolicyCtx(context.Background(), m, filter)
}

// LoadFilteredPolicyCtx 实现 persist.ContextFilteredAdapter：带 context 加载过滤策略子集
func (a *Adapter) LoadFilteredPolicyCtx(ctx context.Context, m model.Model, filter interface{}) error {
	err := loadFilteredPolicy(a, ctx, m, filter)
	if err == nil {
		a.isFiltered.Store(true) // 子集加载后标记为已过滤
	}
	return err
}

// IsFiltered 实现 persist.FilteredAdapter：返回当前策略是否经过过滤加载
func (a *Adapter) IsFiltered() bool {
	return a.isFiltered.Load()
}

// IsFilteredCtx 实现 persist.ContextFilteredAdapter：带 context 查询过滤状态
func (a *Adapter) IsFilteredCtx(ctx context.Context) bool {
	return a.isFiltered.Load()
}

// ==================== 事务链路过滤接口实现 ====================
// 非 Ctx 版本统一使用事务开启时保存的 ctx

// LoadFilteredPolicy 事务链路：加载符合过滤条件的策略子集
func (ta *txAdapter) LoadFilteredPolicy(m model.Model, filter interface{}) error {
	return ta.LoadFilteredPolicyCtx(ta.ctx, m, filter)
}

// LoadFilteredPolicyCtx 事务链路：带 context 加载过滤策略子集
func (ta *txAdapter) LoadFilteredPolicyCtx(ctx context.Context, m model.Model, filter interface{}) error {
	err := loadFilteredPolicy(ta, ctx, m, filter)
	if err == nil {
		ta.isFiltered.Store(true)
	}
	return err
}

// IsFiltered 事务链路：返回当前策略是否经过过滤加载
func (ta *txAdapter) IsFiltered() bool {
	return ta.isFiltered.Load()
}

// IsFilteredCtx 事务链路：带 context 查询过滤状态
func (ta *txAdapter) IsFilteredCtx(ctx context.Context) bool {
	return ta.isFiltered.Load()
}
