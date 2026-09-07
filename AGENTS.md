# erp-finance · AI 助手导读

## 身份证

| 项 | 值 |
|---|---|
| 组件 ID | `erp/finance` |
| 仓库名 | `erp-finance` |
| 端口 | HTTP `8087` / gRPC `9097`（`registry/ports.tsv`，装配仓库根目录那份） |
| schema / role | `erp_finance` / `erp_finance_rw`（归档 schema `erp_finance_archive`，**本组件真的会用**，见设计计划 §7） |
| 语言 / 框架 | Go：Gin + `database/sql` + `pgx/v5/stdlib` + `sqlc` + `golang-migrate` |
| 合并部署时进 | 外壳一 `go-core` |
| 装配角色 | `default` |
| 设计真相源 | 装配仓库 `docs/design/erp-finance.md`——本文件与它冲突时，以那份为准，回来改这里 |

## 边界

**归我：** 会计科目表、总账分录、应收/应付台账、会计日历与期间锁、对账——设计书 §2.6 事件汇枢纽："别人发生的事，在这里变成会计语言"。

**不归我：**
- 客户的**信用额度值**（`credit_limit`）：归 `mdm-customer`，本组件只持有"已用额度"（`credit_exposure`）
- 订单、发票单据本身：归 `erp-sales`。本组件收到事件后生成**凭证**，凭证不是订单，两者不能互相改
- 库存的**数量**：归 `erp-inventory`。本组件消费 `erp.inventory.adjusted.v1` 只关心金额
- 付款的实际执行（走银行接口）：归 `integration-payment-*`（阶段六）
- 多币种、汇率：`customer_fork`（标准版仅支持单币种，设计书 §5.5）

`data_scopes` 声明 `legal_entity` 维（设计书 §14.2.2）：多公司集团的客户里，法人 A 的会计不该看到法人 B 的账。阶段二只有一个默认法人，过滤逻辑到阶段三才实现，**但 `legal_entity_id` 列必须现在就建**——分区表回头加列的代价比建表时多两个数量级（设计计划 §1）。

## 契约面与事件

**gRPC `erp.finance.v1.FinanceService`：** `CheckPeriodOpen`（读，跨组件期间锁的**咨询性**入口，不是权威判定）、`ClosePeriod`/`ReopenPeriod`/`LockPeriod`（写）、`GetCreditExposure`/`BatchGetCreditExposure`（读，`BatchGetCreditExposure` 是防 N+1 的唯一合法方式）、`PostManualEntry`（写，手工凭证）、`ReverseEntry`（写，红字冲销不是删除）、`GetEntry`/`ListEntries`/`ListARLedger`（读）。

**REST 前缀：** `/erp/finance/**`。`CheckPeriodOpen`/`BatchGetCreditExposure` 不暴露到 REST——组件间协议，不是人类操作。

**发布事件：** `finance.voucher.posted.v1`（旁路分析事件）、`finance.credit.rejected.v1`（核心交易事件，**本阶段唯一一条"事件反向流回上游"的边**——`erp-sales` 消费它把订单转为挂起）。

**消费事件：** `sales.order.created.v1`（生成应收凭证 + 累加 `credit_exposure`）、`erp.inventory.adjusted.v1`（生成存货科目凭证）、`mdm.customer.created.v1`/`.updated.v1`（维护客户摘要副本，含信用额度值）。**本组件是本阶段第一个真的把 `besdk.Consume` 用在"从零到有完整消费闭环"上的组件**——`erp-inventory`（Task 10）已经先用上了一次（消费 `mdm.product.*`），这里是第二次，且是本阶段消费面最重的一个（三个不同来源的事件）。

## 依赖与「为什么不依赖某某」

`dependencies.components` 永远是空数组。

- **不依赖 `erp-sales`/`erp-inventory`**：本组件只消费它们的事件，不同步调用（§4.2 同步图里零出边，这正是"事件汇枢纽"的含义）。⚠️ `finance.credit.rejected.v1` → `erp-sales` 是**事件图**里的边，不在同步图里——把它当成同步依赖去判环是本阶段最容易犯的分析错误（设计计划 §6）。
- **不依赖 `mdm-customer`**：要用客户的信用额度值，但走事件摘要副本，不同步调用去取。理由同 `erp-inventory` 对 `mdm-product`——加一条同步边就把事件汇变成链上一环。
- **不依赖 `infra-iam-casdoor`**：IAM 走 JWT 本地验签（决策 87）。

## 这个组件特有的坑

| 不许 | 症状 | 出处 |
|---|---|---|
| 给 `dependencies.components` 加任何一条 | 编译、启动、测试全都正常——**没有任何症状**。但事件汇枢纽从此有了出边，同步图迟早成环 | §2.6、设计计划 §5 |
| 把幂等过账的唯一约束建在 `finance_journal_entry_lines`（按 `accounting_period` 分区）上 | PostgreSQL 直接拒绝建表（除非把分区键塞进唯一键，而那等于"同一张源单在不同期间可以过两次账"——正好是要防的事）。约束必须建在**不分区**的凭证头 `finance_journal_entries` 上 | 设计计划 §2.3 |
| 以为 `event_inbox`（`besdk.Consume` 自带）能防住重复过账 | `event_inbox` 防的是"同一条消息投两次"；唯一约束防的是"两条不同消息指向同一张源单"（如 `created` 与一条补发的 `updated` 都试图建凭证）。**两层都要有，缺一不可** | 设计计划 §2.3 |
| 用消息到达时间而不是源单业务日期推导 `accounting_period` | 消息延迟几小时跨了月底，用到达时间会把凭证记进错误的期间——而期间是分区键，同一张源单重投两次可能落进两个不同分区，头上的唯一约束才拦得住 | 设计计划 §2.3 |
| 把会计期间做成一个 `is_closed` 布尔 | 第一次"月底关账后发现漏了一张单，要不要开回去"就会露馅——关账是可逆的日常操作，锁定是不可逆的年度操作，合并成一个开关只能"永远能改"或"永远不能改"二选一 | 设计计划 §2.2 |
| 凭证过账后允许 `UPDATE`/`DELETE` | 违反会计原则：账一旦过账就是历史事实。要改只能记一笔红字冲销凭证（`ReverseEntry`），原凭证一个字不动 | 设计计划 §2.1 |
| 把 `CheckPeriodOpen` 当成权威判定用（比如上游拿到 `OPEN` 就跳过后续校验） | check 与 write 之间有竞态（期间可能正好在这中间关掉）——`CheckPeriodOpen` 只是咨询性的，真正的权威判定在过账事务内部（第 2 层防护）。当成权威判定用会在关账的那一刻窗口期漏过一张单 | 设计计划 §3.1 |
| 归档 `finance_journal_entry_lines` 时只看时间不看期间状态 | 归档判据是期间 `LOCKED`，不是"够老"——一个 18 个月前但因审计争议还没锁定的期间，一行都不许归档 | 设计计划 §7 |
| 归档明细时把凭证头也归档 | 头上那条幂等唯一约束必须永远有效，头归档后一张三年前的源单重投事件会重新过一遍账。头不归档、明细归档是刻意的不对称 | 设计计划 §7 |
| 事件驱动的自动过账（消费 `sales.order.created.v1`/`erp.inventory.adjusted.v1`）用"先插、撞了 `finance_journal_entries_source_uniq` 就捕获错误当作重复"的写法 | PostgreSQL 里一条语句真的执行失败后，**整个事务**会被标记成 aborted，即使 Go 这层选择吞掉那个错误，事务在数据库那侧也回不去了，随后任何语句（含 COMMIT）都会失败。必须先 `SELECT` 判断源单是不是已经处理过，确认没有才真的插入 | `backend/internal/repo/autoentry.go` 的 `findExistingBySource`；同 mdm-product `archive.go` 的既有教训 |
| `post_no` 生成用 `CREATE SEQUENCE` 或另开一把锁 | 序列在事务回滚时不会把已分配的号退回去（天然留缺口），达不到"同一期间连续无缺口"的审计要求；另开一把锁则是重复造轮子——`postEntryTx` 锁期间行做权威判定本来就已经拿到了这把锁，`post_no` 计数器搭它的便车 | 设计计划 §9 第 7 条；`backend/internal/repo/period.go` 的 `nextPostNo` |
| 给 `besdk.Consume` 测试的 `aggregate_id` 用固定字符串 | `event_inbox` 按 `(subject, aggregate_id, version)` 单调去重且持久化，第二次跑测试套件时这个组合已经"处理过"，`Consume` 静默跳过、handler 根本不会被调用——断言读到的是"从没处理过"的初始状态，不是真的失败 | `backend/internal/consumer/consumer_test.go`；同 `erp-inventory` 的既有约定 |

## 改代码前的自查

1. **我是不是在给这个组件加一条 `dependencies.components`？** 停下——事件汇枢纽零出边是设计前提，不是暂时状态。
2. **我写的这条唯一约束/索引，是不是建在了分区表 `finance_journal_entry_lines` 上？** 幂等过账的唯一约束必须在不分区的凭证头上。
3. **我是不是只加了 `event_inbox` 去重就以为过账幂等的问题解决了？** 停下——两层缺一不可，见上表第二条。
4. **我是不是在 `UPDATE`/`DELETE` 一条已经 `POSTED` 的凭证？** 停下——过账后永不修改永不删除，用红字冲销。
5. **我是不是把 `CheckPeriodOpen` 的返回值当成"这一步一定能写"来用？** 停下——它只是咨询性的，权威判定在过账事务内部。
6. **这个改动会不会让 `contracts/finance.proto` 出现破坏性变更？** 下游 `erp-sales` 消费这份契约（`finance.credit.rejected.v1` 事件 + `CheckPeriodOpen`/`GetCreditExposure` 等 rpc），只能向后兼容地追加。
