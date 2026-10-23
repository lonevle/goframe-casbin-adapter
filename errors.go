package gadapter

import "errors"

// 包级错误定义，调用方可通过 errors.Is 精确判断错误类型
var (
	// ErrDBRequired 未提供数据库实例
	ErrDBRequired = errors.New("g-adapter: database instance is required")
	// ErrNilModel 未提供 casbin 策略模型
	ErrNilModel = errors.New("g-adapter: model is required")
	// ErrInvalidFilter 过滤器类型错误，必须为 *Filter
	ErrInvalidFilter = errors.New("g-adapter: filter must be *Filter")
	// ErrRulesLengthMismatch 批量更新时新旧规则数量不一致
	ErrRulesLengthMismatch = errors.New("g-adapter: old rules and new rules must have the same length")
	// ErrInvalidFieldIndex 字段起始索引越界（合法范围 -1 与 0-5）
	ErrInvalidFieldIndex = errors.New("g-adapter: field index out of range")
	// ErrTransactionFinished 事务已提交或已回滚，不能重复结束
	ErrTransactionFinished = errors.New("g-adapter: transaction already finished")
	// ErrTooManyRuleFields 规则字段数超过表结构上限（v0-v5 共 6 个）。
	// 明确报错而非静默截断：截断会让数据悄悄丢失，加载时因规则长度
	// 与模型不匹配而失败，把错误推迟并模糊化
	ErrTooManyRuleFields = errors.New("g-adapter: rule has more than 6 value fields, which exceeds the table capacity (v0-v5)")
)
