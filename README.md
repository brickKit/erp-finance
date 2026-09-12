# erp-finance · 财务管理

会计科目表、总账分录、应收/应付台账、会计日历与期间锁、对账——设计书 §2.6 称本组件为**事件汇枢纽**：别人发生的事，在这里变成会计语言。

## 它能做什么
- 消费 `sales.order.created.v1`（`erp-sales`）与 `erp.inventory.adjusted.v1`（`erp-inventory`），幂等生成对应的会计凭证
- 会计期间的三态管理（`OPEN`/`CLOSED`/`LOCKED`）与跨组件期间锁咨询入口（`CheckPeriodOpen`）
- 手工凭证（`PostManualEntry`）与红字冲销（`ReverseEntry`，冲销不是删除）
- 客户已用额度（`credit_exposure`）的维护与查询，超限时发 `finance.credit.rejected.v1` 反向通知 `erp-sales`

## 需要哪些基础资源
| 资源 | 形态 | 为什么需要 | 怎么起 |
|---|---|---|---|
| PostgreSQL 16 | **A**（brickKit 基础资源，`kind: database`） | 数据持久化，独占 schema `erp_finance` | 装配仓库根目录 `make up` |
| NATS 2.10 | **A**（`kind: mq`） | 消费 `sales.order.created.v1`/`erp.inventory.adjusted.v1`/`mdm.customer.*`；发布 `finance.voucher.posted.v1`/`finance.credit.rejected.v1` | 同上 |

⚠️ 形态 A / B / C 的区别见设计书 §2.7.0。本组件**不需要** Traefik 与 Casdoor
就能单独跑起来——它不对 IAM 建依赖边，JWT 走本地验签（决策 87）。

## 怎么起来

```bash
# 装配仓库根目录
make up
cd components/erp/finance
go build -o build/migrate ./backend/cmd/migrate
PG_SCHEMA=erp_finance DATABASE_HOST=localhost DATABASE_PORT=5432 \
  DATABASE_USER=postgres DATABASE_PASSWORD=<.env 里的 POSTGRES_PASSWORD> DATABASE_NAME=brickkit_db \
  ./build/migrate up
go run ./backend/cmd/server     # 单独跑：besdk.RunStandalone 读 component.yaml 的端口
```

或者用平台：`brickkit up`（装配仓库根目录，`components/erp/finance` 登记为 submodule 且在 `brickkit.yaml` 里之后）。也可以直接 `make seed`/`make db-reset`——自成一体的演示数据（人工凭证+期间三态生命周期），不依赖任何其他组件先起来。

## 怎么用

```bash
# 手工凭证过账（REST，人类操作；自动凭证走事件消费，不走这里）
curl -X POST -H 'Authorization: Bearer <应用 token>' -H 'Content-Type: application/json' \
  -d '{
    "idempotency_key": "manual-entry-demo-1",
    "legal_entity_id": "1",
    "lines": [
      {"account_id": "1001", "debit": "1000.00", "credit": "0.00", "memo": "示例借方"},
      {"account_id": "2001", "debit": "0.00", "credit": "1000.00", "memo": "示例贷方"}
    ],
    "memo": "示例手工凭证"
  }' \
  http://localhost:8087/erp/finance/entries

# 查已用信用额度
curl -H 'Authorization: Bearer <应用 token>' \
  'http://localhost:8087/erp/finance/credit-exposure/1'

# 关账（可逆；LockPeriod 才是不可逆终态）
curl -X POST -H 'Authorization: Bearer <应用 token>' \
  http://localhost:8087/erp/finance/periods/2026-09/close
```

## 配置项

| 配置键 | 默认值 | 说明 |
|---|---|---|
| `pgSchema` | `erp_finance` | 本组件的 PG schema |
| `otelBaseUrl` | `""` | 空 = Blackhole Exporter，零成本 |
| `iamJwksUrl` | `""` | JWT 本地验签的公钥来源，指向 `infra-iam-casdoor` |
| `authzBundleUrl` | `""` | 权限判定的 bundle 轮询地址，指向 `infra-authz` |

## 参考实现
| 项目 | 看的模块 | 借鉴了什么 | 许可证（已复核） | 用法 |
|---|---|---|---|---|
| Tryton 7.x | `modules/account/move.py` | 凭证只有 draft/posted 两态、cancel 写反向凭证而非改状态；`number` 与 `post_number` 分开 | GPL-3 | 借鉴逻辑 |
| Tryton 7.x | `modules/account/period.py`、`fiscalyear.py` | 期间三态 `open/close/locked`（close 可逆、locked 终态） | GPL-3 | 借鉴逻辑 |
| ERPNext v14+ | `accounts/doctype/payment_ledger_entry/` | 把应收应付从总账里拆出来物化——核销要撤销重记总账是拆分的动因 | GPL-3 | 借鉴逻辑 |
| Oracle Fusion（闭源） | `GL_PERIOD_STATUSES` 文档 | 期间状态是一等可查实体，每个子账各有各的状态 | 闭源 | 借鉴实际应用 |

**要避免它的什么**：ERPNext 的 `make_gl_entries` 不去重，重复过账要靠调用方自己先调反向接口——
本组件用一条唯一约束（`source_component, source_doc_type, source_doc_id, source_revision`）在
凭证头上兜底，`event_inbox` 防的是"同一条消息投两次"，唯一约束防的是"两条不同消息指向同一张源单"，
两层缺一不可。

**⚠️ 跨进程的幂等过账，三家参考系统都没有对应物**——它们是单体单事务。这条唯一约束是我们自己加的。

## 边界与禁令
- 本组件**不持有客户的信用额度值**——额度是 `mdm-customer` 的主数据，本组件只持有"已用额度"
  （`credit_exposure`），两者相减才是可用额度，分别归两个组件是刻意的
- 本组件**不同步调用**`erp-sales`/`erp-inventory`/`mdm-customer`——全部走消费事件，同步图里零出边
- `finance_journal_entries`（凭证头）**永不分区**，`finance_journal_entry_lines`（分录明细）按
  **会计期间**分区——全系统唯一不按 `created_at` 分区的表；过账后的凭证永不修改永不删除，
  要改只能记一笔红字冲销凭证
