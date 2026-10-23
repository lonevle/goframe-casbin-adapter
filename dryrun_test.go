package gadapter

import (
	"context"
	"strings"
	"testing"

	"github.com/gogf/gf/v2/database/gdb"
)

// TestDryRunProbe 验证 GoFrame 对无条件 DELETE 的拦截行为（DryRun 模式，
// 只构建 SQL 不执行，因此无需真实数据库连接）：
// 1. 不带 WHERE 的 Delete 会被 GoFrame 安全机制拒绝——即旧版 savePolicy 的致命 bug 根因
// 2. 追加恒真条件 Where("1=1") 后 Delete 可正常通过——即本次修复
func TestDryRunProbe(t *testing.T) {
	db := newFakeDB(t)
	db.SetDryRun(true)
	ctx := context.Background()

	// 无条件删除：GoFrame 出于安全考虑必须拒绝
	_, err := db.Ctx(ctx).Model(defaultTableName).Delete()
	if err == nil {
		t.Fatal("unconditioned DELETE should be rejected by GoFrame safety check")
	}
	t.Logf("unconditioned DELETE rejected as expected: %v", err)

	// 恒真条件删除：应通过校验并正常返回（dry-run 不实际执行）
	m := db.Ctx(ctx).Model(defaultTableName).Where("1=1")
	if _, err = m.Delete(); err != nil {
		t.Fatalf("DELETE with WHERE 1=1 should pass, got: %v", err)
	}
}

// TestDryRunRemovePoliciesSQL 验证批量删除的 OR 元组条件 SQL 构建正确性。
// 使用 gdb.ToSQL：官方提供的"构建 SQL 而不执行"机制（比 DryRun 更强，
// DryRun 只拦截写操作，SELECT 仍会尝试真实连接）。
// 断言生成的 WHERE 形如 (r1 元组) OR (r2 元组)，每个规则元组独立括号
func TestDryRunRemovePoliciesSQL(t *testing.T) {
	db := newFakeDB(t)
	names := DefaultFieldName()

	rules := [][]string{
		{"alice", "data1", "read"},
		{"bob", "data2", "write"},
	}
	sqlStr, err := gdb.ToSQL(context.Background(), func(ctx context.Context) error {
		m := db.Ctx(ctx).Model(defaultTableName).
			Where(ruleCondition(names), ruleArgs(names, "p", rules[0])...)
		for _, rule := range rules[1:] {
			m = m.WhereOr(ruleCondition(names), ruleArgs(names, "p", rule)...)
		}
		_, err := m.All()
		return err
	})
	if err != nil {
		t.Fatalf("ToSQL failed: %v", err)
	}
	t.Logf("generated SQL: %s", sqlStr)

	// 每条规则的元组条件必须独立括号包裹，否则 AND/OR 优先级会破坏匹配语义
	wantFrag := "(ptype='p' AND v0='alice' AND v1='data1' AND v2='read' AND v3='' AND v4='' AND v5='')"
	if !strings.Contains(sqlStr, wantFrag) {
		t.Errorf("first rule condition fragment missing in %q", sqlStr)
	}
	if !strings.Contains(sqlStr, ") OR ((") {
		t.Errorf("OR tuple structure missing in %q", sqlStr)
	}
	// 两条规则共 14 个占位参数，应全部内联
	if strings.Count(sqlStr, "ptype='p'") != 2 {
		t.Errorf("expected 2 rule tuples in %q", sqlStr)
	}
}
