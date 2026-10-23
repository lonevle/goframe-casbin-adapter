package gadapter

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/casbin/casbin/v3"
	"github.com/casbin/casbin/v3/model"
	"github.com/casbin/casbin/v3/persist"

	"github.com/gogf/gf/v2/container/gvar"
	"github.com/gogf/gf/v2/database/gdb"
)

// rbacModelText 内嵌的 RBAC 测试模型，与官方 casbin 示例等价
const rbacModelText = `
[request_definition]
r = sub, obj, act

[policy_definition]
p = sub, obj, act

[role_definition]
g = _, _

[policy_effect]
e = some(where (p.eft == allow))

[matchers]
m = g(r.sub, p.sub) && r.obj == p.obj && r.act == p.act
`

// testPolicies 初始 p 策略（与官方 examples/rbac_policy.csv 一致）
var testPolicies = [][]string{
	{"alice", "data1", "read"},
	{"bob", "data2", "write"},
	{"data2_admin", "data2", "read"},
	{"data2_admin", "data2", "write"},
}

// testGrouping 初始 g 策略（角色关系）
var testGrouping = [][]string{
	{"alice", "data2_admin"},
}

// copyPolicies 深拷贝策略集合，避免测试间共享底层数组互相污染
func copyPolicies(policies [][]string) [][]string {
	out := make([][]string, len(policies))
	for i, p := range policies {
		out[i] = append([]string(nil), p...)
	}
	return out
}

// arrayEquals 判断两组二维字符串数组是否一致（忽略顺序）
func arrayEquals(a, b [][]string) bool {
	if len(a) != len(b) {
		return false
	}
	for _, x := range a {
		found := false
		for _, y := range b {
			if reflect.DeepEqual(x, y) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// newRecord 构造模拟数据库行记录
func newRecord(pType string, values ...string) gdb.Record {
	names := DefaultFieldName()
	record := gdb.Record{names.PType: gvar.New(pType)}
	for i, v := range values {
		record[names.ValueNames()[i]] = gvar.New(v)
	}
	return record
}

// ==================== 纯逻辑单元测试（不依赖数据库） ====================

// mockDriver 假数据库驱动：仅注册驱动类型使 gdb.New 能创建实例，
// 不提供任何实际连接能力（GoFrame 连接是惰性的，不执行 SQL 即不连接）
type mockDriver struct {
	*gdb.Core // 内嵌 Core 复用 gdb.DB 的默认实现
}

// New 实现 gdb.Driver：返回内嵌 Core 的驱动实例
func (mockDriver) New(core *gdb.Core, _ *gdb.ConfigNode) (gdb.DB, error) {
	return &mockDriver{Core: core}, nil
}

// Open 实现 gdb.DB：测试不执行 SQL，不应走到此方法；返回空连接即可
func (mockDriver) Open(*gdb.ConfigNode) (*sql.DB, error) {
	return nil, nil
}

// TableFields 覆盖默认实现：返回预设的策略表结构，
// 使 SELECT 类操作在 DryRun 模式下无需真实连接即可完成
// 字段排序等元数据处理（默认实现会查询数据库表结构）
func (d *mockDriver) TableFields(ctx context.Context, table string, schema ...string) (map[string]*gdb.TableField, error) {
	fields := DefaultFieldName()
	result := make(map[string]*gdb.TableField, ruleFieldCount+2)
	for i, name := range append([]string{fields.ID, fields.PType}, fields.ValueNames()...) {
		result[name] = &gdb.TableField{
			Name:  name,
			Index: i,
			Type:  "varchar(100)",
			Null:  false,
			Key:   "",
			Extra: "",
		}
	}
	return result, nil
}

func init() {
	_ = gdb.Register("mock", mockDriver{})
}

// newFakeDB 创建不实际连接数据库的 gdb 实例（GoFrame 惰性连接），
// 用于验证适配器构造逻辑与状态标记
func newFakeDB(t *testing.T) gdb.DB {
	db, err := gdb.New(gdb.ConfigNode{Type: "mock"})
	if err != nil {
		t.Fatalf("failed to create fake db: %v", err)
	}
	return db
}

// TestNewFilteredAdapter 验证过滤适配器初始状态为已过滤：
// casbin 依赖该标记在 Enforcer 初始化时跳过自动全量加载
func TestNewFilteredAdapter(t *testing.T) {
	db := newFakeDB(t)

	a, err := NewFilteredAdapterByDB(db)
	if err != nil {
		t.Fatalf("failed to create filtered adapter: %v", err)
	}
	if !a.IsFiltered() || !a.IsFilteredCtx(context.Background()) {
		t.Error("filtered adapter should be filtered on creation")
	}

	// 普通适配器初始应为未过滤
	a2, err := NewAdapterByDB(db)
	if err != nil {
		t.Fatalf("failed to create adapter: %v", err)
	}
	if a2.IsFiltered() {
		t.Error("normal adapter should not be filtered on creation")
	}
}

// TestNewAdapterByDBNil 验证空数据库实例返回 ErrDBRequired
func TestNewAdapterByDBNil(t *testing.T) {
	if _, err := NewAdapterByDB(nil); err != ErrDBRequired {
		t.Errorf("expected ErrDBRequired, got %v", err)
	}
}

// TestFieldNameDefaults 验证默认字段名配置
func TestFieldNameDefaults(t *testing.T) {
	names := DefaultFieldName()
	want := []string{"v0", "v1", "v2", "v3", "v4", "v5"}
	if got := names.ValueNames(); !reflect.DeepEqual(got, want) {
		t.Errorf("ValueNames() = %v, want %v", got, want)
	}
	if names.ID != "id" || names.PType != "ptype" {
		t.Errorf("unexpected default field names: %+v", names)
	}
}

// TestOptionsDefaults 验证配置默认值：默认表名与字段名
func TestOptionsDefaults(t *testing.T) {
	o := newOptionsWithDefaults()
	if o.TableName != defaultTableName {
		t.Errorf("TableName = %q, want %q", o.TableName, defaultTableName)
	}
	if o.FieldNames == nil || o.FieldNames.PType != "ptype" {
		t.Errorf("unexpected default field names: %+v", o.FieldNames)
	}

	// 函数式选项覆盖默认值
	o = newOptionsWithDefaults(
		WithTableName("my_rule"),
		WithFieldNames(&FieldName{PType: "p_type", V0: "v_0", V1: "v_1", V2: "v_2", V3: "v_3", V4: "v_4", V5: "v_5"}),
	)
	if o.TableName != "my_rule" || o.FieldNames.PType != "p_type" || o.FieldNames.V0 != "v_0" {
		t.Errorf("options not applied: %+v", o)
	}
}

// TestRuleToData 验证规则到行数据的转换：未填字段不写入、超出字段截断
func TestRuleToData(t *testing.T) {
	names := DefaultFieldName()

	// 正常 3 字段规则
	data := ruleToData(names, "p", []string{"alice", "data1", "read"})
	want := gdb.Map{"ptype": "p", "v0": "alice", "v1": "data1", "v2": "read"}
	if !reflect.DeepEqual(data, want) {
		t.Errorf("ruleToData = %v, want %v", data, want)
	}

	// ruleToData 自身仍保持防御性截断，但写入路径已由 validateRuleLen 前置拦截
	data = ruleToData(names, "p", []string{"a", "b", "c", "d", "e", "f", "g"})
	if len(data) != 7 {
		t.Errorf("expected 7 fields, got %d: %v", len(data), data)
	}
	if data["v5"] != "f" {
		t.Errorf("v5 = %v, want f", data["v5"])
	}
}

// TestValidateRuleLen 验证规则字段数上限校验：
// 6 个字段合法，7 个及以上返回 ErrTooManyRuleFields（不静默截断）
func TestValidateRuleLen(t *testing.T) {
	six := []string{"a", "b", "c", "d", "e", "f"}
	if err := validateRuleLen(six); err != nil {
		t.Errorf("6 fields should be valid, got %v", err)
	}
	if err := validateRuleLen(nil); err != nil {
		t.Errorf("empty rule should be valid, got %v", err)
	}

	seven := append(append([]string(nil), six...), "g")
	err := validateRuleLen(seven)
	if err == nil {
		t.Fatal("7 fields should be rejected")
	}
	if !errors.Is(err, ErrTooManyRuleFields) {
		t.Errorf("expected ErrTooManyRuleFields, got %v", err)
	}
}

// TestAddPolicyTooManyFields 验证超限规则在写入入口被拒绝（无需数据库）
func TestAddPolicyTooManyFields(t *testing.T) {
	db := newFakeDB(t)
	a, err := NewAdapterByDB(db)
	if err != nil {
		t.Fatalf("failed to create adapter: %v", err)
	}
	// 7 字段规则（如 7 值模型 p = sub,obj,act,dom,f4,f5,f6）必须报错而非静默截断
	longRule := []string{"a", "b", "c", "d", "e", "f", "g"}
	err = a.AddPolicy("p", "p", longRule)
	if !errors.Is(err, ErrTooManyRuleFields) {
		t.Errorf("AddPolicy: expected ErrTooManyRuleFields, got %v", err)
	}
	err = a.AddPolicies("p", "p", [][]string{{"a", "b", "c"}, longRule})
	if !errors.Is(err, ErrTooManyRuleFields) {
		t.Errorf("AddPolicies: expected ErrTooManyRuleFields, got %v", err)
	}
	err = a.UpdatePolicy("p", "p", []string{"a", "b"}, longRule)
	if !errors.Is(err, ErrTooManyRuleFields) {
		t.Errorf("UpdatePolicy: expected ErrTooManyRuleFields, got %v", err)
	}
	// SavePolicy 路径：模型内含超限规则同样拒绝
	m, err := model.NewModelFromString(rbacModelText)
	if err != nil {
		t.Fatalf("failed to create model: %v", err)
	}
	if err = m.AddPolicy(sectionPolicy, "p", longRule); err != nil {
		t.Fatalf("failed to add policy to model: %v", err)
	}
	err = a.SavePolicy(m)
	if !errors.Is(err, ErrTooManyRuleFields) {
		t.Errorf("SavePolicy: expected ErrTooManyRuleFields, got %v", err)
	}
}

// TestTxAdapterFinishedFlag 验证事务结束标记与 checkActive 的联动（无需数据库）：
// Commit/Rollback 置位后，txAdapter 的所有操作入口统一返回 ErrTransactionFinished
func TestTxAdapterFinishedFlag(t *testing.T) {
	finished := &atomic.Bool{}
	ta := &txAdapter{finished: finished}

	// 未结束时链路可用
	if err := ta.checkActive(); err != nil {
		t.Errorf("active txAdapter should pass checkActive, got %v", err)
	}
	// 普通链路恒可用
	a := &Adapter{}
	if err := a.checkActive(); err != nil {
		t.Errorf("normal adapter should always pass checkActive, got %v", err)
	}

	// 结束后：checkActive 返回 ErrTransactionFinished
	finished.Store(true)
	if err := ta.checkActive(); !errors.Is(err, ErrTransactionFinished) {
		t.Errorf("finished txAdapter should return ErrTransactionFinished, got %v", err)
	}
}

// TestRuleConditionAndArgs 验证批量删除的 OR 元组条件构造：
// 条件为 ptype 与 v0-v5 的全等比较（自带括号），参数共 7 个且尾部补空串
func TestRuleConditionAndArgs(t *testing.T) {
	names := DefaultFieldName()

	cond := ruleCondition(names)
	want := "(ptype=? AND v0=? AND v1=? AND v2=? AND v3=? AND v4=? AND v5=?)"
	if cond != want {
		t.Errorf("ruleCondition = %q, want %q", cond, want)
	}

	args := ruleArgs(names, "p", []string{"alice", "data1"})
	wantArgs := []any{"p", "alice", "data1", "", "", "", ""}
	if !reflect.DeepEqual(args, wantArgs) {
		t.Errorf("ruleArgs = %v, want %v", args, wantArgs)
	}

	// 自定义字段名时条件随之变化，不影响参数顺序
	custom := &FieldName{PType: "p_type", V0: "v_0", V1: "v_1", V2: "v_2", V3: "v_3", V4: "v_4", V5: "v_5"}
	cond = ruleCondition(custom)
	if cond != "(p_type=? AND v_0=? AND v_1=? AND v_2=? AND v_3=? AND v_4=? AND v_5=?)" {
		t.Errorf("unexpected custom ruleCondition: %q", cond)
	}
}

// TestRuleToFullData 验证更新用行数据：尾部不足字段补空串
func TestRuleToFullData(t *testing.T) {
	names := DefaultFieldName()
	data := ruleToFullData(names, "p", []string{"alice", "data1"})
	want := gdb.Map{
		"ptype": "p", "v0": "alice", "v1": "data1",
		"v2": "", "v3": "", "v4": "", "v5": "",
	}
	if !reflect.DeepEqual(data, want) {
		t.Errorf("ruleToFullData = %v, want %v", data, want)
	}
}

// TestRecordToRule 验证数据库记录到策略规则的还原：尾部裁剪与无效行
func TestRecordToRule(t *testing.T) {
	names := DefaultFieldName()

	// 尾部空字段应被裁剪
	rule, ok := recordToRule(newRecord("p", "alice", "data1", "read", "", "", ""), names)
	if !ok || !reflect.DeepEqual(rule, []string{"p", "alice", "data1", "read"}) {
		t.Errorf("recordToRule = %v, ok = %v", rule, ok)
	}

	// 缺失策略类型的无效行
	if _, ok = recordToRule(gdb.Record{names.PType: gvar.New("")}, names); ok {
		t.Error("empty ptype record should be invalid")
	}
	if _, ok = recordToRule(gdb.Record{}, names); ok {
		t.Error("missing ptype record should be invalid")
	}

	// 中间空字段保留、尾部裁剪
	rule, ok = recordToRule(newRecord("p", "alice", "", "admin"), names)
	if !ok || !reflect.DeepEqual(rule, []string{"p", "alice", "", "admin"}) {
		t.Errorf("recordToRule = %v, ok = %v", rule, ok)
	}
}

// TestModelToData 验证模型导出：p 与 g 分区全部策略
func TestModelToData(t *testing.T) {
	m, err := model.NewModelFromString(rbacModelText)
	if err != nil {
		t.Fatalf("failed to create model: %v", err)
	}
	// 添加 p 与 g 策略
	if err = m.AddPolicy(sectionPolicy, "p", []string{"alice", "data1", "read"}); err != nil {
		t.Fatalf("failed to add p policy: %v", err)
	}
	if err = m.AddPolicy(sectionGroup, "g", []string{"alice", "admin"}); err != nil {
		t.Fatalf("failed to add g policy: %v", err)
	}

	names := DefaultFieldName()
	list, err := modelToData(names, m)
	if err != nil {
		t.Fatalf("modelToData failed: %v", err)
	}
	want := gdb.List{
		{"ptype": "p", "v0": "alice", "v1": "data1", "v2": "read"},
		{"ptype": "g", "v0": "alice", "v1": "admin"},
	}
	if !reflect.DeepEqual(list, want) {
		t.Errorf("modelToData = %v, want %v", list, want)
	}
}

// ==================== 集成测试（依赖真实数据库，未配置时自动跳过） ====================

// newTestDB 依据环境变量创建测试数据库连接：
// GADAPTER_TEST_TYPE 数据库类型（mysql / mssql，默认 mysql）
// GADAPTER_TEST_LINK 连接串（如 mysql:root:123456@tcp(127.0.0.1:3306)/casbin）
func newTestDB(t *testing.T) gdb.DB {
	link := os.Getenv("GADAPTER_TEST_LINK")
	if link == "" {
		t.Skip(`skip database integration tests: set GADAPTER_TEST_LINK (e.g. "mysql:root:123456@tcp(127.0.0.1:3306)/casbin") to enable`)
	}
	dbType := os.Getenv("GADAPTER_TEST_TYPE")
	if dbType == "" {
		dbType = "mysql"
	}
	db, err := gdb.New(gdb.ConfigNode{Type: dbType, Link: link})
	if err != nil {
		t.Fatalf("failed to create db: %v", err)
	}
	return db
}

// newTestAdapter 创建测试适配器并清空策略表
func newTestAdapter(t *testing.T) *Adapter {
	db := newTestDB(t)
	a, err := NewAdapterByDB(db)
	if err != nil {
		t.Fatalf("failed to create adapter: %v", err)
	}
	// 清空策略表，保证测试从空白状态开始
	if _, err = db.Exec(context.Background(), "TRUNCATE TABLE "+defaultTableName); err != nil {
		t.Fatalf("failed to truncate table: %v", err)
	}
	return a
}

// newTestEnforcer 基于适配器创建测试 enforcer（创建时自动加载策略）
func newTestEnforcer(t *testing.T, a *Adapter) *casbin.Enforcer {
	m, err := model.NewModelFromString(rbacModelText)
	if err != nil {
		t.Fatalf("failed to create model: %v", err)
	}
	e, err := casbin.NewEnforcer(m, a)
	if err != nil {
		t.Fatalf("failed to create enforcer: %v", err)
	}
	return e
}

// initPolicy 初始化数据库中的策略：写入初始策略并重新加载验证
func initPolicy(t *testing.T, a *Adapter) {
	e := newTestEnforcer(t, a)
	for _, rule := range testPolicies {
		if _, err := e.AddPolicy(rule); err != nil {
			t.Fatalf("failed to add policy %v: %v", rule, err)
		}
	}
	for _, rule := range testGrouping {
		if _, err := e.AddGroupingPolicy(rule); err != nil {
			t.Fatalf("failed to add grouping policy %v: %v", rule, err)
		}
	}
	// 保存到数据库
	if err := a.SavePolicy(e.GetModel()); err != nil {
		t.Fatalf("failed to save policy: %v", err)
	}
	// 清空内存后重新加载，验证落库成功
	e.ClearPolicy()
	if err := a.LoadPolicy(e.GetModel()); err != nil {
		t.Fatalf("failed to load policy: %v", err)
	}
	testGetPolicy(t, e, testPolicies)
}

// testGetPolicy 校验 enforcer 当前 p 策略与期望一致
func testGetPolicy(t *testing.T, e *casbin.Enforcer, want [][]string) {
	got, err := e.GetPolicy()
	if err != nil {
		t.Fatalf("failed to get policy: %v", err)
	}
	if !arrayEquals(got, want) {
		t.Errorf("policy = %v, supposed to be %v", got, want)
	}
}

// TestSaveLoad 验证保存与加载的完整回路
func TestSaveLoad(t *testing.T) {
	a := newTestAdapter(t)
	initPolicy(t, a)

	// 默认表名验证
	if a.GetTableName() != defaultTableName {
		t.Errorf("GetTableName() = %q, want %q", a.GetTableName(), defaultTableName)
	}

	// 直接基于适配器创建 enforcer，自动加载应得到全部策略
	e := newTestEnforcer(t, a)
	testGetPolicy(t, e, testPolicies)

	// g 策略验证
	got, err := e.GetGroupingPolicy()
	if err != nil {
		t.Fatalf("failed to get grouping policy: %v", err)
	}
	if !arrayEquals(got, testGrouping) {
		t.Errorf("grouping policy = %v, supposed to be %v", got, testGrouping)
	}
}

// TestAutoSave 验证 AutoSave 开关行为：关闭时仅内存生效，开启时同步落库
func TestAutoSave(t *testing.T) {
	a := newTestAdapter(t)
	initPolicy(t, a)

	e := newTestEnforcer(t, a)
	e.EnableAutoSave(false)
	// AutoSave 关闭：策略变更不落库
	if _, err := e.AddPolicy("alice", "data1", "write"); err != nil {
		t.Fatalf("failed to add policy: %v", err)
	}
	if err := e.LoadPolicy(); err != nil {
		t.Fatalf("failed to load policy: %v", err)
	}
	testGetPolicy(t, e, testPolicies)

	// AutoSave 开启：策略变更同步落库
	e.EnableAutoSave(true)
	if _, err := e.AddPolicy("alice", "data1", "write"); err != nil {
		t.Fatalf("failed to add policy: %v", err)
	}
	if err := e.LoadPolicy(); err != nil {
		t.Fatalf("failed to load policy: %v", err)
	}
	expected := append(copyPolicies(testPolicies), []string{"alice", "data1", "write"})
	testGetPolicy(t, e, expected)

	// 删除新增策略
	if _, err := e.RemovePolicy("alice", "data1", "write"); err != nil {
		t.Fatalf("failed to remove policy: %v", err)
	}
	if err := e.LoadPolicy(); err != nil {
		t.Fatalf("failed to load policy: %v", err)
	}
	testGetPolicy(t, e, testPolicies)

	// 按条件删除：移除 data2_admin 相关策略
	if _, err := e.RemoveFilteredPolicy(0, "data2_admin"); err != nil {
		t.Fatalf("failed to remove filtered policy: %v", err)
	}
	if err := e.LoadPolicy(); err != nil {
		t.Fatalf("failed to load policy: %v", err)
	}
	testGetPolicy(t, e, [][]string{{"alice", "data1", "read"}, {"bob", "data2", "write"}})
}

// TestUpdatePolicy 验证单条策略更新
func TestUpdatePolicy(t *testing.T) {
	a := newTestAdapter(t)
	initPolicy(t, a)

	e := newTestEnforcer(t, a)
	if _, err := e.UpdatePolicy(
		[]string{"alice", "data1", "read"},
		[]string{"alice", "data1", "write"},
	); err != nil {
		t.Fatalf("failed to update policy: %v", err)
	}
	if err := e.LoadPolicy(); err != nil {
		t.Fatalf("failed to load policy: %v", err)
	}
	expected := [][]string{
		{"alice", "data1", "write"},
		{"bob", "data2", "write"},
		{"data2_admin", "data2", "read"},
		{"data2_admin", "data2", "write"},
	}
	testGetPolicy(t, e, expected)
}

// TestUpdatePolicies 验证批量更新
func TestUpdatePolicies(t *testing.T) {
	a := newTestAdapter(t)
	initPolicy(t, a)

	e := newTestEnforcer(t, a)
	oldRules := [][]string{
		{"alice", "data1", "read"},
		{"bob", "data2", "write"},
	}
	newRules := [][]string{
		{"alice", "data1", "write"},
		{"bob", "data2", "read"},
	}
	if _, err := e.UpdatePolicies(oldRules, newRules); err != nil {
		t.Fatalf("failed to update policies: %v", err)
	}
	if err := e.LoadPolicy(); err != nil {
		t.Fatalf("failed to load policy: %v", err)
	}
	expected := [][]string{
		{"alice", "data1", "write"},
		{"bob", "data2", "read"},
		{"data2_admin", "data2", "read"},
		{"data2_admin", "data2", "write"},
	}
	testGetPolicy(t, e, expected)
}

// TestUpdateFilteredPolicies 验证按条件替换策略
func TestUpdateFilteredPolicies(t *testing.T) {
	a := newTestAdapter(t)
	initPolicy(t, a)

	e := newTestEnforcer(t, a)
	// 将 data2_admin 的两条策略替换为一条
	if _, err := e.UpdateFilteredPolicies(
		[][]string{{"data2_admin", "data2", "all"}},
		0, "data2_admin",
	); err != nil {
		t.Fatalf("failed to update filtered policies: %v", err)
	}
	if err := e.LoadPolicy(); err != nil {
		t.Fatalf("failed to load policy: %v", err)
	}
	expected := [][]string{
		{"alice", "data1", "read"},
		{"bob", "data2", "write"},
		{"data2_admin", "data2", "all"},
	}
	testGetPolicy(t, e, expected)
}

// TestBatchOperations 验证批量增删
func TestBatchOperations(t *testing.T) {
	a := newTestAdapter(t)
	initPolicy(t, a)

	e := newTestEnforcer(t, a)
	rules := [][]string{
		{"tom", "data3", "read"},
		{"jerry", "data4", "write"},
	}
	// 批量添加
	if _, err := e.AddPolicies(rules); err != nil {
		t.Fatalf("failed to add policies: %v", err)
	}
	if err := e.LoadPolicy(); err != nil {
		t.Fatalf("failed to load policy: %v", err)
	}
	expected := append(copyPolicies(testPolicies), rules...)
	testGetPolicy(t, e, expected)

	// 批量删除
	if _, err := e.RemovePolicies(rules); err != nil {
		t.Fatalf("failed to remove policies: %v", err)
	}
	if err := e.LoadPolicy(); err != nil {
		t.Fatalf("failed to load policy: %v", err)
	}
	testGetPolicy(t, e, testPolicies)
}

// TestLoadFilteredPolicy 验证过滤加载子集
func TestLoadFilteredPolicy(t *testing.T) {
	a := newTestAdapter(t)
	initPolicy(t, a)

	e := newTestEnforcer(t, a)
	// 仅加载 v0=bob 的 p 策略
	if err := e.LoadFilteredPolicy(&Filter{P: []string{"bob", "", ""}}); err != nil {
		t.Fatalf("failed to load filtered policy: %v", err)
	}
	testGetPolicy(t, e, [][]string{{"bob", "data2", "write"}})
	if !e.IsFiltered() {
		t.Error("enforcer should be filtered")
	}

	// 全量加载后过滤标记应被清除
	if err := e.LoadPolicy(); err != nil {
		t.Fatalf("failed to load policy: %v", err)
	}
	if e.IsFiltered() {
		t.Error("enforcer should not be filtered after full load")
	}
}

// TestTransaction 验证事务提交与回滚
func TestTransaction(t *testing.T) {
	a := newTestAdapter(t)
	initPolicy(t, a)

	ctx := context.Background()

	// 提交路径：事务内新增策略并提交
	tc, err := a.BeginTransaction(ctx)
	if err != nil {
		t.Fatalf("failed to begin transaction: %v", err)
	}
	if err = tc.GetAdapter().AddPolicy("p", "p", []string{"alice", "data1", "write"}); err != nil {
		t.Fatalf("failed to add policy in transaction: %v", err)
	}
	if err = tc.Commit(); err != nil {
		t.Fatalf("failed to commit transaction: %v", err)
	}
	e := newTestEnforcer(t, a)
	expected := append(copyPolicies(testPolicies), []string{"alice", "data1", "write"})
	testGetPolicy(t, e, expected)

	// 回滚路径：事务内删除策略但回滚，数据应保持不变
	tc, err = a.BeginTransaction(ctx)
	if err != nil {
		t.Fatalf("failed to begin transaction: %v", err)
	}
	if err = tc.GetAdapter().RemovePolicy("p", "p", []string{"alice", "data1", "write"}); err != nil {
		t.Fatalf("failed to remove policy in transaction: %v", err)
	}
	if err = tc.Rollback(); err != nil {
		t.Fatalf("failed to rollback transaction: %v", err)
	}
	e = newTestEnforcer(t, a)
	testGetPolicy(t, e, expected)
}

// TestTransactionContextAdapter 验证事务适配器实现各扩展接口，
// 确保事务内可通过接口断言使用批量、更新等全部能力
func TestTransactionContextAdapter(t *testing.T) {
	a := newTestAdapter(t)
	initPolicy(t, a)

	ctx := context.Background()
	tc, err := a.BeginTransaction(ctx)
	if err != nil {
		t.Fatalf("failed to begin transaction: %v", err)
	}
	txa := tc.GetAdapter()
	// 事务适配器应支持批量、更新等全部扩展接口
	batchA, ok := txa.(persist.BatchAdapter)
	if !ok {
		t.Fatal("transaction adapter should implement BatchAdapter")
	}
	if _, ok = txa.(persist.ContextUpdatableAdapter); !ok {
		t.Fatal("transaction adapter should implement ContextUpdatableAdapter")
	}
	if err = batchA.AddPolicies("p", "p", [][]string{{"tom", "data3", "read"}}); err != nil {
		t.Fatalf("failed to add policies in transaction: %v", err)
	}
	if err = tc.Commit(); err != nil {
		t.Fatalf("failed to commit transaction: %v", err)
	}
	e := newTestEnforcer(t, a)
	expected := append(copyPolicies(testPolicies), []string{"tom", "data3", "read"})
	testGetPolicy(t, e, expected)
}

// TestSavePolicyEmptyModel 验证空模型全量保存后表被清空
// （覆盖 savePolicy 的无条件清表路径）
func TestSavePolicyEmptyModel(t *testing.T) {
	a := newTestAdapter(t)
	initPolicy(t, a)

	e := newTestEnforcer(t, a)
	e.ClearPolicy()
	if err := a.SavePolicy(e.GetModel()); err != nil {
		t.Fatalf("failed to save empty policy: %v", err)
	}
	if err := e.LoadPolicy(); err != nil {
		t.Fatalf("failed to load policy: %v", err)
	}
	testGetPolicy(t, e, nil)
}

// TestRemoveFilteredPolicyFieldIndexAll 验证 fieldIndex=-1 的官方兼容语义：
// 忽略 fieldValues，清空该 ptype 的全部策略
func TestRemoveFilteredPolicyFieldIndexAll(t *testing.T) {
	a := newTestAdapter(t)
	initPolicy(t, a)

	e := newTestEnforcer(t, a)
	if _, err := e.RemoveFilteredPolicy(-1); err != nil {
		t.Fatalf("failed to remove filtered policy with fieldIndex -1: %v", err)
	}
	if err := e.LoadPolicy(); err != nil {
		t.Fatalf("failed to load policy: %v", err)
	}
	testGetPolicy(t, e, nil)

	// 越界索引仍应报错（-2 与 6 均非法）
	for _, idx := range []int{-2, 6} {
		if err := a.RemoveFilteredPolicy("p", "p", idx, "alice"); err != ErrInvalidFieldIndex {
			t.Errorf("fieldIndex %d: expected ErrInvalidFieldIndex, got %v", idx, err)
		}
	}
}

// TestTransactionDoubleFinish 验证事务防重复结束：
// 提交或回滚后再调用 Commit/Rollback 均返回 ErrTransactionFinished
func TestTransactionDoubleFinish(t *testing.T) {
	a := newTestAdapter(t)
	initPolicy(t, a)

	ctx := context.Background()

	// 提交后再提交/回滚
	tc, err := a.BeginTransaction(ctx)
	if err != nil {
		t.Fatalf("failed to begin transaction: %v", err)
	}
	if err = tc.Commit(); err != nil {
		t.Fatalf("failed to commit transaction: %v", err)
	}
	if err = tc.Commit(); err != ErrTransactionFinished {
		t.Errorf("second commit: expected ErrTransactionFinished, got %v", err)
	}
	if err = tc.Rollback(); err != ErrTransactionFinished {
		t.Errorf("rollback after commit: expected ErrTransactionFinished, got %v", err)
	}

	// 回滚后再提交/回滚
	tc, err = a.BeginTransaction(ctx)
	if err != nil {
		t.Fatalf("failed to begin transaction: %v", err)
	}
	if err = tc.Rollback(); err != nil {
		t.Fatalf("failed to rollback transaction: %v", err)
	}
	if err = tc.Commit(); err != ErrTransactionFinished {
		t.Errorf("commit after rollback: expected ErrTransactionFinished, got %v", err)
	}
	if err = tc.Rollback(); err != ErrTransactionFinished {
		t.Errorf("second rollback: expected ErrTransactionFinished, got %v", err)
	}
}

// TestTransactionAdapterAfterFinish 验证事务结束后 GetAdapter() 返回的
// 适配器拒绝一切数据库操作（返回明确的 ErrTransactionFinished
// 而非晦涩的驱动层错误）
func TestTransactionAdapterAfterFinish(t *testing.T) {
	a := newTestAdapter(t)
	initPolicy(t, a)

	ctx := context.Background()
	tc, err := a.BeginTransaction(ctx)
	if err != nil {
		t.Fatalf("failed to begin transaction: %v", err)
	}
	txa := tc.GetAdapter()
	if err = txa.AddPolicy("p", "p", []string{"tom", "data3", "read"}); err != nil {
		t.Fatalf("add policy in active transaction should succeed: %v", err)
	}
	if err = tc.Commit(); err != nil {
		t.Fatalf("failed to commit transaction: %v", err)
	}

	// 提交后：全部操作入口统一返回 ErrTransactionFinished
	if err = txa.AddPolicy("p", "p", []string{"jerry", "data4", "write"}); !errors.Is(err, ErrTransactionFinished) {
		t.Errorf("AddPolicy after commit: expected ErrTransactionFinished, got %v", err)
	}
	if err = txa.RemovePolicy("p", "p", []string{"tom", "data3", "read"}); !errors.Is(err, ErrTransactionFinished) {
		t.Errorf("RemovePolicy after commit: expected ErrTransactionFinished, got %v", err)
	}
	batchA, ok := txa.(persist.BatchAdapter)
	if !ok {
		t.Fatal("transaction adapter should implement BatchAdapter")
	}
	if err = batchA.AddPolicies("p", "p", [][]string{{"a", "b", "c"}}); !errors.Is(err, ErrTransactionFinished) {
		t.Errorf("AddPolicies after commit: expected ErrTransactionFinished, got %v", err)
	}
	updatableA, ok := txa.(persist.UpdatableAdapter)
	if !ok {
		t.Fatal("transaction adapter should implement UpdatableAdapter")
	}
	if _, err = updatableA.UpdateFilteredPolicies("p", "p", nil, 0, "x"); !errors.Is(err, ErrTransactionFinished) {
		t.Errorf("UpdateFilteredPolicies after commit: expected ErrTransactionFinished, got %v", err)
	}
	m, err := model.NewModelFromString(rbacModelText)
	if err != nil {
		t.Fatalf("failed to create model: %v", err)
	}
	if err = txa.LoadPolicy(m); !errors.Is(err, ErrTransactionFinished) {
		t.Errorf("LoadPolicy after commit: expected ErrTransactionFinished, got %v", err)
	}

	// 数据未被事务后操作污染：仍只有已提交的 tom 规则
	e := newTestEnforcer(t, a)
	expected := append(copyPolicies(testPolicies), []string{"tom", "data3", "read"})
	testGetPolicy(t, e, expected)
}

// TestRemovePoliciesMixedLength 验证批量删除对不同字段长度规则的精确匹配：
// 删除 2 字段规则不会误伤同前缀的 3 字段规则（OR 元组条件语义回归）
func TestRemovePoliciesMixedLength(t *testing.T) {
	a := newTestAdapter(t)
	initPolicy(t, a)

	// 写入一条 2 字段规则与一条 3 字段规则（前缀相同）
	if err := a.AddPolicies("p", "p", [][]string{
		{"alice", "data1"},
		{"alice", "data1", "read"},
	}); err != nil {
		t.Fatalf("failed to add policies: %v", err)
	}

	// 只删 2 字段规则
	if err := a.RemovePolicies("p", "p", [][]string{{"alice", "data1"}}); err != nil {
		t.Fatalf("failed to remove policies: %v", err)
	}

	// 3 字段规则应保留
	e := newTestEnforcer(t, a)
	expected := append(copyPolicies(testPolicies), []string{"alice", "data1", "read"})
	testGetPolicy(t, e, expected)

	// 再批量删除多条混合规则（含不存在的规则），存在的应全部删除
	if err := a.RemovePolicies("p", "p", [][]string{
		{"alice", "data1", "read"},
		{"bob", "data2", "write"},
		{"nobody", "data9", "none"},
	}); err != nil {
		t.Fatalf("failed to remove mixed policies: %v", err)
	}
	e = newTestEnforcer(t, a)
	testGetPolicy(t, e, [][]string{
		{"data2_admin", "data2", "read"},
		{"data2_admin", "data2", "write"},
	})
}

// TestFilteredAdapterNoAutoLoad 验证过滤适配器传入 NewEnforcer 时
// 不会自动加载全量策略（多租户防越权的关键行为）
func TestFilteredAdapterNoAutoLoad(t *testing.T) {
	db := newTestDB(t)
	a, err := NewFilteredAdapterByDB(db)
	if err != nil {
		t.Fatalf("failed to create filtered adapter: %v", err)
	}
	if _, err = db.Exec(context.Background(), "TRUNCATE TABLE "+defaultTableName); err != nil {
		t.Fatalf("failed to truncate table: %v", err)
	}

	// 先写入两个"租户"的策略
	e := newTestEnforcer(t, a)
	for _, rule := range testPolicies {
		if _, err = e.AddPolicy(rule); err != nil {
			t.Fatalf("failed to add policy: %v", err)
		}
	}

	// 用新的过滤适配器创建 enforcer：不应自动加载任何策略
	a2, err := NewFilteredAdapterByDB(db)
	if err != nil {
		t.Fatalf("failed to create filtered adapter: %v", err)
	}
	e2 := newTestEnforcer(t, a2)
	testGetPolicy(t, e2, nil)

	// 过滤加载后仅包含匹配子集
	if err = e2.LoadFilteredPolicy(&Filter{P: []string{"bob", "", ""}}); err != nil {
		t.Fatalf("failed to load filtered policy: %v", err)
	}
	testGetPolicy(t, e2, [][]string{{"bob", "data2", "write"}})
	if !e2.IsFiltered() {
		t.Error("enforcer should be filtered")
	}
}

// TestAddPoliciesMixedLength 验证字段长度不一的批量插入：
// GoFrame 会拆分为多条 INSERT，事务保证整体原子性
func TestAddPoliciesMixedLength(t *testing.T) {
	a := newTestAdapter(t)
	initPolicy(t, a)

	// p 规则 3 字段与 2 字段混批，触发 GoFrame 按字段分组拆分插入
	rules := [][]string{
		{"tom", "data3", "read"},
		{"jerry"},
	}
	if err := a.AddPolicies("p", "p", rules); err != nil {
		t.Fatalf("failed to add mixed policies: %v", err)
	}

	e := newTestEnforcer(t, a)
	expected := append(copyPolicies(testPolicies), rules...)
	testGetPolicy(t, e, expected)
}
