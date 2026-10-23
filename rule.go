package gadapter

import (
	"fmt"

	"github.com/casbin/casbin/v3/model"
	"github.com/gogf/gf/v2/database/gdb"
)

// casbin 模型固定分区：p 为策略分区，g 为角色关系分区
const (
	sectionPolicy = "p"
	sectionGroup  = "g"
)

// ruleFieldCount 规则字段数量上限：v0-v5 共 6 个。
// 这是 casbin 生态表结构的普遍上限（官方 gorm/xorm 适配器的
// CasbinRule 结构体同样固定 6 个值字段），模型定义超过 6 个
// 值字段的场景无法用本适配器持久化
const ruleFieldCount = 6

// batchSize 批量写入时每批的行数
const batchSize = 100

// validateRuleLen 校验规则字段数不超过表容量（6 个）。
// 写入路径统一调用：超限直接报错而非静默截断，
// 截断会导致写库数据与模型定义不一致（保存成功、加载失败）
func validateRuleLen(rule []string) error {
	if len(rule) > ruleFieldCount {
		return fmt.Errorf("%w: got %d fields, rule: %v", ErrTooManyRuleFields, len(rule), rule)
	}
	return nil
}

// ruleToData 将一条策略规则转换为待写入数据库的行数据。
// ptype 为策略类型，rule 为规则值（长度已由调用方校验不超过 6）；
// 未填写的 v 字段不出现在行数据中，由数据库默认值（空串）填充
func ruleToData(names *FieldName, ptype string, rule []string) gdb.Map {
	data := gdb.Map{names.PType: ptype}
	valueNames := names.ValueNames()
	for i, v := range rule {
		if i >= ruleFieldCount {
			break // 防御分支：正常情况下 validateRuleLen 已拦截
		}
		data[valueNames[i]] = v
	}
	return data
}

// ruleToFullData 与 ruleToData 类似，但将不足 6 个的尾部字段补为空串，
// 用于更新场景，确保旧行中的残留值被清空
func ruleToFullData(names *FieldName, ptype string, rule []string) gdb.Map {
	data := gdb.Map{names.PType: ptype}
	valueNames := names.ValueNames()
	for i := 0; i < ruleFieldCount; i++ {
		if i < len(rule) {
			data[valueNames[i]] = rule[i]
		} else {
			data[valueNames[i]] = ""
		}
	}
	return data
}

// modelToData 将 casbin 模型中的全部策略导出为待写入的行数据，
// 依次遍历 p 分区与 g 分区的所有规则；
// 任一规则超过 6 个字段时返回 ErrTooManyRuleFields
func modelToData(names *FieldName, m model.Model) (gdb.List, error) {
	var list gdb.List
	for _, sec := range []string{sectionPolicy, sectionGroup} {
		for ptype, ast := range m[sec] {
			for _, rule := range ast.Policy {
				if err := validateRuleLen(rule); err != nil {
					return nil, fmt.Errorf("g-adapter: ptype %q: %w", ptype, err)
				}
				list = append(list, ruleToData(names, ptype, rule))
			}
		}
	}
	return list, nil
}

// recordToRule 将数据库行记录还原为策略规则，规则首元素为策略类型 ptype。
// ok 为 false 表示记录缺失策略类型（无效行），调用方应跳过。
// 尾部的空字段会被裁剪，保证规则长度与模型定义一致
func recordToRule(record gdb.Record, names *FieldName) (rule []string, ok bool) {
	ptype, exists := record[names.PType]
	if !exists || ptype == nil || ptype.String() == "" {
		return nil, false
	}
	rule = append(rule, ptype.String())
	for _, name := range names.ValueNames() {
		if v, exists := record[name]; exists && v != nil {
			rule = append(rule, v.String())
		} else {
			rule = append(rule, "")
		}
	}
	// 裁剪尾部空字段：如 ["p","alice","data1","read","","",""] → ["p","alice","data1","read"]
	for len(rule) > 1 && rule[len(rule)-1] == "" {
		rule = rule[:len(rule)-1]
	}
	return rule, true
}

// applyRuleWhere 为查询追加精确匹配单条规则的条件：ptype + v0...vN。
// rule 不足 6 个字段时，剩余字段按空串匹配，确保只命中目标行，
// 避免误删同前缀的其他规则
func applyRuleWhere(m *gdb.Model, names *FieldName, ptype string, rule []string) *gdb.Model {
	m = m.Where(names.PType, ptype)
	valueNames := names.ValueNames()
	for i := 0; i < ruleFieldCount; i++ {
		if i < len(rule) {
			m = m.Where(valueNames[i], rule[i])
		} else {
			m = m.Where(valueNames[i], "")
		}
	}
	return m
}

// applyFilterWhere 为查询追加过滤条件：从第 fieldIndex 个规则字段起，
// 将 fieldValues 依次对应到 v(fieldIndex)...；空字符串视为通配，不参与匹配。
// fieldIndex 为 -1 时忽略 fieldValues，仅按 ptype 匹配
// （官方语义：清空该策略类型下的全部规则）
func applyFilterWhere(m *gdb.Model, names *FieldName, ptype string, fieldIndex int, fieldValues []string) *gdb.Model {
	m = m.Where(names.PType, ptype)
	if fieldIndex < 0 {
		return m
	}
	valueNames := names.ValueNames()
	for i, v := range fieldValues {
		pos := fieldIndex + i
		if v == "" || pos >= ruleFieldCount {
			continue
		}
		m = m.Where(valueNames[pos], v)
	}
	return m
}

// applySectionWhere 为查询追加分区过滤条件：ptype 前缀匹配（如 p、p2 均属 p 分区），
// 随后从 v0 起应用过滤值，空字符串视为通配。用于 LoadFilteredPolicy
func applySectionWhere(m *gdb.Model, names *FieldName, sec string, values []string) *gdb.Model {
	m = m.WhereLike(names.PType, sec+"%")
	valueNames := names.ValueNames()
	for i, v := range values {
		if v == "" || i >= ruleFieldCount {
			continue
		}
		m = m.Where(valueNames[i], v)
	}
	return m
}
