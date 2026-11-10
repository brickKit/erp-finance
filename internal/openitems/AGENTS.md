# openitems：未清项

应收、应付与客户的贷方余额：每个未清项记应结、已核销、认领中，可用余额 = 应结 − 已核销 − 认领中，未清余额 = 应结 − 已核销（协议 05）。没有“已结清”的状态列：未清余额为零就是已结清（读出来的 `state` 是 `settled`）；`state` 列只记人工未清项的审批（`pending_approval`、`cancelled`）或 `open`。

## 负责与不负责

- 负责：表 `open_item`（带 `department_path`，在 `core.DepartmentTables` 里）；形成未清项（`Form`，应收同事务发布 `erp.receivable.formed.v1`）；路由 `/api/finance/open-items`（列表）与 `/api/finance/open-items/:id`（读取）。
- 不负责：从哪个事实形成、怎样过账（`intake`）；认领与核销（P3e、P3f 起，各自的模块改 `pay_claimed`、`settled`）；人工未清项的登记与审批（P3f）。

## 文件

| 文件 | 内容 |
| --- | --- |
| `records.go` | `OpenItem`、`Source`、`Claim`、`Allocation`；读取的 SQL（`Cols`：可用余额、未清余额、读出的状态都在 SQL 里算）、`Scan`、`Get`、`Read` |
| `openitems.go` | `New` 与 `Form`；`GetOpenItem`；`ListOpenItems`（按法人、种类、伙伴、`open` 筛选，`open` 是存储的生成列 `is_open`；按来源单号搜索；新的在前） |

## 依赖与被依赖

- 依赖：`core`。
- 被依赖：`intake`（`Form`）；入口登记路由。

## 规则在哪

`docs/design.md` 的术语（未清项、可用余额）与“应收、应付与核销”（AR01）；数据范围：法人与部门两个维度，没有部门的未清项只给不受部门约束的人看（`core.DocFacts`，安全 07）。

## 测试

集成：`itest/receivables_integration_test.go`（`receivables`：AR01 的未清项与事实）。
