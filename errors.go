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
	// ErrEmptyRule 规则为空。空规则落库后仅含 ptype，加载时因规则长度
	// 与模型定义不符而整体失败，写入端直接拒绝
	ErrEmptyRule = errors.New("g-adapter: rule must not be empty")
	// ErrTrailingEmptyField 规则尾部字段为空串。读取端必须裁剪尾部空串
	// （v0-v5 数据库缺省值为空串），尾部空串的规则无法从数据库还原，
	// 写入端拒绝以保证写读对称
	ErrTrailingEmptyField = errors.New("g-adapter: rule must not end with an empty field (trailing empty fields are trimmed on load)")
	// ErrPolicySizeMismatch 规则长度与模型定义不一致（p 分区要求与
	// token 数完全相等，g 分区要求不小于），与 casbin 加载端的校验一致；
	// 不一致的规则落库后会让 LoadPolicy 整体失败
	ErrPolicySizeMismatch = errors.New("g-adapter: policy rule size does not match model definition")
	// ErrPolicyNotFound 更新策略时旧规则在数据库中不存在（内存与数据库失联）
	ErrPolicyNotFound = errors.New("g-adapter: old policy not found in storage")
	// ErrEmptyFieldName 字段名配置含空值：会生成非法 SQL（如 `=""` 条件），
	// 报错发生在数据库端且信息晦涩，应在构造期拦截
	ErrEmptyFieldName = errors.New("g-adapter: field name must not be empty")
)
