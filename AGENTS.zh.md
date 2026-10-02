[English](AGENTS.md) · [中文](AGENTS.zh.md)

# erp/finance

开发本组件的 AI 指南。用法、边界与契约：BRICKKIT.md；为什么长这样：`docs/design.zh.md`；依赖、配置与部署：component.yaml。

## 代码地图

| 路径 | 负责 |
|---|---|
| `backend/module/module.go` | 唯一入口 `New(ctx, rt)`：组装 repo → service → HTTP + gRPC，启动 outbox 推送、分区维护与事件消费。独立运行与进外壳是同一个函数 |
| `backend/cmd/server/main.go` | 一行：`besdk.RunStandalone(module.New)` |
| `backend/cmd/migrate/main.go` | 一行：`migrate.Main(migrations.FS)`，迁移容器的入口（`./migrate up`） |
| `backend/internal/repo/repo.go` | 哨兵错误、claim-first 幂等的工具函数（`claimIdempotency`、`finalizeIdempotency`） |
| `backend/internal/repo/entry.go` | `postEntryTx`：所有凭证都走的唯一过账路径——校验 → 锁期间 → 认领凭证头 → `post_no` → 分录行 → outbox；`publish` |
| `backend/internal/repo/money.go` | 金额按分精确换算（`parseCents`、`formatCents`）与严格的金额格式 |
| `backend/internal/repo/manual.go` | `PostManualEntry`、`ReverseEntry`（claim-first，一张凭证只冲销一次） |
| `backend/internal/repo/entry_read.go` | `GetEntry`、`ListEntries`（过滤条件、法人范围、keyset 分页） |
| `backend/internal/repo/autoentry.go` | 事件生成的凭证：销售订单（应收、已用额度、超限事件）、库存调整（占位金额） |
| `backend/internal/repo/arledger.go` | 应收台账：写入、`ListARLedger`（客户名、未核销余额、到期日）、`SummarizeARLedger`（合计与账龄） |
| `backend/internal/repo/period.go` | 期间状态机、`CheckPeriodOpen`、`lockOpenPeriodForDate`、`nextPostNo` |
| `backend/internal/repo/credit.go` | 每个客户的已用额度；`snapshot.go` 是客户摘要副本 |
| `backend/internal/repo/access.go` | `legal_entity_access`：调用者能访问的法人、授予、撤销 |
| `backend/internal/service/` | 入参校验、取调用者的法人授权、错误 → gRPC 状态码映射（`status.go`） |
| `backend/internal/http/http.go` | REST 路由，每条都带权限键注册；`parseWindow` |
| `backend/internal/grpc/grpc.go` | `erp.finance.v1.FinanceService` |
| `backend/internal/consumer/consumer.go` | 四个被消费 subject 的订阅与它们的 payload 结构 |
| `backend/internal/partition/` | `event_outbox` / `event_inbox` 的周分区，提前四周建好 |
| `migrations/` | SQL 迁移，由 `migrations/embed.go` 嵌进二进制；`003` 预置科目与 FY2026 的期间，`008` 开 FY2027 |
| `contracts/` | proto、OpenAPI、事件 schema |
| `gen/erp/finance/` | 生成的 Go 代码：独立的嵌套 Go 模块，单独打 gen/erp/finance/v1.x.y 的 tag；不手改 |
| `scripts/` | 本地演示数据 `seed.sh` |

| 要做的事 | 先看 | 再看 |
|---|---|---|
| 金额或借贷平衡的规则 | `backend/internal/repo/money.go` | `backend/internal/repo/entry.go`（`validateBalanced`）、`backend/internal/repo/money_test.go` |
| 任何会过账的改动 | `backend/internal/repo/entry.go`（`postEntryTx`） | 它在 `manual.go`、`autoentry.go` 里的调用方 |
| 新消费一个事件 | `backend/internal/consumer/consumer.go` | `backend/internal/repo/` 里对应的 `*Tx` 函数、`backend/internal/consumer/consumer_test.go` |
| 应收列表或账龄 | `backend/internal/repo/arledger.go` | `backend/internal/http/http.go`、`contracts/finance.openapi.yaml`、`backend/internal/repo/arsummary_test.go` |
| 新的 REST 查询参数 | `backend/internal/http/http.go` | `contracts/finance.openapi.yaml`、`backend/internal/http/http_test.go` |
| 谁能看哪个法人 | `backend/internal/repo/access.go` | `backend/internal/service/service.go`（`allowedLegalEntityIDs`） |
| 开新的会计年度 | `migrations/`（一份新迁移：每个法人的期间与分录行分区） | 照 `migrations/008_open_fiscal_year_2027.up.sql` 的写法 |
| 某个错误回错了状态码 | `backend/internal/service/status.go` | `backend/internal/repo/repo.go` 里的哨兵错误 |

## 构建与测试

```bash
# 测试连测试库 brickkit_test_db，绝不连 brickkit_db
export TEST_PG_DSN="postgres://postgres:<密码>@localhost:5432/brickkit_test_db?sslmode=disable"
export TEST_NATS_URL=nats://localhost:4222       # 消费者测试真的发消息
make test                    # 每个包都以 "ok" 结尾；没设 TEST_PG_DSN 时拒绝运行
go test ./... -count=1 -v | grep -c -- '--- SKIP'   # 0：没有被跳过的测试
make check-version dag-check contract-check import-scan module-check   # 各打印一行 ✓
make docs-check              # "0 with errors, 0 warnings"
```

迁移以登录角色 `erp_finance_rw` 运行：设好 `PG_HOST PG_PORT PG_DATABASE PG_USER PG_PASSWORD PG_SCHEMA` 后 `make migrate-idempotent` 打印 `✓ 迁移幂等`；在 BrickEnterprise 项目里用项目根的 `make test-db-init ID=erp/finance`，由它提供这些值。真机验证在项目根：`make verify ID=erp/finance ROUTE=/erp/finance/entries FOCUS=1 SEED=1` 构建镜像、只起本组件需要的东西、核对迁移、健康与权限判定、灌演示数据、跑一次 focus，然后收尾。

## 设计取舍

- **事件汇，没有出边。** 没有依赖；所有东西都以事件到来，唯一反向送出的也是事件。详见 `docs/design.zh.md`。
- **所有凭证都走 `postEntryTx`**：它锁住期间行（`FOR UPDATE`），一次同时完成期间是否开放的权威判定与连续无缺口的 `post_no`。
- **两套幂等机制**：写命令先认领 `command_idempotency`；事件生成的凭证以 `ON CONFLICT DO NOTHING` 认领凭证头上的源单唯一索引。inbox 挡住同一条消息两次，唯一索引挡住指向同一张源单的两条消息。
- **金额精确**：换算成分，绝不经过 `float64`；合计与未核销余额在 SQL 里按 `NUMERIC` 算。
- **法人数据范围是本组件自己的数据**（`legal_entity_access`），不是 JWT 声明：读路径把授权列表下推进 SQL，写路径核对请求点名的法人。
- **客户名在查询时读**客户摘要副本，不复制进每一行应收。

## 易错点

| 不许 | 症状 | 原因 |
|---|---|---|
| 给 `dependencies.components` 加任何一条 | 编译、测试全绿；事件汇从此进了调用图，下一个人再加一条边就成环 | 财务只听事件、只被调用；`make dag-check` 对任何依赖都失败 |
| 事件生成的凭证先 `SELECT` 再 `INSERT` 认领，或捕获唯一约束冲突 | 两次并发投递：一次撞 `finance_journal_entries_source_uniq` 失败，消费者把它丢掉；捕获冲突则事务已作废，`COMMIT` 失败 | 凭证头的插入本身就是认领（`ON CONFLICT … WHERE source_component != '' DO NOTHING`），且在取 `post_no` 之前 |
| 认领凭证头之前分配 `post_no`，或用序列分配 | `post_no` 出现缺口：回滚或重复的过账也占了号 | 锁住的期间行上的 `last_post_seq` 随凭证一起提交或回滚 |
| 把唯一索引或幂等约束建在 `finance_journal_entry_lines` 上 | 分区表上 PostgreSQL 直接拒绝，除非把期间放进键——那等于同一张源单可以在两个期间各过一次账 | 唯一性放在不分区的凭证头上 |
| `UPDATE` / `DELETE` 已过账的凭证 | 测试可能照样绿；账与报出去的数字对不上 | 过账即终态；用 `ReverseEntry` 纠正（每张只能一次） |
| 把 `CheckPeriodOpen` 当成"可以写" | 关账那一刻的窗口里漏过一张凭证 | 它只是咨询；算数的判定在 `postEntryTx` 里 |
| 用 `strconv.ParseFloat` 解析金额 | `"NaN"` 能过账；大额时差一分被当成平衡 | 用 `parseCents` / `NUMERIC` |
| 别的组件事件里的十进制值不过 `parseCents` 就写进 `NUMERIC` 列 | PostgreSQL 照存 `NaN`；之后每一次读到它的过账都失败，背后的销售订单事件被丢掉 | 进库前校验；不合法的额度按 `0`（未配置）存并记 Warn |
| 在任何地方把 schema 写成字面量 `"erp_finance"` | 这里能跑；换一个 `PG_SCHEMA` 事件就写进别的 outbox，永远发不出去 | `publish` 从 `current_schema()` 取 schema |
| 从 gRPC 或 `Start()` 调面向用户的 service 方法 | `besdk.ScopeOf` panic（gRPC 回 `INTERNAL`） | 只有 REST 请求带验过签的 Claims |
| 下一个会计年度没开 | 从 1 月 1 日起每一次过账（包括每一条销售订单事件）都以 `NotFound` 失败，事件被丢掉 | 期间与分录行分区只建到 FY2027（`008`）；在有"开会计年度"操作之前，每个新年度都是一份迁移 |
| 测试里用固定的 `aggregate_id` / `idempotency_key` | 第二次跑时被 inbox 跳过或被幂等重放，测试什么都没测 | 测试一律用 `uniqueID` 与 `UnixNano` |

## 改代码前自查

1. 这是会计（这里），还是引起它的业务单据（销售、库存、客户）？单据留在原处。
2. 我是不是在加依赖或同步调用？停下，见易错点。
3. 改动会过账吗？一律走 `postEntryTx`，已过账的凭证永不修改。
4. 碰到钱了吗？精确到分或用 `NUMERIC`，绝不用浮点；契约里的金额是十进制字符串。
5. 新的读写路径从 `allowedLegalEntityIDs` 取调用者的法人，并且有一条"第二个法人的数据看不到"的测试。
6. 契约变了？只增不改。`gen/` 有变化时契约包要打新 tag，`go.mod` 要 require 它。
7. 新规则先写失败的测试（真实数据库），再写代码；BRICKKIT.md 与 `docs/design.zh.md` 在同一个提交里更新。
