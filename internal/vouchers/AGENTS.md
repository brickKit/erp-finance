# vouchers：手工凭证与它的生命周期

会计手工录的凭证：草稿（本位币）→ 提交 → 另一个人批准（过账、取号）或驳回；过账后不改，更正用冲销凭证。**手工凭证的生命周期**只在这里；系统凭证由事实直接过出（`ledger.Issue`），不经这张表。

## 负责与不负责

- 负责：路由 `/api/finance/vouchers`（列表、新建、读取、修改）与 `submit`、`approve`、`reject`、`reverse` 四个动作（`Action`）；手工凭证状态的写入（`Transition`；过账由 `ledger.Issue` 写）。
- 不负责：凭证的表、平衡与取号（`ledger`）；期间开不开放（`periods.CheckManual`）；取法人与币种小数位（`masterdata`）。

## 生命周期（改手工凭证规则先读这里）

`lifecycle.go` 是一张表（`core.Table`）：4 个状态（`draft`、`submitted`、`posted`、`reversed`）× 5 个命令（修改、提交、批准、驳回、冲销），附加条件 `GuardNotMaker`（批准人不是制单人，`APPROVER_IS_SUBMITTER`），规则 VC01–VC04。

- 用法：命令先读凭证（加锁），`Lifecycle.Decide(状态, 命令)`，按格行事；批准先问期间（`periods.CheckManual`）再 `ledger.Issue`；冲销在今天所在的月份写一张借贷相反、已过账的凭证（`ledger.Write` + `Issue`），原凭证经 `Transition` 变成 `reversed` 并记下冲销它的凭证。
- 加一个状态或命令：表与 `docs/design.md` 的同一张表一起改（`lifecycle_test.go` 逐格比较），集成测试的注释写上规则编号。

## 文件

| 文件 | 内容 |
| --- | --- |
| `lifecycle.go` | 状态、命令、附加条件；表 `Lifecycle`；`Transition` |
| `lifecycle_test.go` | 单元测试：每一格都定了；设计文档的表与代码逐格相同；每条规则都有集成测试写明 |
| `vouchers.go` | 输入（`voucherIn`、`lineIn`）与事务外的取证（`prepare`：法人、本位币小数位，金额本位币等于交易币）；`CreateVoucher`（幂等，制单人是调用者）、`GetVoucher`、`UpdateVoucher`（按版本整份换行）、`Action`；`ListVouchers`（按法人、期间、状态筛选，按凭证号搜索，新的在前） |

## 依赖与被依赖

- 依赖：`core`、`ledger`、`masterdata`、`periods`。
- 被依赖：入口登记路由。

## 规则在哪

`docs/design.md` 的“手工凭证与期间”（VC01–VC04、PD01）；审批人不是制单人：装配项目协议 09（按自然人等 C7）。

## 测试

- 单元：`lifecycle_test.go`。
- 集成：`itest/vouchers_integration_test.go`（`vouchers`：VC01–VC04；`periods`：手工凭证落在关闭的月份）。
