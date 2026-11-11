# supplierinvoices：供应商票据与三单匹配

供应商开来的票据：订单票据（每行是采购订单行，按 `erp/purchase` 的匹配依据匹配）或费用票据（每行是一个费用科目，经批准）；过账形成应付、凭证与“票据单价确认”（D50、协议 05 的供应商票据）。**票据的生命周期**只在这里。

## 负责与不负责

- 负责：表 `supplier_invoice`（带 `department_path`，在 `core.DepartmentTables` 里）、`supplier_invoice_line`、`invoiced_line`、`invoiced_movement`；路由 `/api/finance/supplier-invoices`（列表、登记、读取、修改）与 `match`、`approve-difference`、`post`、`cancel` 四个动作（`Action`）；事件 `erp.supplier_invoice.price_confirmed.v1`（增量事实）。
- 不负责：匹配依据从哪来（`masterdata.MatchBasis`，事务外）；凭证与取号（`ledger`）；过账日期（`periods`）；应付未清项（`openitems.Form`）；票据单价确认的冲回、供应商贷项与补差票（以后）。

## 生命周期（改票据规则先读这里）

`lifecycle.go` 是一张表（`core.Table`）：5 个状态（`draft`、`matched`、`mismatch`、`posted`、`cancelled`）× 5 个命令（修改、匹配、批准差异、过账、取消），附加条件 `GuardOrderInvoice`、`GuardExpenseInvoice`、`GuardExpenseApproved`、`GuardApproved`、`GuardNotMaker`（在 `actions.go` 的 `guard` 里检查），规则 SI01–SI05。

- 用法：命令先在事务外取证（`gather`：匹配与过账取依据，过账另取法人与小数位），再在事务里锁住票据、`Lifecycle.Decide`、按格行事。状态的写入只有三处：`record`（匹配的结果：`matched` 或 `mismatch`）、`setState`（取消）、`post` 末尾（`posted`）；修改回到 `draft` 在 `UpdateSupplierInvoice` 的那条更新里。
- 匹配是一张票据自己的证据，不审计；登记、修改、批准、过账、取消审计。
- 加一个状态或命令：表与 `docs/design.md` 的同一张表一起改（`lifecycle_test.go` 逐格比较），集成测试写上规则编号。

## 怎样算（SI02、SI03）

- 数量按订单行的单位。货物的可开票数量 = 已收（基本单位，净额）换算到订单单位（`toOrderUnit`，用依据里的 `ordered_quantity` 与 `ordered_base_quantity`）− 已过账开票的（`invoiced_line`）；服务的 = 已验收 − 已开票。不低于 0。
- 容差来自 `core.Base.ToleranceOf`（配置键 `MATCH_TOLERANCES`，启动时读一次）。
- 过账前同一订单行的过账按行排队（事务级的 advisory lock，按行 ID 排序取锁），已过账的开票数量因此准确；订单票据在锁里再匹配一次：`matched` 的不再匹配、或已批准差异的结果变了，就退回 `mismatch`、不过账。
- 批准的差异只对批准时的结果有效（SI04）：`record` 写下的结果与票据原有的不同（`changed`）时清掉批准。
- 货物行按依据里流水的先后取基本单位数量，每条不超过净收货减去已开票的（`invoiced_movement`）；取到流水的才发布“票据单价确认”，单价换算到每个基本单位（`toBase` 的反方向）。

## 文件

| 文件 | 内容 |
| --- | --- |
| `lifecycle.go` | 状态、命令、附加条件；表 `Lifecycle` |
| `lifecycle_test.go` | 单元测试：每一格都定了；设计文档的表与代码逐格相同；每条规则都有集成测试写明 |
| `records.go` | `Invoice`、`Line`、`Result`；读取的 SQL（`Cols`：合计在 SQL 里算）、`Scan`、`Get`、`Read` |
| `invoices.go` | 输入与事务外的取证（`prepare`：票据的种类、法人与本位币、伙伴、部门、行金额）；`CreateSupplierInvoice`（幂等，内部号 `SI`）、`GetSupplierInvoice`、`UpdateSupplierInvoice`、`ListSupplierInvoices`；`taken`（供应商票据号重复 → `CODE_TAKEN`） |
| `matching.go` | `invoicedOf`、`toOrderUnit`、`toBase`、`evaluate`（每行的结果）、`changed`（结果变了没有）、`record` |
| `actions.go` | `Action`、`gather`、`guard`、`match`、`setState` |
| `posting.go` | `post`、`voucher`（规则 `supplier_invoice`）、`invoiced`（开票数量、流水分配与票据单价确认） |

## 依赖与被依赖

- 依赖：`core`、`ledger`、`masterdata`、`openitems`、`orgunits`、`periods`。
- 被依赖：入口登记路由。

## 规则在哪

`docs/design.md` 的“供应商票据”（表与 SI01–SI05）与未决问题（容差的配置、只收本位币、一张票据只有一种行，都是 AI 定、待复核）；装配项目协议 05 的“供应商票据与应付”、D50。

## 测试

- 单元：`lifecycle_test.go`。
- 集成：`itest/supplierinvoices_integration_test.go`（`supplierInvoices`：SI01–SI05）。
