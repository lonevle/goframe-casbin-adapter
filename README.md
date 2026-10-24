# goframe-casbin-adapter

基于 [GoFrame ORM (gdb)](https://goframe.org) 的 [casbin v3](https://github.com/casbin/casbin) 策略适配器，支持 MySQL、SQL Server 等数据库。

## 特性

- 实现 casbin v3 全部持久化接口：基础读写、Context 版本、批量增删、策略更新、过滤加载、事务
- 事务适配器与普通适配器复用同一套实现，事务内任何操作均不会逃逸到事务外；
  事务提交或回滚后，适配器的任何操作返回明确的 `ErrTransactionFinished`
- 支持自定义表名与字段名（含空值的字段名配置在构造期被拒绝）
- 批量写入自动分批（每批 100 行），全量保存 / 批量增删 / 批量更新均在事务中完成；
  批量删除先按精确条件查主键再按主键集合删除，每批 100 条规则分块执行
  （规避 SQL Server 单语句 2100 参数上限）
- 精确匹配删除与更新：策略规则不足 6 个字段时，尾部按空串匹配，不会误伤同前缀的其他规则
- 写读对称保障：规则超过 6 个字段（`ErrTooManyRuleFields`）、规则为空（`ErrEmptyRule`）、
  尾部字段为空串（`ErrTrailingEmptyField`）、长度与模型定义不符（`ErrPolicySizeMismatch`，
  仅 SavePolicy 路径）时写入端直接拒绝，而非落库后让加载整体失败
- 更新策略时旧规则不在库中返回 `ErrPolicyNotFound`（内存与数据库失联的显式信号）
- `fieldIndex=-1` 兼容官方语义：忽略过滤值，清空该策略类型下的全部规则
- 事务上下文带状态机：重复提交或回滚返回明确错误 `ErrTransactionFinished`；
  另提供 `Transaction` 便捷方法自动提交 / 回滚

## 安装

```bash
go get github.com/lonevle/goframe-casbin-adapter
```

## 建表

适配器不自动建表，请先在数据库中执行对应方言的建表 SQL。

### MySQL

```sql
CREATE TABLE IF NOT EXISTS `casbin_rule` (
    `id`     BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    `ptype`  VARCHAR(32)  NOT NULL DEFAULT '',
    `v0`     VARCHAR(100) NOT NULL DEFAULT '',
    `v1`     VARCHAR(100) NOT NULL DEFAULT '',
    `v2`     VARCHAR(100) NOT NULL DEFAULT '',
    `v3`     VARCHAR(100) NOT NULL DEFAULT '',
    `v4`     VARCHAR(100) NOT NULL DEFAULT '',
    `v5`     VARCHAR(100) NOT NULL DEFAULT '',
    PRIMARY KEY (`id`),
    UNIQUE KEY `uk_casbin_rule` (`ptype`, `v0`, `v1`, `v2`, `v3`, `v4`, `v5`)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4;
```

### SQL Server

> 注：SQL Server 非聚集唯一索引键上限为 900 字节，因此规则字段使用 `NVARCHAR(64)`（6 列共 768 字节，加 `ptype` 后不超过上限）。

```sql
IF NOT EXISTS (SELECT * FROM sys.objects WHERE object_id = OBJECT_ID(N'[dbo].[casbin_rule]') AND type = N'U')
BEGIN
    CREATE TABLE [dbo].[casbin_rule] (
        [id]     BIGINT IDENTITY(1,1) NOT NULL,
        [ptype]  NVARCHAR(32) NOT NULL DEFAULT '',
        [v0]     NVARCHAR(64) NOT NULL DEFAULT '',
        [v1]     NVARCHAR(64) NOT NULL DEFAULT '',
        [v2]     NVARCHAR(64) NOT NULL DEFAULT '',
        [v3]     NVARCHAR(64) NOT NULL DEFAULT '',
        [v4]     NVARCHAR(64) NOT NULL DEFAULT '',
        [v5]     NVARCHAR(64) NOT NULL DEFAULT '',
        CONSTRAINT [PK_casbin_rule] PRIMARY KEY ([id]),
        CONSTRAINT [UK_casbin_rule] UNIQUE ([ptype], [v0], [v1], [v2], [v3], [v4], [v5])
    );
END
```

> **不要为策略表启用软删除**：适配器的清空重写（`SavePolicy`）与删除依赖物理删除语义。
> 若表含 `deleted_at` 等软删除字段，GoFrame 会把 `DELETE` 转为软删 `UPDATE`，
> 软删行仍占据 `(ptype, v0..v5)` 唯一索引，`SavePolicy` 重写相同规则时会报 duplicate key 错误。

## 快速开始

```go
package main

import (
    "github.com/casbin/casbin/v3"
    gadapter "github.com/lonevle/goframe-casbin-adapter"
    "github.com/gogf/gf/v2/frame/g"
)

func main() {
    // 方式一：复用 GoFrame 默认数据库分组
    a, err := gadapter.NewAdapterByDB(g.DB())
    if err != nil {
        panic(err)
    }

    // 方式二：基于指定配置分组创建（分组来自 config.yaml 的 database 节点）
    // a, err := gadapter.NewAdapterByGroup("casbin")

    e, err := casbin.NewEnforcer("rbac_model.conf", a)
    if err != nil {
        panic(err)
    }

    // 后续策略增删改会自动同步到数据库（AutoSave 默认开启）
    e.AddPolicy("alice", "data1", "read")
    e.Enforce("alice", "data1", "read") // true
}
```

## 配置选项

```go
a, err := gadapter.NewAdapterByDB(db,
    // 自定义策略表名，默认 casbin_rule
    gadapter.WithTableName("my_rule"),
    // 自定义字段名，默认 ptype / v0-v5
    gadapter.WithFieldNames(&gadapter.FieldName{
        ID:    "id",
        PType: "p_type",
        V0:    "v_0",
        V1:    "v_1",
        V2:    "v_2",
        V3:    "v_3",
        V4:    "v_4",
        V5:    "v_5",
    }),
)
```

## 过滤加载

仅加载符合条件的服务子集，适合多租户等场景。

**多租户场景务必使用 `NewFilteredAdapterByDB` 构造**：casbin 创建 Enforcer 时会自动调用 `LoadPolicy` 全量加载，
`NewFilteredAdapterByDB` 初始即标记"已过滤"，casbin 检测到该标记后跳过自动加载，
否则所有租户的策略会在过滤条件生效前进内存：

```go
// 正确：过滤适配器 + 手动过滤加载
a, err := gadapter.NewFilteredAdapterByDB(g.DB())
e, err := casbin.NewEnforcer("rbac_model.conf", a) // 不会自动加载任何策略

// 仅加载 v0=bob 的 p 策略与 v0=alice 的 g 策略，空字符串表示通配
err = e.LoadFilteredPolicy(&gadapter.Filter{
    P: []string{"bob", "", ""},
    G: []string{"alice", ""},
})
```

全量加载（`LoadPolicy`）成功后过滤标记自动清除。注意：过滤加载成功后调用 `e.SavePolicy()` 会被
casbin 拒绝（"cannot save a filtered policy"），这是预期行为，防止子集保存覆盖全量数据。

**分区过滤陷阱**：`Filter.P` / `Filter.G` 为 nil 或空切片时表示**该分区不过滤（全量加载）**，
与官方 gorm 适配器行为一致。多租户场景若只传 `G` 而留空 `P`，会加载全部租户的 p 策略——
需要限定某个分区时必须显式传入该分区的过滤值（如租户标识）：

```go
// 错误：P 为 nil，全部租户的 p 策略都会进入内存
e.LoadFilteredPolicy(&gadapter.Filter{G: []string{"tenant-1"}})

// 正确：两个分区都按租户过滤
e.LoadFilteredPolicy(&gadapter.Filter{
    P: []string{"tenant-1"},
    G: []string{"tenant-1"},
})
```

`LoadFilteredPolicy(nil)` 返回错误而非全量加载（与官方 gorm 一致）：nil 全量加载
会与"已过滤"标记的语义矛盾。传入 `*Filter` 类型的空指针同样返回 `ErrInvalidFilter`。

## 规则字段校验

策略表固定 6 个值字段（`v0`-`v5`），因此模型中每个策略定义最多支持 6 个值字段
（如 `p = sub, obj, act` 为 3 个）。这也是 casbin 生态表结构的普遍上限
（官方 gorm / xorm 适配器的 `CasbinRule` 结构体同样固定 6 个值字段）。

为保证"写入成功则一定可以加载回来"（写读对称），以下规则会在写入端被明确拒绝，
而非落库后让下一次 `LoadPolicy` 整体失败：

| 拒绝场景 | 错误 | 原因 |
| --- | --- | --- |
| 超过 6 个值字段 | `ErrTooManyRuleFields` | 超出表容量，静默截断会丢数据 |
| 规则为空（0 个字段） | `ErrEmptyRule` | 落库后仅含 ptype，加载必然失败 |
| 尾部字段为空串 | `ErrTrailingEmptyField` | 读取端必须裁剪尾部空串（`v0`-`v5` 数据库缺省值为空串），尾部空串的规则无法还原 |
| 长度与模型定义不符（仅 `SavePolicy`） | `ErrPolicySizeMismatch` | p 分区要求与 token 数完全相等，g 分区要求不小于（与 casbin 加载端校验一致） |

中间字段的空串不受影响（如 `["alice", "", "admin"]` 可正常存取）。
经 enforcer 的增删改由 casbin 核心按模型校验长度，通常不会触发上表错误；
直接使用适配器写入时需自行保证规则长度与模型定义一致。

## 事务

方式一：`Transaction` 便捷方法，`fn` 返回 `nil` 自动提交、返回错误自动回滚，
`fn` panic 时同样自动回滚（panic 原样向上传播），无需手动处理 Commit / Rollback：

```go
import "github.com/casbin/casbin/v3/persist"

err := a.Transaction(ctx, func(tx persist.Adapter) error {
    if err := tx.AddPolicy("p", "p", []string{"alice", "data1", "write"}); err != nil {
        return err // 返回错误，自动回滚
    }
    // ... 其他策略操作，均在该事务内执行
    return nil // 返回 nil，自动提交
})
```

方式二：手动控制提交时机（需确保每个错误路径都调用 `Rollback`）：

```go
ctx := context.Background()

// 开启事务，事务内的全部操作要么全部生效，要么全部回滚
tc, err := a.BeginTransaction(ctx)
if err != nil {
    panic(err)
}

// GetAdapter 返回事务适配器，断言后可使用批量、更新等全部能力
if err := tc.GetAdapter().AddPolicy("p", "p", []string{"alice", "data1", "write"}); err != nil {
    tc.Rollback() // 回滚
    panic(err)
}

if err := tc.Commit(); err != nil { // 提交
    panic(err)
}

// 事务只能结束一次：Commit / Rollback 之后再次调用返回 ErrTransactionFinished
```

## 运行测试

纯逻辑测试无需数据库，直接运行：

```bash
go test -v -run "TestNewFilteredAdapter|TestNewAdapterByDBNil|TestFieldNameDefaults|TestOptionsDefaults|TestRuleToData|TestValidateRule|TestAddPolicyTooManyFields|TestWriteRejectsNonRoundTrippableRules|TestLoadFilteredPolicyNilFilter|TestTxAdapterFinishedFlag|TestRuleConditionAndArgs|TestRuleToFullData|TestRecordToRule|TestModelToData|TestDryRunProbe|TestDryRunRemovePoliciesSQL" .
```

集成测试依赖真实数据库，通过环境变量指定连接后运行（表需预先创建）：

```bash
# MySQL
export GADAPTER_TEST_TYPE=mysql
export GADAPTER_TEST_LINK="mysql:root:123456@tcp(127.0.0.1:3306)/casbin"
go test -v .

# SQL Server
export GADAPTER_TEST_TYPE=mssql
export GADAPTER_TEST_LINK="mssql://sa:123456@127.0.0.1:1433/casbin"
go test -v .
```

## License

Apache-2.0
