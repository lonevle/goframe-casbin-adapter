package gadapter

import "fmt"

// defaultTableName 默认策略表名
const defaultTableName = "casbin_rule"

// FieldName 策略表字段名配置，所有 SQL 的构建均以此为准，
// 因此自定义字段名后无需改动任何逻辑即可生效
type FieldName struct {
	ID    string // 主键字段名，仅用于加载时的稳定排序
	PType string // 策略类型字段名（p / p2 / g / g2 等）
	V0    string // 规则字段 V0，与策略行第一个字段对应
	V1    string // 规则字段 V1
	V2    string // 规则字段 V2
	V3    string // 规则字段 V3
	V4    string // 规则字段 V4
	V5    string // 规则字段 V5
}

// ValueNames 按顺序返回 V0-V5 的字段名，便于按下标遍历
func (fn *FieldName) ValueNames() []string {
	return []string{fn.V0, fn.V1, fn.V2, fn.V3, fn.V4, fn.V5}
}

// DefaultFieldName 返回 casbin 生态约定的默认字段名配置
func DefaultFieldName() *FieldName {
	return &FieldName{
		ID:    "id",
		PType: "ptype",
		V0:    "v0",
		V1:    "v1",
		V2:    "v2",
		V3:    "v3",
		V4:    "v4",
		V5:    "v5",
	}
}

// Options 适配器配置项
type Options struct {
	TableName  string     // 策略表名
	FieldNames *FieldName // 策略表字段名配置
}

// validate 拒绝含空值的字段名配置（按声明顺序报出第一个空值字段）：
// 空字段名会生成非法 SQL（如 `=""` 条件或空排序字段），错误发生在
// 数据库端且信息晦涩，应在构造期拦截
func (fn *FieldName) validate() error {
	for _, d := range []struct{ label, value string }{
		{"ID", fn.ID}, {"PType", fn.PType},
		{"V0", fn.V0}, {"V1", fn.V1}, {"V2", fn.V2},
		{"V3", fn.V3}, {"V4", fn.V4}, {"V5", fn.V5},
	} {
		if d.value == "" {
			return fmt.Errorf("%w: %s", ErrEmptyFieldName, d.label)
		}
	}
	return nil
}

// newOptionsWithDefaults 应用全部函数式配置并填充默认值，
// 自定义字段名配置含空值时返回 ErrEmptyFieldName
func newOptionsWithDefaults(opts ...Option) (Options, error) {
	o := Options{}
	for _, opt := range opts {
		opt(&o)
	}
	// 表名缺省时使用默认值
	if o.TableName == "" {
		o.TableName = defaultTableName
	}
	switch {
	case o.FieldNames == nil:
		o.FieldNames = DefaultFieldName()
	default:
		if err := o.FieldNames.validate(); err != nil {
			return Options{}, err
		}
	}
	return o, nil
}

// Option 函数式配置项
type Option func(*Options)

// WithTableName 设置策略表名
func WithTableName(name string) Option {
	return func(o *Options) {
		o.TableName = name
	}
}

// WithFieldNames 设置策略表字段名配置
func WithFieldNames(names *FieldName) Option {
	return func(o *Options) {
		o.FieldNames = names
	}
}
