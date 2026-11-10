# internal/：业务模块

财务的业务，每个模块一个包。只给本仓库引用（Go 的 `internal`）：别的组件编译期就引用不了，跨组件只走契约（决定 0101）。

先按下表找到模块，再读那个目录的 `AGENTS.md`。

## 模块

| 包 | 一句话 | 依赖（本组件内） |
| --- | --- | --- |
| `supplierinvoices` | 供应商票据与三单匹配、应付、票据单价确认（SI01–SI05） | `core`、`ledger`、`masterdata`、`openitems`、`orgunits`、`periods` |
| `settlement` | 资金事实的过账与核销：分配、正式化与冲回、给信用的应收事实（ST02–ST05） | `core`、`claims`、`ledger`、`openitems`、`periods` |
| `claims` | 付款认领与到期未清项（CL01–CL09） | `core`、`masterdata`、`openitems` |
| `intake` | 收到的事实：收件去重、取证、过账工作项；已出账 → 应收（AR01–AR04）；会计事实过账（AF01、AF02）；资金事实交给 `settlement` | `core`、`ledger`、`masterdata`、`openitems`、`periods`、`settlement` |
| `manualitems` | 人工未清项的登记、批准与取消（MI01–MI03） | `core`、`ledger`、`masterdata`、`openitems`、`orgunits`、`periods` |
| `vouchers` | 手工凭证与它的生命周期（VC01–VC04） | `core`、`ledger`、`masterdata`、`periods` |
| `ledger` | 科目表、过账规则与映射、凭证的写入与过账（连续凭证号）、余额 | `core` |
| `periods` | 会计期间与它的生命周期（PD01、PD02） | `core` |
| `openitems` | 未清项：形成、查询；应收已形成的事实 | `core` |
| `facts` | 系统面 `FactService.Read`：按身份读回本组件的增量事实 | `core` |
| `orgunits` | 部门投影（协议 02） | `core`、`masterdata` |
| `masterdata`（共用） | 事务外取证：主数据组件、`erp/purchase` 的匹配依据 | `core` |
| `core`（共用） | 名字、资源与操作的定义、错误与校验、生命周期表、增量事实、`Base` | — |

分层（每个包只引用比它低的层，没有环）：

| 层 | 包 |
| --- | --- |
| 5 | `intake` |
| 4 | `settlement` |
| 3 | `claims`、`manualitems`、`supplierinvoices`、`vouchers`、`orgunits` |
| 2 | `ledger`、`periods`、`openitems`、`facts`、`masterdata` |
| 1 | `core` |

核对：`go list -f '{{.Imports}}' ./internal/<包>`。

## 共同的写法

- **每个模块的函数先收 `m *core.Base`**（组件的运行时：数据库、调用、事件、审计、日志），入口包的 `Module` 持有它、登记时适配；模块之间直接调对方导出的函数。
- **名字在 `core`**：资源、操作、维度、事件类型、依赖的 ID、错误原因都在 `core/names.go`、`core/rules.go`；资源定义（`core.Definitions()`）是全组件一张表。
- **生命周期是一张表**：付款认领、供应商票据、手工凭证、期间、人工未清项在哪个状态能做什么，只在各自模块的 `lifecycle.go`（`core.Table`，写法照 `erp/purchase`，SF30：以后进 SDK）；变状态只经该模块写明的写入点（各模块说明的“生命周期”一节列着）。
- **增量事实只经 `core.Fact`**：发事件与存进 `finance_fact` 在同一个事务。
- **事务外取证、事务内写**：上游调用都在 `masterdata`，在事务之前；写入、审计（`core.Change`）与发件表在同一个事务。

## 加一个模块

一个新的业务能力有自己的表、路由与规则时才建模块（拿不准时放进最近的模块，宁可少切）：建目录、写 `AGENTS.md` 与 `CLAUDE.md`（`@AGENTS.md`），把它加进上面的表与仓库根的模块表，在 `finance/module.go` 登记它的路由与工作。

## 测试

模块没有单独的单元测试（生命周期表除外）：业务规则由 `itest/` 的流程测试覆盖，每个模块的说明写了它在哪个文件里被测。
