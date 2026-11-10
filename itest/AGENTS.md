# itest/：集成测试与测试索引

财务组件对着真实的 PostgreSQL 与消息系统、经它真实的接口（用户面 HTTP、系统面 gRPC、事件）的测试（L2）。构建标签 `integration`；一条业务流程一个文件。

## 怎样跑

- `make test-integration`（`go test -count=1 -tags integration ./itest/`）。
- 环境变量：`BE_SDK_TEST_PG_ADMIN`（口令是装配项目 `.env` 的 `POSTGRES_PASSWORD`）、`BE_SDK_TEST_NATS_URL=nats://localhost:4222`；be-sdk-go 的 `sectest` 是测试部署。
- 前提：装配项目里 `make up` 起了 `be-postgres` 与 `be-nats`。
- 端口：28087（用户面）、9097（系统面）、替身 19120（`mdm/org`）、19121（`mdm/partner`）、19122（`mdm/currency`）、19123（`erp/purchase`）要空着，被占时测试直接失败。
- 每次开始：删掉再建自己的 schema `erp_finance_it`（跑完留着，可以查）；删掉流 `mdm.org`、`erp.billing`、`erp.payment`、`erp.finance`（别的运行留下的事件）。
- 配置：上海的匹配容差由测试的 `MATCH_TOLERANCES` 定为数量 5 %、价格 0（北京没写，是 0）；认领期限 `CLAIM_PERIOD` 是 4 秒；`FINANCE_FACT_SOURCES` 订阅流 `erp.payment` 的资金事实与会计事实。
- **只有一个测试** `TestFinanceEndToEnd`：它按下表的顺序跑各步；改了哪个流程就跑整个。

## 文件

按运行顺序：

| 文件 | 步 | 测的流程 | 覆盖的规则 | 被测的模块 |
| --- | --- | --- | --- | --- |
| `unsupported_integration_test.go` | `notInThisVersion` | 契约里有、本版不做的路由答“能力不支持” | D75 | 入口 `finance/module.go` |
| `ledger_integration_test.go` | `chart` | 上海的科目表（末级与非末级、停用、代码重复、按版本修改）；过账规则目录；科目映射与它的版本 | VC01 | `ledger` |
| `vouchers_integration_test.go` | `vouchers` | 手工凭证：不平衡与不可记账被拒；草稿按版本修改、提交、制单人不能批准、另一人批准取号；驳回写原因；冲销；当月余额（草稿与待审批不算）；凭证已过账的事实；列表 | VC01–VC04 | `vouchers`、`ledger` |
| `vouchers_integration_test.go` | `periods` | 两个月前：有待审批的凭证时不能关；关闭后手工凭证 `PERIOD_CLOSED`；带原因重开后能批准；之间的月份没关时当月不能关；期间事件 | PD01、PD02 | `periods`、`vouchers` |
| `receivables_integration_test.go` | `receivables` | 出账的替身发布已出账：人民币发票形成应收、凭证与事实；重发与内容不同的同一事实不再入账；美元发票按汇率快照折算、舍入差落在最大行；两个月前（已关闭）的发票顺延到下个开放月的首日；北京没有映射时等着、补上映射后过账；`FactService.Read` | AR01–AR04、PD01 | `intake`、`openitems`、`ledger`、`periods`、`facts` |
| `supplierinvoices_integration_test.go` | `supplierInvoices` | 采购的替身给订单（货物 10 箱、已收 96 个；服务 5 小时、已验收 3）：登记的拒绝（暂停的供应商、外币、两种行混在一起、票据号重复）；超量超价进 `mismatch`、登记人不能批准；改回草稿、在 5 % 容差内匹配；过账前依据变了退回；过账：应付、凭证、按流水的票据单价确认；没有可开票数量的经批准过账；别的供应商的订单行；改了清掉批准；费用票据经批准过账；过账后不能改不能取消、取消后票据号可再用 | SI01–SI05 | `supplierinvoices`、`openitems`、`ledger` |
| `claims_integration_test.go` | `claims` | 作为 `erp/payment` 调系统面：认领、同一认领再来、不同数量冲突、超出可用余额、别的组件被拒、应收不能认领；续期、确认、确认后不能续期；释放与重复释放；不认识的认领；到期退回数量（期限 4 秒）、续期过的到原期限不到期；使用方释放已确认的；到期未清项的分页、可用为零的不出现 | CL01–CL06、CL08 | `claims` |
| `settlement_integration_test.go` | `settlement` | 收付款的替身发布资金事实：同一到账发两次只核销一次、最早到期先分、跨法人与外币的不分；剩余形成客户贷方余额；退款与付款按已确认的认领正式化；不认识与未确认的认领不分配；付款冲正（重复的只一次）、到账冲正（应收重开）、冲正先于原事实到达；没有应收的伙伴的到账成为贷方余额、冲正后作废；不知道付款方的到账记暂挂 | ST02–ST05、CL07、CL09 | `settlement`、`claims`、`intake` |
| `accountingfacts_integration_test.go` | `accountingFacts` | 收付款的替身发布会计事实：没映射的规则等着、映射后过账（部门与伙伴在每行）；美元按汇率快照、关闭月份的顺延；目录里没有的规则键等着；借贷不平的不过账 | AF01、AF02、PD01 | `intake`、`ledger` |
| `manualitems_integration_test.go` | `manualItems` | 登记的拒绝（非末级的对方科目、外币、暂停的伙伴）；待批准的不能认领；登记人不能批准、版本不对、没映射时被拒；另一人批准：凭证、应收已形成；应付批准后能认领；取消与重复取消、取消的不能批准、已批准的不能取消 | MI01–MI03 | `manualitems`、`openitems` |
| `recovery_integration_test.go` | `recoveryChecks` | 作为恢复工具（`ops/recover`）：别的组件被拒；审计过的每种对象都在、版本不低于最后一次审计，高一千版答“旧了”、新身份答“不在”；RC01 报出前面留下的两条没过账的事实 | RC01、be-protocol 第 14 章 | 入口 `finance/checks.go`、`intake` |

共用的：

| 文件 | 内容 |
| --- | --- |
| `endtoend_integration_test.go` | `TestFinanceEndToEnd`（各步的顺序）；`fixture`（运行中的成员、替身、世界的 ID：上海与北京两个法人、两个部门、一个客户、一个供应商、一个暂停的伙伴；Alice 与 Bob 两个会计）；`start`；辅助：`client.do`、`at`、`get`、`unit`、`publishOrgUnit`、`publishOrg`、`portFree` |
| `standins_integration_test.go` | 替身，各按已发布的契约经 mTLS 答：`mdm/org`（法人与部门）、`mdm/partner`（伙伴与它的状态）、`mdm/currency`（CNY 与 USD）、`erp/purchase` 的 `MatchService.Basis`（测试按行放入依据：`purchaseStandIn.set`） |

只给本步与后面的步用的辅助放在该步的文件里：

| 文件 | 辅助 |
| --- | --- |
| `ledger_integration_test.go` | `mapping`、`account`、`month` |
| `vouchers_integration_test.go` | `lines`、`voucherBody`、`voucher`、`voucherAction`、`voucherRefused`、`periodAction`、`balances`、`lastPeriodEvent` |
| `receivables_integration_test.go` | 出账的替身 `invoice`、`publishInvoice`（按出账的契约发布到流 `erp.billing`）；`openItemOf`、`voucherOf`、`chartOf`、`readFacts`、`plus`、`cell` |
| `supplierinvoices_integration_test.go` | `siBody`、`si`、`siUpdate`、`siAction`、`siRefused`、`noFact` |
| `claims_integration_test.go` | `claimClient`（作为某个组件调 `ClaimService`）、`claim`、`claimRefused`、`claimReason` |
| `accountingfacts_integration_test.go` | 收付款的替身 `accounting`（按共享契约的会计事实） |
| `manualitems_integration_test.go` | `manual`、`manualAction` |
| `settlement_integration_test.go` | 收付款的替身 `fund`（按共享契约的资金事实，发布用 `publishFact`）；`posted`、`receivedState`、`countFacts`、`assertLines` |

## 加测试

- 新流程：新文件 `<流程>_integration_test.go`，写一个 `func (fx *fixture) <步>()`，在 `TestFinanceEndToEnd` 里按依赖排进顺序，在上表加一行。
- 测试不为了通过而改（装配项目规范的铁律）。
