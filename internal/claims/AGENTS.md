# claims：付款认领与到期未清项

`erp/payment` 付款前对一个未清项的认领（协议 05 的认领模式，付款用法）：额度是未清项的可用余额，认领中的数量记在 `open_item.pay_claimed`；系统面 `ClaimService`（只对 `erp/payment` 开放）。**认领的生命周期**只在这里。

## 负责与不负责

- 负责：表 `pay_claim`；`ClaimService` 的 `Claim`、`Renew`、`Confirm`、`Release`、`Get`、`DueItems`；后台工作 `expire-claim`（`JobExpireClaim`、`ExpireClaim`）；认领时与释放、到期时改 `open_item.pay_claimed`。
- 不负责：什么时候正式化、冲回（核销决定，`settlement` 在它的事务里调 `Formalize`、`ReverseFormalized`）；未清项本身（`openitems`）。`openitems` 的读取 SQL 读 `pay_claim` 来列出未清项上的认领。

## 生命周期（改认领规则先读这里）

`lifecycle.go` 是一张表（`core.Table`）：6 个状态（`none` 是还不认识的认领身份，`pending`、`confirmed`、`formalized`、`expired`、`released`）× 7 个命令（认领、续期、确认、到期、释放、正式化、冲回），附加条件 `GuardAvailable`、`GuardSameAmount`、`GuardNotExpired`、`GuardPastExpiry`、`GuardClaimant`、`GuardWithinFormalized`，规则 CL01–CL09。

- 用法：在事务里锁住认领（`Get` 带 `FOR UPDATE`；不认识的是 `none`），`Lifecycle.Decide`，按格行事。状态的写入：新认领的插入在 `Claim`；到期、确认、释放经 `move`（离开 `pending`、`confirmed` 去 `expired`、`released` 时同一事务把数量退回 `pay_claimed`）；正式化经 `Formalize`（整个认领退出 `pay_claimed`，已核销由核销加上），冲回经 `ReverseFormalized`（只累加 `reversed_amount`）；续期只改期限。
- 拒绝都答 `CLAIM_STATE`、`CLAIM_CONFLICT` 或 `CLAIM_EXCEEDS_AVAILABLE`（gRPC 的 `FailedPrecondition`），收付款据此不付；`Get` 问不认识的身份答 `NOT_FOUND`。
- 到期（CL05）：认领与续期时各用 `EnqueueAt` 排一个到期时刻的工作项；到时锁住认领，期限确实已过、还是 `pending` 才到期，续期过或已确认的就什么也不做。期限是 `core.Base.ClaimPeriod`（配置键 `CLAIM_PERIOD`，没配 7 天）。
- 每次变化都审计（操作 `erp.finance.system.claim`，操作者是调用的组件；到期是本组件），审计的内容是系统面答的认领加上版本（`change`；整套恢复按版本问它在不在）。

## 文件

| 文件 | 内容 |
| --- | --- |
| `lifecycle.go` | 状态、命令、附加条件；表 `Lifecycle` |
| `lifecycle_test.go` | 单元测试：每一格都定了；设计文档的表与代码逐格相同；每条规则都有集成测试写明 |
| `claims.go` | `Record`、`Get`；`adjust`（改 `pay_claimed`）、`move`、`change`（审计）、`expireAt`；`Claim`、`command`（续期、确认、释放）与它们的系统面函数；`GetClaim`；`ExpireClaim`；`Formalize`、`ReverseFormalized`（核销用） |
| `due.go` | `DueItems`：法人的应付与客户贷方余额，到期日不晚于给定日期、可用余额大于零，按（到期日，身份）排序，游标是上一页最后一个的这两个值 |

## 依赖与被依赖

- 依赖：`core`、`masterdata`（币种小数位）、`openitems`（读与锁未清项）。
- 被依赖：入口（`finance/services.go` 的 `ClaimService`、`Jobs`）；`settlement`（正式化与冲回）。

## 规则在哪

`docs/design.md` 的“付款认领”（表、CL01–CL09、到期未清项）；装配项目协议 05 的“认领模式”与“付款认领”。

## 测试

- 单元：`lifecycle_test.go`。
- 集成：`itest/claims_integration_test.go`（`claims`：CL01–CL06、CL08 与到期未清项）、`itest/settlement_integration_test.go`（`settlement`：CL07、CL09）。
