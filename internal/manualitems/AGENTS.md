# manualitems：人工未清项

缺来源时由人登记的应收与应付（例如补登的往来）：登记后等批准，另一个人批准时过凭证、变成普通的未清项（之后能认领、能核销）。**人工未清项的批准**只在这里；未清项本身在 `openitems`。

## 负责与不负责

- 负责：路由 `POST /api/finance/open-items`（登记）与 `approve`、`cancel` 两个动作（`Action`）；人工未清项的凭证（规则 `manual_receivable`、`manual_payable`）。
- 不负责：未清项的表、读取与“应收已形成”（`openitems.Form`、`openitems.Formed`）；认领与核销（`claims`、`settlement`，只认 `open` 的）。

## 生命周期（改人工未清项规则先读这里）

`lifecycle.go` 是一张表（`core.Table`）：存储的 3 个状态（`pending_approval`、`open`、`cancelled`；读出的 `settled` 由未清余额算，不在表里）× 2 个命令（批准、取消），附加条件 `GuardNotMaker`，规则 MI01–MI03。

- 用法：锁住未清项（`openitems.Get`），只处理 `origin = manual` 的，`Lifecycle.Decide`，按格行事；状态的写入是 `Action` 里那一条更新。批准在同一事务里过凭证（`post`）、改 `open`、发“应收已形成”（应收时）、审计。
- 加一个状态或命令：表与 `docs/design.md` 的同一张表一起改（`lifecycle_test.go` 逐格比较），集成测试写上规则编号。

## 文件

| 文件 | 内容 |
| --- | --- |
| `lifecycle.go` | 状态、命令、附加条件；表 `Lifecycle` |
| `lifecycle_test.go` | 单元测试：每一格都定了；设计文档的表与代码逐格相同；每条规则都有集成测试写明 |
| `manual.go` | `CreateOpenItem`（事务外取证：法人、币种小数位、伙伴、部门；事务里查对方科目可记账、`openitems.Form` 成 `pending_approval`，登记人、说明、对方科目记在未清项上）、`Action`、`post`（应收或应付经映射，对方科目照登记的，今天、照 PD01 顺延） |

## 依赖与被依赖

- 依赖：`core`、`ledger`、`masterdata`、`openitems`、`orgunits`、`periods`。
- 被依赖：入口登记路由。

## 规则在哪

`docs/design.md` 的“应收、应付与核销”里人工未清项的表与 MI01–MI03；协议 05（人工应收的“应收已形成”给信用）。

## 测试

- 单元：`lifecycle_test.go`。
- 集成：`itest/manualitems_integration_test.go`（`manualItems`：MI01–MI03）。
