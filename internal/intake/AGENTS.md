# intake：收到的事实与它们的过账

别的组件发来的增量事实（`erp/billing` 的已出账；`FINANCE_FACT_SOURCES` 里的资金事实，以后会计事实）在这里收件、去重、过账。两段：收到事件的事务里只写收件表与一个过账工作项；工作项先在事务外取证，再在一个事务里过账并产生业务效果（AR03）。

## 负责与不负责

- 负责：表 `fact_received`（去重记录：事实身份、类型、来源、内容摘要、内容、是否已过账、凭证）；订阅的处理函数 `OnFact`；后台工作 `post-fact`（`JobPostFact`、`PostFact`）：取证、事务、按类型分派（`posterOf`）；已出账的过账（`postInvoice`）。资金事实的过账与核销在 `settlement.PostFund`。
- 不负责：凭证的写入与取号（`ledger`）、过账日期（`periods`）、未清项（`openitems`）、取证（`masterdata`）。

## 怎样走

1. `OnFact`（消费者的事务）：按 `fact_id` 插入收件表；新的排一个 `post-fact`；同一身份已在时确认，摘要不同的告警（`fact_digest_mismatch`，AR02）。摘要是载荷的规范 JSON（键排序、数字照原样）的 SHA-256。
2. `PostFact`（工作项）：读收件，已过账的结束；事务外取证（`evidence`：法人的代码与本位币、本位币小数位，收成 `core.Received`）；`r.Complete` 的事务里先锁收件（`claim`，别的运行已过账的就结束），按类型调过账函数（`posterOf`：已出账 → `postInvoice`，`*.fund_fact.v1` → `settlement.PostFund`），它答凭证，`posted` 记下。映射缺失（`MAPPING_MISSING`）与 `core.ErrWaitForOriginal`（冲正先到）时 `work.Wait`：不耗尝试次数，等着。
3. `postInvoice`：`ledger.Resolve` → `ledger.Convert`（AR04）→ `periods.PostingDate`（迟到顺延）→ `ledger.Write` 与 `Issue`（审计的操作者是来源组件，操作 `core.OpPostFact`）→ `openitems.Form`。

去重有四层，任何一层都够，一起拆掉时测试会红（`c4-data/mut-p3c`）：收件表的主键、工作项开头的“已过账”、事务里锁住的状态、凭证与未清项按来源的唯一索引。

## 文件

| 文件 | 内容 |
| --- | --- |
| `receive.go` | `JobPostFact`；`OnFact`、`digestOf`；`poster`、`posterOf`、`PostFact`、`evidence`；`claim`、`posted` |
| `invoice.go` | `invoice`（已出账里过账要读的字段）；`postInvoice` |

## 依赖与被依赖

- 依赖：`core`、`ledger`、`masterdata`、`openitems`、`periods`、`settlement`。
- 被依赖：入口登记订阅与后台工作。

## 加一种事实

在 `posterOf` 里加一个类型与它的过账函数（签名 `poster`：在事务里过账、答凭证）；固定来源的在入口的 `Subscriptions` 加订阅、`component.yaml` 的 `events.subscribes` 加类型，按共享契约来的由部署的 `FINANCE_FACT_SOURCES` 订阅；规则写进 `docs/design.md`，集成测试经替身发布它。

## 规则在哪

`docs/design.md` 的“会计事实契约”（重复投递）与“应收、应付与核销”（AR01–AR04）；协议 05 的共同规则与第 2 步；协议 01（工作项）；协议 06（迟到顺延，PD01）。

## 测试

集成：`itest/receivables_integration_test.go`（`receivables`：AR01–AR04、PD01 的顺延）、`itest/settlement_integration_test.go`（`settlement`：资金事实）。
