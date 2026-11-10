# settlement：资金事实的过账与核销

收付款发来的资金事实（到账、付款、退款、冲正）在收件的过账事务里（`intake` 调 `PostFund`）过账并核销：分配记录、未清项的已核销、认领的正式化与冲回、给信用的应收事实（协议 05 的核销与付款认领；设计 ST02–ST05、CL07、CL09）。

## 负责与不负责

- 负责：表 `allocation`；`open_item.settled` 的唯一写入（`allocate`）；事件 `erp.open_item.settled.v1`、`erp.receivable.cleared.v1`、`erp.receivable.reduced.v1`、`erp.receivable.reopened.v1`（增量事实）；到账剩下的形成客户贷方余额（经 `openitems.Form`）；冲正时作废它。
- 不负责：收件、去重、取证与事务（`intake`）；认领的状态（`claims.Formalize`、`claims.ReverseFormalized`）；凭证与取号（`ledger`）。冲正读 `intake` 的表 `fact_received` 找原事实与它的凭证（只读）。

## 怎样走

- `PostFund`：按种类出凭证（`fundVoucher`，ST02），再分配：到账按伙伴在本法人、同币种的应收，最早到期先分（`receive`）；付款与退款按认领清单（`pay`：先锁认领、再锁未清项，只用 `confirmed`、同一伙伴、同币种的认领，其余告警跳过，事实照常过账）。
- 锁的顺序：认领按身份，未清项按身份升序（`lockItems`），在锁内读余额再分（ST03）。
- `reverse`（ST05）：原事实没过账、部分冲正、原来形成的贷方余额已被用过时答 `core.ErrWaitForOriginal`，`intake` 让工作项等着；否则照原凭证借贷相反出凭证并把原凭证标为已冲销（`mirror`、`ledger.MarkReversed`），每条原分配写一条反向分配，正式化过的认领冲回。

## 文件

| 文件 | 内容 |
| --- | --- |
| `fund.go` | `fundFact`（共享契约里要读的字段）；`PostFund`、`fundVoucher`、`receive`、`pay` |
| `allocation.go` | `allocation`、`allocate`（分配记录、已核销、事实）、`lockItems` |
| `reversal.go` | `reverse`、`mirror`、`scan` |

## 依赖与被依赖

- 依赖：`core`、`claims`、`ledger`、`openitems`、`periods`。
- 被依赖：`intake`（`PostFund`）。

## 规则在哪

`docs/design.md` 的“应收、应付与核销”（ST01–ST05）与“付款认领”（CL07、CL09）；装配项目协议 05 的“核销：财务本地事务内的分配”“付款认领”“应收形成与信用占用的转换”。

## 测试

集成：`itest/settlement_integration_test.go`（`settlement`：ST02–ST05、CL07、CL09，含“同一收款被两次核销时只成功一次”）。
