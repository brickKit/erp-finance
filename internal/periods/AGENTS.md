# periods：会计期间与它的生命周期

一个法人按月的会计期间：第一次过账到某月时建立（`open`），手工关闭、带原因重开、再关闭；期间的事件 `erp.accounting_period.changed.v1`。首批没有关账批次（参与方还没有封存），关闭是本组件的本地动作。

## 负责与不负责

- 负责：表 `period`；期间的生命周期表与它唯一的写入（`Action` 里的条件更新）；路由 `/api/finance/periods`（列表）与 `/api/finance/periods/:legal_entity_id/:period/close`、`/reopen`；手工凭证能不能过账到某月（`CheckManual`）、来源的事实落在哪天过账（`PostingDate`，P3c 起用）。
- 不负责：凭证本身（`ledger`、`vouchers`）；关账批次与封存（不在首批）。

## 生命周期（改期间规则先读这里）

`lifecycle.go` 是一张表（`core.Table`）：3 个状态（`open`、`closed`、`reopened`）× 3 个命令（关闭、重开、手工过账），附加条件 `GuardNothingWaiting`（没有待审批的手工凭证、之前的月份都已关闭或重开）、`GuardReason`（重开写明原因），规则 PD01、PD02。

- 加一个状态或命令：表与 `docs/design.md` 的同一张表一起改（`lifecycle_test.go` 逐格比较），集成测试的注释写上规则编号。

## 文件

| 文件 | 内容 |
| --- | --- |
| `lifecycle.go` | 状态、命令、附加条件；表 `Lifecycle` |
| `lifecycle_test.go` | 单元测试：每一格都定了；设计文档的表与代码逐格相同；每条规则都有集成测试写明 |
| `periods.go` | `Period`；`Of`（取或建某月，建时发事件）、`Month`、`PostingDate`（PD01：落在关闭或重开的月份时顺延到下一个开放月份的首日）、`CheckManual`（关闭的月份答 `PERIOD_CLOSED`）；`ListPeriods`；`Action`（关闭与重开：加锁、按格行事、修订号加 1、发事件、审计）；`nothingWaiting` |
| `events.go` | `eventOf`：期间事件的形状 |

## 依赖与被依赖

- 依赖：`core`。
- 被依赖：`vouchers`（`CheckManual`）；以后接收事实的模块（`PostingDate`）；入口登记路由。

## 规则在哪

`docs/design.md` 的“手工凭证与期间”（PD01、PD02）；迟到单据：装配项目协议 06。

## 测试

- 单元：`lifecycle_test.go`。
- 集成：`itest/vouchers_integration_test.go`（`periods`：PD01、PD02）。
