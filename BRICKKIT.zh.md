# erp/finance

## 组件定位

会计账簿：科目表、凭证、应收与应付台账、会计日历及其期间锁，以及每个客户的已用信用额度。别的组件发生的业务事件在这里变成会计分录；人在这里录手工凭证、红字冲销、关账。

**归本组件**

- 凭证（头 + 分录行）：过账一次，之后永不修改、永不删除；记错了用一张冲销凭证纠正。每张凭证两个号：`entry_no`（允许有缺口）与 `post_no`（`P-<期间>-<序号>`，同一法人、同一期间内连续无缺口）。
- 每个法人的会计期间，三态：`OPEN`、`CLOSED`（可逆）、`LOCKED`（终态）。业务日期落在非开放期间的凭证记进下一个开放期间。
- 应收台账（金额、已核销金额、到期日）与应付台账（还没有写入方），与总账分开：核销应收不会去碰已过账的分录行。
- 每个客户的已用信用额度（`exposure`），以及销售订单过账时权威的超限判定。
- 每个客户的摘要副本（额度值与客户名），靠 `mdm.customer.*` 事件保持最新。
- 谁能看、能改哪个法人的账（`legal_entity_access`）。
- 事件 `finance.voucher.posted.v1` 与 `finance.credit.rejected.v1`。

**不归本组件**

- 客户的信用额度值本身与客户档案：`mdm/customer`。本组件持有的是已用额度；额度减已用才是可用额度。
- 订单与发票：`erp/sales`。订单过账在这里生成一张凭证；凭证不是订单，两者互不修改。
- 库存数量：`erp/inventory`。库存调整事件按占位金额（每单位 1 元）记账，不是真实成本。
- 收付款的实际执行（银行接口），以及核销应收的收款流程：还没有；现在没有任何东西写 `reconciled_amount`。
- 多币种：只支持单一币种。

## 部署前准备

- **PostgreSQL**：schema `erp_finance` 与 `erp_finance_archive`（归档 schema 目前没有任何写入）。登录角色 `erp_finance_rw`，在两个 schema 上都有 `USAGE` 与 `CREATE`，以及它的密码。迁移以这个角色运行并建表，表的属主就是它；运行中的组件自己给 `event_outbox` / `event_inbox` 建周分区，需要这个属主身份。BrickKit 不建这些；在 BrickEnterprise 装配项目里由 `make dev-env` 把密码写进 `.env`、`make db-init` 建 schema、角色与授权。
- **迁移建好的东西**：5 个科目（`1122` 应收账款、`2202` 应付账款、`1405` 库存商品、`6001` 主营业务收入、`6401` 主营业务成本）、法人 `default` 的 2026 与 2027 两个会计年度（各 12 个月度期间），以及对应的分录行分区。
- **每个新会计年度都要在 1 月 1 日之前开好**：没有开会计年度的接口，也没有后台任务。业务日期落在没有期间的年份里的过账会失败（`NotFound`）——包括所有由事件生成的凭证，而事件是至多一次投递，这些事件就丢了。现在新年度随本组件的一份迁移来（FY2027 就是这样）；部署方必须在年度开始之前让下一年的期间与分区就位，并应在第四季度核对。
- **先授权，否则谁都看不到**：所有读写都限定在调用者被授权的法人范围内（`legal_entity_access`）。用 `POST /erp/finance/legal-entity-access/{sub}`（权限键 `erp.finance.manage_access`）授权；授权之前连管理员看到的也是空列表，写入一律 `403`。
- **NATS** 可经 `NATS_URL` 访问：组件消费事件，并通过 outbox 表与后台推送线程发布自己的事件。NATS 不可达时组件照样启动；这期间要发的事件留在 outbox 里，别人这期间发来的事件收不到。
- **权限**（`infra/authz`）与**身份**（`infra/iam-casdoor`，或任何提供 JWKS 的 IAM）可经 `AUTHZ_BUNDLE_URL`、`IAM_JWKS_URL` 访问，REST 路由才会给出正常应答。它们是配置，不是依赖：没有它们组件照样启动。
- 演示数据（可选）：组件跑起来之后在组件目录 `make seed`。它发出几条样例销售订单事件，让应收台账与账龄有数据；人工凭证与期间示例还要求 `infra/iam-casdoor`、`infra/authz` 在项目里。

## 依赖说明

没有。财务只听事件、只被调用，从不调用别的组件。它消费 `erp/sales`（`sales.order.created.v1`）、`erp/inventory`（`erp.inventory.adjusted.v1`）与 `mdm/customer`（`mdm.customer.created.v1`、`mdm.customer.updated.v1`）的事件；这些组件都不必在项目里它也能启动，缺席的那个的事件只是不会到来。`finance.credit.rejected.v1` 反向流回 `erp/sales`，由它把订单挂起：这是事件图里的边，不是同步依赖。

权限 bundle 与 JWKS 从配置里的 URL 拉取，不经依赖边。没有它们时每条受保护路由都失败关闭：没有 token 或 token 无效 → `401`；`IAM_JWKS_URL` 为空或不可达 → `403`；bundle 还没拉到过 → `503`；合法用户但没有权限键 → `403`。`/healthz` 始终是 `200`：它只报告本进程活着。

## 配置指南

| 变量 | 含义 |
|---|---|
| `PG_HOST` | PostgreSQL 主机。通常写项目的共享值（`$var:PG_HOST`）。 |
| `PG_PORT` | PostgreSQL 端口；默认 `5432`。 |
| `PG_DATABASE` | 存放 `erp_finance` schema 的库（`$var:PG_DATABASE`）。 |
| `PG_USER` | 登录角色，写字面量 `erp_finance_rw`。进外壳后外壳以自己的角色登录，每个事务里切到这个角色（`SET LOCAL ROLE`），所以角色名必须是 `<PG_SCHEMA>_rw`。 |
| `PG_PASSWORD` | `PG_USER` 的密码。密钥：写 `${ERP_FINANCE_DB_PASSWORD}`（或你的密钥库引用），不写明文。 |
| `PG_SCHEMA` | 全部表、outbox 与迁移状态表（`schema_migrations_erp_finance`）所在的 schema；默认 `erp_finance`。事件写进这个 schema 的 outbox。照样写出字面量，所有组件的 schema 在一处就能看到。 |
| `NATS_URL` | 消费者订阅、outbox 推送线程发布用的 NATS 服务器（`$var:NATS_URL`）。 |
| `OTEL_BASE_URL` | OpenTelemetry collector 的基础 URL；为空（默认）时什么都不导出。 |
| `AUTHZ_BUNDLE_URL` | 权限判定轮询的 bundle 地址，例如 `http://infra-authz-2-0-0:8223/authz/bundle`。必填：没有它每条受保护路由都回 `503`。要与项目里 authz 的版本保持一致。 |
| `IAM_JWKS_URL` | 本地验签用户 token 的 JWKS 地址，例如 `http://infra-iam-casdoor-2-0-0:8200/.well-known/jwks.json`。必填：没有它每条受保护路由都回 `403`。 |

## 契约索引

- `contracts/erp/finance/v1/finance.proto` — gRPC `erp.finance.v1.FinanceService`。给别的组件用的：`CheckPeriodOpen`（咨询性的：权威判定在过账事务内部）、`GetCreditExposure`、`BatchGetCreditExposure`（一次取多个客户；查不到的客户算 0）。面向用户的 `ClosePeriod`、`ReopenPeriod`、`LockPeriod`、`PostManualEntry`、`ReverseEntry`、`GetEntry`、`ListEntries`、`ListARLedger` 要靠调用者身份套用 `legal_entity_access`，gRPC 不带用户身份：经 gRPC 调它们回 `INTERNAL`，请用 REST。Go 包是独立模块 `github.com/brickKit/erp-finance/gen/erp/finance`。
- `contracts/finance.openapi.yaml` — REST，前缀 `/erp/finance`，每条路由都带权限键，并按调用者的法人授权过滤：
  - `erp.finance.view`：`GET /entries`（`period`、`status_filter`、`source_doc_id`、`source_doc_type`、`created_after` / `created_before`（默认最近 90 天）、`cursor`、`page_size`）、`GET /entries/{id}`、`GET /ar-ledger`（每行带 `customer_name`、`outstanding` = 金额 − 已核销、`due_date`；`customer_id`、时间窗口、游标）、`GET /ar-ledger/summary`（`total_receivable`、`total_reconciled`、`outstanding`，以及未核销余额按逾期天数的账龄分桶：`d0_30`（含未到期）、`d31_60`、`d61_90`、`d90_plus`；可选 `customer_id`；不套时间窗口）、`GET /credit-exposure/{customer_id}`。
  - `erp.finance.post`：`POST /entries`（手工凭证，按 `idempotency_key` 幂等；金额是非负、最多两位小数的十进制字符串，借贷必须精确相等）、`POST /entries/{id}/reverse`（幂等；一张凭证只能冲销一次）。
  - `erp.finance.close`：`POST /periods/{period}/close`、`/reopen`、`/lock`（幂等；`LOCKED` 是终态，锁定前必须先 `CLOSED`）。
  - `erp.finance.manage_access`：`GET` / `POST /legal-entity-access/{sub}`、`DELETE /legal-entity-access/{sub}/{legal_entity_id}`。
  - 金额一律十进制字符串；列表一律游标分页，没有 offset。
- `contracts/events/finance.events.json` — 经 outbox 发布：`finance.voucher.posted.v1`（每次过账之后；给报表用）与 `finance.credit.rejected.v1`（销售订单过账使客户已用额度超过已配置的额度时）。消费（每条按 `(subject, aggregate_id, version)` 只处理一次，每张源单至多过账一次）：`sales.order.created.v1`（应收凭证、到期日为记账当天的应收台账行、已用额度）、`erp.inventory.adjusted.v1`（按占位金额记存货凭证）、`mdm.customer.created.v1` / `mdm.customer.updated.v1`（客户摘要副本，版本大的为准；不是合法金额的 `credit_limit` 按未配置额度处理）。
- `assembly.yaml` — 本项目自己的元数据：四个权限键、菜单项、网关路由 `/erp/finance/**`、schema 与角色，以及凭证头、分录行、应收与应付台账上的数据范围 `legal_entity`（`mode: in`）。

## 外壳声明

不是外壳。可以被 Go 外壳托管（BrickEnterprise 项目里是 `be/go-core`），也可以独立运行；两种方式代码相同。
