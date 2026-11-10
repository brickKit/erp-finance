# itest/：集成测试与测试索引

财务组件对着真实的 PostgreSQL 与消息系统、经它真实的接口（用户面 HTTP、系统面 gRPC、事件）的测试（L2）。构建标签 `integration`；一条业务流程一个文件。

## 怎样跑

- `make test-integration`（`go test -count=1 -tags integration ./itest/`）。
- 环境变量：`BE_SDK_TEST_PG_ADMIN`（口令是装配项目 `.env` 的 `POSTGRES_PASSWORD`）、`BE_SDK_TEST_NATS_URL=nats://localhost:4222`；be-sdk-go 的 `sectest` 是测试部署。
- 前提：装配项目里 `make up` 起了 `be-postgres` 与 `be-nats`。
- 端口：28087（用户面）、9097（系统面）、替身 19120（`mdm/org`）、19121（`mdm/partner`）、19122（`mdm/currency`）、19123（`erp/purchase`）要空着，被占时测试直接失败。
- 每次开始：删掉再建自己的 schema `erp_finance_it`（跑完留着，可以查）；删掉流 `mdm.org`、`erp.billing`、`erp.finance`（别的运行留下的事件）。
- 配置：上海的匹配容差由测试的 `MATCH_TOLERANCES` 定为数量 5 %、价格 0；北京没写（0）。
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

## 加测试

- 新流程：新文件 `<流程>_integration_test.go`，写一个 `func (fx *fixture) <步>()`，在 `TestFinanceEndToEnd` 里按依赖排进顺序，在上表加一行。
- 测试不为了通过而改（装配项目规范的铁律）。
