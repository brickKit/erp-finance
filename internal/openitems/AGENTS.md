# openitems：未清项

应收、应付与客户的贷方余额：每个未清项记应结、已核销、认领中，可用余额 = 应结 − 已核销 − 认领中，未清余额 = 应结 − 已核销（协议 05）。没有“已结清”的状态列：未清余额为零就是已结清（读出来的 `state` 是 `settled`）；`state` 列只记人工未清项的审批（`pending_approval`、`cancelled`）或 `open`。

## 负责与不负责

- 负责：表 `open_item`（带 `department_path`，在 `core.DepartmentTables` 里）；形成未清项（`Form`：`open` 的应收同事务发布 `erp.receivable.formed.v1`，即 `Formed`；人工未清项登记时是 `pending_approval`，带登记人、说明与对方科目，批准时由 `manualitems` 调 `Formed`）；路由 `/api/finance/open-items`（列表）与 `/api/finance/open-items/:id`（读取）。
- 不负责：从哪个事实形成、怎样过账（`intake`、`settlement`、`supplierinvoices`）；认领（`claims` 改 `pay_claimed`）与核销（`settlement` 改 `settled`、作废贷方余额）；人工未清项的登记与批准（`manualitems`，经这里的 `Form` 与 `Formed`）。读取的 SQL 读 `pay_claim` 与 `allocation` 列出认领与分配。

## 文件

| 文件 | 内容 |
| --- | --- |
| `records.go` | `OpenItem`、`Source`、`Claim`、`Allocation`；读取的 SQL（`Cols`：可用余额、未清余额、读出的状态、认领与分配都在 SQL 里算）、`Scan`、`Get`、`Read` |
| `openitems.go` | `New` 与 `Form`、`Formed`；`GetOpenItem`；`ListOpenItems`（按法人、种类、伙伴、`open` 筛选，`open` 是存储的生成列 `is_open`；按来源单号搜索；新的在前） |

## 依赖与被依赖

- 依赖：`core`。
- 被依赖：`intake`、`supplierinvoices`、`settlement`、`manualitems`（`Form`、`Formed`、`Get`）；`claims`（`Read`、`Get`）；入口登记路由。

## 规则在哪

`docs/design.md` 的术语（未清项、可用余额）与“应收、应付与核销”（AR01）；数据范围：法人与部门两个维度，没有部门的未清项只给不受部门约束的人看（`core.DocFacts`，安全 07）。

## 测试

集成：`itest/receivables_integration_test.go`（`receivables`：AR01 的未清项与事实）。
