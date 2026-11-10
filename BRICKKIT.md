# erp/finance

## 组件定位

财务：每个法人一个正式账簿（D51）。
- **科目与过账**：科目表、过账规则（规则键与角色的借贷方向）、法人的科目映射；会计期间按月；凭证借贷平衡、凭证号按法人与期间连续。来源组件不写科目，只按共享的会计事实契约带规则键与角色金额。
- **应收与应付**：订阅 `erp/billing` 的“已出账”形成应收；供应商票据按 `erp/purchase` 的匹配依据做三单匹配，通过后形成应付并发布“票据单价确认”；缺来源时登记人工未清项（经审批）。
- **付款认领与核销**：收付款（`erp/payment`）付款前向本组件认领未清项（只用可用余额）；它发布的资金事实到达时，本组件在一个本地事务里核销，发布“已核销”与应收的结清、减少、重开（给 `erp/credit`）。
- **会计事实契约**：本组件定义（契约的 `events/shared/`），成本、收付款等来源按它发布，本组件按装配清单订阅。

账单与发票（`erp/billing`）、收付款与银行（`erp/payment`）、存货成本（`erp/costing`）、税额（`erp/tax`，远期）不在这里。本版答“能力不支持”（`CAPABILITY_UNSUPPORTED`，D75）的：关账批次的协调、预收与款到发货、预付与借款、外币重估、递延、期末暂估、维度目录、供应商贷项与补差票、催款与报表、期初。

**3.0.0 开发中**：科目表、过账规则与映射、期间、手工凭证与余额，已出账形成应收、未清项查询、`FactService.Read`，供应商票据与三单匹配、付款认领与到期未清项、资金事实的核销、会计事实的过账、人工未清项已实现；本版不做的路由答“能力不支持”。

## 部署前准备

- 数据库：PostgreSQL 16 及以上；schema `erp_finance`，属主角色 `erp_finance_owner`（只供迁移）与运行角色 `erp_finance_rw`（只有读写数据的权限），名字来自装配项目的注册表。三者由部署方在第一次启动前建好；表由本组件的迁移建，迁移里不建角色、不授权。
- 消息系统（`BUS_URL`）与实现实例（`IMPLEMENTATION_INSTANCE`）：凭证、应收、核销、票据单价确认以增量事实，期间以完整状态发布到流 `erp.finance`（4 个分区）；订阅 `mdm/org` 的部门事件（`mdm.org_unit.>`）与 `erp/billing` 的已出账（流 `erp.billing` 的 `erp.invoice.issued.v1`；到期日取账单带的、没有就取开票日期，法人没映射 `sales_invoice` 时等映射补上再过账）。
- 部署的服务证书（`SERVICE_TLS_*`）；身份与授权（`IAM_*`、`AUTHZ_GRPC_URL`、`DEPLOYMENT_ID`）；审计（`AUDIT_GRPC_URL`）。
- 依赖 `mdm/org`、`mdm/partner`、`mdm/currency`；`erp/purchase` 可选。
- 事务密集、期末批量：单跑或外壳按负载。

## 依赖说明

- 必需：数据库、消息系统、身份成员、授权成员、`mdm/org`、`mdm/partner`、`mdm/currency`。
- 可选：`erp/purchase`（缺时供应商票据只能按费用登记，不做订单匹配）、`erp/billing`、`erp/payment`、`erp/costing`（只经事件；缺哪个，相应的事实就没有，用手工凭证或人工未清项处理）、审计组件。
- 主数据不可达时：需要取证的登记答“依赖不可用”，已有数据照常读取。

## 配置指南

全部键见 `component.yaml` 的 `configSchema`（由 SDK 生成，测试核对）。

| 键 | 说明 |
| --- | --- |
| `DEPLOYMENT_ID`、`IAM_ISSUER`、`IAM_JWKS_URL`、`AUTHZ_GRPC_URL`、`SERVICE_TLS_CERT_FILE`、`SERVICE_TLS_KEY_FILE`、`SERVICE_TLS_CA_FILE`（必填） | 受保护的用户面与 mTLS 的系统面要用；部署身份、身份与授权的地址在外壳里是进程级的键 |
| `PG_HOST`、`PG_DATABASE`、`PG_USER`、`PG_PASSWORD`、`PG_SCHEMA`（必填） | 自己的数据库，密码是密钥；迁移另用 `PG_OWNER_USER`、`PG_OWNER_PASSWORD` |
| `BUS_URL`（必填，密钥）、`IMPLEMENTATION_INSTANCE`（必填） | 消息系统；实现实例是安装时生成的随机身份 |
| `MATCH_TOLERANCES` | 供应商票据的匹配容差，按法人：JSON，法人身份 → `{"quantity_percent": "5", "price_percent": "0"}`（十进制字符串）；没写的法人两个都是 0；格式不对时组件起不来 |
| `FINANCE_FACT_SOURCES` | 订阅哪些来源的资金事实与会计事实：JSON 数组 `[{"stream": "erp.payment", "type": "erp.payment.fund_fact.v1"}]`，类型以 `.fund_fact.v1` 或 `.accounting_fact.v1` 结尾；按装了哪些来源填写，没填就不订阅（没有核销）；格式不对时组件起不来 |
| `CLAIM_PERIOD` | 付款认领的期限（时长，例 `168h`）；没填是 7 天 |
| `AUDIT_GRPC_URL` | 审计组件的系统面；缺时审计记录留在本地，按协议 03 处理 |

依赖的地址由 BrickKit 按依赖注入（`MDM_ORG_GRPC_ENDPOINT`、`MDM_PARTNER_GRPC_ENDPOINT`、`MDM_CURRENCY_GRPC_ENDPOINT`，装了时还有 `ERP_PURCHASE_GRPC_ENDPOINT`），不是配置键。凭证号格式 `JV-<法人代码>-<年月>-<六位序号>`（连续）。

## 契约索引

- **系统面**（本仓库 `contract/proto`，模块 `github.com/brickKit/erp-finance/contract/v3`，标签 `contract/v3.…`）：`be.erp.finance.v3.ClaimService`（`Claim`、`Renew`、`Confirm`、`Release`、`Get`、`DueItems`；只对 `erp/payment` 开放，操作 `erp.finance.system.claim`）、`be.erp.finance.v3.FactService/Read`（增量事实按身份读回；任一组件，操作 `erp.finance.system.read`）。整套恢复的核对（`be.recovery.v1.CheckService`）答科目与映射、凭证、期间、未清项、供应商票据、付款认领的“还在不在”，被推过的凭证号登记作废，检查 `erp.finance.pending_facts` 报出收到还没过账的事实。
- **共享契约**：`contract/events/shared/accounting_fact.v1.schema.json`、`fund_fact.v1.schema.json`。来源组件在自己的流上用 `<领域>.<来源>.accounting_fact.v1`、`…fund_fact.v1` 的类型发布这个形状。
- **用户面**：`contract/openapi.yaml`。操作 `erp.finance.account.read`、`.manage`，`erp.finance.voucher.read`、`.edit`、`.approve`，`erp.finance.period.read`、`.close`，`erp.finance.open_item.read`、`.edit`、`.approve`，`erp.finance.supplier_invoice.read`、`.edit`、`.approve`；都带法人维度，未清项与票据另带部门。新建要带 `Idempotency-Key`。
- **事件**（流 `erp.finance`，schema 在 `contract/events/`）：`erp.voucher.posted.v1`、`erp.receivable.formed.v1`、`erp.open_item.settled.v1`、`erp.receivable.cleared.v1`、`erp.receivable.reduced.v1`、`erp.receivable.reopened.v1`、`erp.supplier_invoice.price_confirmed.v1`、`erp.supplier_invoice.price_reversed.v1`（增量事实，按 `fact_id` 去重），`erp.accounting_period.changed.v1`（完整状态）。

## 外壳声明

不是外壳。可以被 Go 的核心外壳（`be/go-core`）承载。
