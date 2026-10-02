[English](design.md) · [中文](design.zh.md)

# erp/finance 设计

只写结论，给要改本组件设计的人看。怎么用：`BRICKKIT.zh.md`；代码怎么组织：`AGENTS.zh.md`。

## 边界

本组件是项目的事件汇：别处发生的事，在这里变成会计语言。它拥有科目表、凭证、应收与应付台账、会计日历及其期间锁，以及每个客户的已用信用额度。

| 不归这里 | 归谁 | 为什么 |
|---|---|---|
| 客户的信用额度值 | `mdm/customer` | 额度是"给他多少"（商务决定，主数据）；已用是"用了多少"（会计事实）。分别归两个组件是刻意的；可用额度 = 额度 − 已用 |
| 订单与发票 | `erp/sales` | 订单过账生成一张凭证；凭证不是订单。改订单是销售的事，改凭证是财务的事，两者互不修改 |
| 库存数量 | `erp/inventory` | 财务记的是调整的价值；数量的真相在库存 |
| 收付款的实际执行 | 支付集成组件（还没有） | 财务记应收应付，不碰支付通道 |
| 多币种 | 客户定制 | 只支持单一币种。每张金额表都留一个恒为本位币的 `currency` 列：分区表以后再加列，代价远大于现在就有 |

## 拥有的数据

| 表 | 分区 | 说明 |
|---|---|---|
| `fiscal_years` | 否 | FY2026（`003`）与 FY2027（`008`），由迁移建 |
| `accounting_periods` | 否 | 自然键 `(period, legal_entity_id)`；状态 `OPEN` / `CLOSED` / `LOCKED`；`last_post_seq` 是 `post_no` 的计数器 |
| `accounts` | 否 | 迁移预置 5 个科目：`1122` 应收账款、`2202` 应付账款、`1405` 库存商品、`6001` 主营业务收入、`6401` 主营业务成本 |
| `finance_journal_entries` | **否** | 凭证头；带源单唯一索引（见契约面），`post_no` 按法人唯一 |
| `finance_journal_entry_lines` | 按 `accounting_period` 做 LIST 分区 | 全项目唯一不按 `created_at` 分区的表；每行恰好一边非零（`CHECK`）；`accounting_period` 与 `legal_entity_id` 从凭证头冗余过来，数据范围过滤不用 JOIN |
| `ar_ledger` | 否 | 金额、`reconciled_amount`（`0 ≤ 已核销 ≤ 金额`）、`due_date`、状态 `OPEN` / `RECONCILED` |
| `ap_ledger` | 否 | 同样的形状；还没有写入方（没有采购组件） |
| `customer_credit_exposure` | 否 | 已用额度的物化值；`CHECK exposure ≥ 0` |
| `customer_credit_snapshots` | 否 | 来自 `mdm.customer.*` 的客户摘要副本：`credit_limit`、`name`、`version` |
| `legal_entity_access` | 否 | `(sub, legal_entity_id)`：谁能看、能改哪个法人的账 |
| `command_idempotency`、`event_outbox`、`event_inbox` | outbox / inbox 按周 | 与每个组件相同 |

客户相关的是两张表（`customer_credit_exposure`、`customer_credit_snapshots`），不是一张：一张是本组件自己的过账事实，一张是外部的副本，两者变化的时机不同。

**凭证两态，`DRAFT` 与 `POSTED`，没有 `CANCELLED`。** 过账的凭证永不修改、永不删除；撤销它是一张借贷互换的新凭证（红字冲销），记进当前期间，绝不记进原凭证的期间。一张凭证只能冲销一次：冲销事务锁住原凭证头，拒绝第二次。现在每张凭证都是直接过账，没有任何路径留下草稿。

**每张凭证两个号。** `entry_no` 来自序列，允许有缺口。`post_no` 是 `P-<期间>-<序号>`，同一法人、同一期间内连续无缺口（审计要求）。它取自过账已经锁住的那一行期间的 `last_post_seq`，随凭证一起提交或回滚；序列在回滚时会留下缺口。计数器按法人分开，所以 `post_no` 按法人唯一，不是全局唯一：每个法人各有一套账。

**期间三态。** `OPEN` → 关账 → `CLOSED` → 锁定 → `LOCKED`（终态）；`CLOSED` → 反关账 → `OPEN`。关账是可逆的日常操作，锁定是不可逆的年度操作。只有一个"已关账"开关的话，第一次月底后发现漏了一张单，就只能在"永远能改"和"永远不能改"之间二选一。

**金额。** 非负、最多两位小数、整数部分最多 16 位的十进制字符串，与 `NUMERIC(18,2)` 一致。校验、借贷平衡与超限比较按分精确计算；合计、未核销余额与账龄在 SQL 里按 `NUMERIC` 算。`float64` 会放过 `"NaN"`，并把大额时差一分当成平衡。

## 契约面

gRPC `erp.finance.v1.FinanceService`：

| rpc | 类型 | 说明 |
|---|---|---|
| `CheckPeriodOpen` | 读 | 给别的组件；咨询性的（见下） |
| `GetCreditExposure`、`BatchGetCreditExposure` | 读 | 给别的组件；查多个客户用批量调用，不要循环；查不到的客户算 0 |
| `ClosePeriod`、`ReopenPeriod`、`LockPeriod` | 写 | 按 `idempotency_key` 幂等 |
| `PostManualEntry`、`ReverseEntry` | 写 | 按 `idempotency_key` 幂等 |
| `GetEntry`、`ListEntries`、`ListARLedger` | 读 | 游标分页，默认窗口最近 90 天 |

面向用户的 rpc 要套用调用者的法人授权，需要验过签的用户身份；gRPC 不带用户身份，所以经 gRPC 调它们在进 service 之前就回 `UNAUTHENTICATED`。它们由 REST 提供。

REST 前缀 `/erp/finance`，每条路由都带权限键，并按调用者的法人授权过滤：

| 路径 | 权限键 | 说明 |
|---|---|---|
| `GET /entries` | `erp.finance.view` | `period`、`status_filter`、`source_doc_id`、`source_doc_type`（订单详情 → 它的凭证）、`created_after` / `created_before`、游标 |
| `GET /entries/{id}` | `erp.finance.view` | 凭证属于调用者无权看的法人时回 `403` |
| `POST /entries` | `erp.finance.post` | 手工凭证，幂等；借贷必须精确相等 |
| `POST /entries/{id}/reverse` | `erp.finance.post` | 幂等；每张只能一次 |
| `POST /periods/{period}/close`、`/reopen`、`/lock` | `erp.finance.close` | 幂等；从不对的状态转换回 `400` |
| `GET /ar-ledger` | `erp.finance.view` | 每行带 `customer_name`、`outstanding`、`due_date` |
| `GET /ar-ledger/summary` | `erp.finance.view` | 合计与账龄（见下） |
| `GET /credit-exposure/{customer_id}` | `erp.finance.view` | 已用额度没有法人维度 |
| `GET` / `POST /legal-entity-access/{sub}`、`DELETE /legal-entity-access/{sub}/{legal_entity_id}` | `erp.finance.manage_access` | 幂等的授予与撤销 |

`CheckPeriodOpen` 与 `BatchGetCreditExposure` 不上 REST：它们是组件之间的协议，不是人的操作。

**跨组件的期间锁**分三层。`CheckPeriodOpen` 是咨询性的：上游改历史单据前先问一句，给它的用户一个及时、体面的报错。权威判定发生在本组件的过账事务内部，不可绕过。迟到的单据不拒绝，记进下一个开放期间（以后期间调整）。问与写之间的竞态是接受的，不是遗漏：关账是一段人工流程，不是一次 API 调用。

**过账幂等**，两层，缺一不可。写命令先用 `INSERT … ON CONFLICT DO NOTHING` 认领 `command_idempotency` 再写。事件生成的凭证由凭证头本身认领：在取 `post_no` 之前 `INSERT … ON CONFLICT (source_component, source_doc_type, source_doc_id, source_revision) WHERE source_component != '' DO NOTHING`；冲突说明另一个事务已经为这张源单过了账，这里什么都不写。inbox 挡住同一条消息两次，唯一索引挡住指向同一张源单的两条不同消息。插入前先 `SELECT` 会让两次并发投递都往下走；改成捕获唯一约束冲突，事务已经作废。几个参考系统都没有这条约束（它们是单进程），这是我们自己加的。

**给前端的应收。**

- `customer_name` 在查询时从客户摘要副本读（`LEFT JOIN`；客户的第一条事件到达之前为空；从 1.x 带过来的客户，副本早于 `name` 列，要等它下一次 `mdm.customer.updated.v1`）。备选是过账时复制进每一行应收；销售订单事件不带客户名，复制也只能从同一份副本取，而查询时读还能跟上改名。
- `outstanding` = `amount − reconciled_amount`，在 SQL 里算。
- `due_date` 在写应收时定下。上游事件不带付款条件，所以就是记账当天（见即付）；销售在事件里加上付款条件（只增字段）之后，财务就能用它。
- `GET /ar-ledger/summary` 返回 `total_receivable`、`total_reconciled`、`outstanding`，以及未核销余额按逾期天数——`as_of`（今天，UTC）减 `due_date`——的分桶：`d0_30`（含未到期）、`d31_60`、`d61_90`、`d90_plus`；第 30 天在第一桶，第 31 天在第二桶。它不套列表的默认 90 天窗口：这是全部未结清应收的统计，最老的那一桶才最要紧。所有值都是两位小数的十进制字符串（没有数据时是 `"0.00"`）。
- 只改 REST；gRPC 消息不变，契约包版本不变。

## 事件

发布，与过账在同一个事务里经 outbox：

| Subject | 分级 | 何时 | Payload |
|---|---|---|---|
| `finance.voucher.posted.v1` | 旁路 | 每次过账之后 | 凭证 id、entry no、post no、期间、金额（借方合计） |
| `finance.credit.rejected.v1` | 核心 | 销售订单过账使已用额度超过已配置的额度（额度为 0 表示"没配"，绝不是"不许赊"） | 客户 id、订单 id、已用额度、额度 |

`finance.credit.rejected.v1` 由 `erp/sales` 消费，把订单挂起。信用额度刻意判两次：销售建单前用本地副本预判（便宜，挡住明显超限的），财务过账时用权威值再判一次。这比 Odoo、ERPNext 只提示不拦更严。

消费：

| Subject | 来自 | 作用 |
|---|---|---|
| `sales.order.created.v1` | `erp/sales` | 应收凭证（借 `1122` / 贷 `6001`）、一行到期日为记账当天的应收、已用额度增加、超限判定 |
| `erp.inventory.adjusted.v1` | `erp/inventory` | 存货凭证：入库借 `1405` / 贷 `2202`；出库与盘亏借 `6401` / 贷 `1405`；盘盈相反。金额 = 数量绝对值 × 每单位 1 元，是占位 |
| `mdm.customer.created.v1`、`mdm.customer.updated.v1` | `mdm/customer` | 客户摘要副本（额度、客户名）；只有事件的 version 大于已存的才生效——inbox 只在同一个 subject 内单调，这是两个 subject。`credit_limit` 不是合法金额（`NaN`、负数、指数写法）时按 `0`（未配置额度）存并记 Warn；过账时读到解析不了的已存值也同样处理，上游的坏值不会挡住应收 |

事件生成的凭证的记账日期是处理它的那一天：事件不带业务日期，期间就按那一天定。上游事件不带法人，它们的凭证记在法人 `default` 名下。

## 依赖

没有，这是设计。预期中却没有的：

| 不是依赖 | 为什么 |
|---|---|
| `erp/sales`、`erp/inventory` | 只消费它们的事件；一条同步边会把事件汇放进调用链 |
| `mdm/customer` | 要用额度值，但来自事件副本，不是调用；副本的最终一致窗口（毫秒级）对"额度刚改就下单"可以接受 |
| `infra/iam-casdoor`、`infra/authz` | JWKS 与权限 bundle 是配置（`IAM_JWKS_URL`、`AUTHZ_BUNDLE_URL`），不是边 |
| `mdm/product` | 真实的单位成本需要它；占位金额在成本方法定下来之前避免了这条边（见未决问题） |

## 在同步调用图里的位置

叶子。`erp/sales` 持有调 `CheckPeriodOpen` 与 `BatchGetCreditExposure` 的 gRPC 客户端；财务不调任何人。它是三个枢纽之一（主数据被所有人读；库存是库存流水唯一的写入方；财务主要被调用、听事件）。两张图要分开看：事件图里财务有一条出边（`finance.credit.rejected.v1` → `erp/sales`），同步图里销售有一条指向财务的边。这不是环；把事件边画进同步图去判环，是这里最典型的分析错误。

## 分区与归档

分录行按会计期间分区（LIST，每个 `YYYY-MM` 一个分区），不按时间：期间是离散的业务标识。没有提前建未来分区的后台任务：开新的会计年度是一次业务动作，不是日历往前走的必然事实，新一年的期间与分区随一份迁移来。迁移把分区表的属主交给 `erp_finance_rw`，以后归档才能以这个角色 DETACH 分区。

| 数据 | 热 | 归档条件 | 去哪 |
|---|---|---|---|
| 分录行 | 未关账期间与最近 12 个月 | 只有期间 `LOCKED` 才能归档，绝不因为"够老" | `erp_finance_archive` |
| 凭证头 | 永久 | 不归档 | — |
| 应收 / 应付行 | 未结清期间 | 已结清且超过 24 个月 | `erp_finance_archive` |
| 科目、会计年度、期间 | 永久 | 不归档 | — |

这种不对称是刻意的：凭证头上的幂等唯一索引必须永远有效（三年前的源单再投递一次也不能再过一次账），所以头留下，明细可以走。现在还什么都没归档；归档 schema 是空的。

## 数据范围

`legal_entity`，`mode: in`，作用在 `finance_journal_entries`、`finance_journal_entry_lines`、`ar_ledger`、`ap_ledger` 上：多公司集团里，A 公司的会计不该看到 B 公司的账。`legal_entity_id` 是业务列，不是权限专用列；分区的分录行从一开始就有它，因为分区表以后再加列代价大得多。没有 owner、部门列。

范围数据是本组件自己的（`legal_entity_access`），不是 JWT 声明。每个读路径把调用者的列表下推进 SQL（`legal_entity_id = ANY(...)`；空列表什么都匹配不到，未授权的用户什么都看不到），每个写路径核对点名的法人，冲销核对原凭证的法人。现在只有一个法人（`default`），但代码与测试都按多个法人跑。

## 参考实现

| 项目 | 版本 | 看的模块 | 借鉴了什么 | 许可证 | 用法 |
|---|---|---|---|---|---|
| Tryton | 7.x | `modules/account/move.py` | 凭证只有 draft / posted 两态；取消是写一张反向凭证；`number` 与 `post_number` 分开 | GPL-3 | 借鉴逻辑 |
| Tryton | 7.x | `modules/account/period.py`、`fiscalyear.py` | 期间三态，关账可逆、锁定是终态 | GPL-3 | 借鉴逻辑 |
| ERPNext | v14+ | `accounts/doctype/payment_ledger_entry/` | 应收应付从总账拆出来物化，否则核销要改写总账分录 | GPL-3 | 借鉴逻辑 |
| ERPNext | v15 | `accounts/doctype/accounting_period/` | 跨单据类型的期间控制 | GPL-3 | 借鉴逻辑 |
| Oracle Fusion | — | `GL_PERIOD_STATUSES` 文档 | 期间状态是一等的、可查询的实体 | 闭源 | 借鉴实际应用 |

**刻意避开的**：ERPNext 的 `make_gl_entries` 不去重（要调用方先冲销），是反复出现重复总账条目的根源——用凭证头唯一索引代替。ERPNext 运行时 DDL 的会计维度——用固定列代替。Frappe 的编号计数器行（全局热点行、回滚留缺口）——`post_no` 用过账本来就锁住的那一行期间。Odoo 只有锁定日期没有期间实体，以及它悄悄顺延记账日期的做法。

**槽位族候选**：关账策略（Tryton 的期间实体加前置校验、Odoo 的锁定日期、ERPNext 的三套机制）与迟到单据策略（拒绝 / 顺延 / 有权限的人强改）因客户规模而异。因为 `erp/sales` 对本组件有同步边，它们不能做成 slot；可行的形态是组件内部策略或客户定制。现在：顺延。

## 未决问题

| 问题 | 现在的答案 |
|---|---|
| 开会计年度 | 期间建到 2027-12-31（迁移 `008`）。下一年的期间不在，从 1 月 1 日起每次过账都以 `NotFound` 失败，包括每张由事件生成的凭证，这些事件就丢了（投递是至多一次）。开年度是一次业务动作，所以需要一个管理接口（权限键 `erp.finance.close` 或新键），或一个提前建好下一年的定时任务，外加第四季度下一年还没开时的告警。在那之前每年都是一份像 `008` 这样的迁移 |
| 库存调整的真实成本 | 占位：每单位 1 元。标准成本 / 移动加权 / FIFO 是成本槽位族候选，该归哪个组件（金额在这里、数量在库存）还没定 |
| 应收从不结清 | 没有收款流程写 `reconciled_amount` 或冲减已用额度；已用额度只增不减。收款流程属于支付集成 |
| 已用额度与总账对不上 | 以总账为准，已用额度是缓存；比对它们的对账任务还没有 |
| 期间"先问再写"竞态的更强解法 | 暂时接受（事务内的判定兜底）；备选是分布式锁（不用，没有 Redis 类组件）或关账时反向扫描已放行的单据，与对账一起做 |
| gRPC 上的用户身份 | 面向用户的 rpc 经 gRPC 会失败；在 gRPC 上传递用户身份是 SDK 的工作 |
| 付款条件 | `due_date` 是记账当天；销售在事件里发布付款条件或到期日（只增字段）之后，财务用它 |
| 归档 | 上面的规则已定；还什么都没归档 |
| 应付台账 | 只有结构，等采购组件 |
| 多个法人 | 代码与测试都支持；没有接口能建一个法人的期间，所以新法人也随一份迁移来 |
| 事件生成的凭证的业务日期 | 事件不带业务日期，期间跟着处理日期走：31 号建的订单过了午夜才处理，就落进下个月。销售发布业务日期（只增字段）之后过账应该用它；不管日期是哪天，凭证头的唯一索引都保证重投不会再过一次账 |
