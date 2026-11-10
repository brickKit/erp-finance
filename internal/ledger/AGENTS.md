# ledger：科目表、过账规则与映射、凭证的写入与过账、余额

总账的底层：一个法人的科目表、规则键到科目的映射、凭证怎样写进表（借贷平衡、只记可记账的启用科目）与怎样过账（取连续的凭证号、发“凭证已过账”、审计）。手工凭证的审批流程不在这里（`vouchers`），期间开不开放也不在这里（`periods`）：调用方先问期间，再调 `Issue`。

## 负责与不负责

- 负责：表 `account`、`account_mapping`、`mapping_version`、`voucher`、`voucher_line`；路由 `/api/finance/accounts`（列表、新建、读取、修改）、`/api/finance/posting-rules`、`/api/finance/account-mappings/:legal_entity_id`（读、整份替换）、`/api/finance/balances`；凭证的写入（`Write`、`Rewrite`）与过账（`Issue`）；事件 `erp.voucher.posted.v1`（增量事实，存进 `finance_fact`）。
- 不负责：手工凭证的状态与审批（`vouchers`）；期间与过账日期（`periods`）；事实到角色金额的换算（P3c 起由接收事实的模块做，再调 `Resolve`）。

## 文件

| 文件 | 内容 |
| --- | --- |
| `rules.go` | 过账规则目录 `Rules`：规则键 → 角色 → 借或贷（`sales_invoice`、`supplier_invoice`、`receipt`、`payment`）；`RuleKeys` |
| `accounts.go` | `Account`；`Postable`（VC01：存在、末级、启用）；`CreateAccount`（上级必须是同法人的非末级科目，代码重复答 `CODE_TAKEN`）、`GetAccount`、`UpdateAccount`（按版本）、`ListAccounts`（按代码排序，可按代码与名称搜索） |
| `mapping.go` | `Entry`、`Mapping`；`GetMapping`；`PutMapping`（版本在同一条语句里定，条目必须是目录里的规则与角色，科目必须可记账）；`Resolve`（规则 + 角色金额 → 分录行）；`ListPostingRules` |
| `records.go` | `Voucher`、`Line`、`Source`；读取的 SQL（`Cols`、`Scan`、`Get`、`Read`） |
| `posting.go` | `Draft`；`Write`（零金额行跳过、至少两行、本位币与每种交易币分别平衡、科目可记账）、`Rewrite`（草稿整份换行）；`Issue`（VC03：`db.NextNumber` 按法人、`JV`、期间取号，号 `JV-<法人代码>-<YYYYMM>-<6 位>`，同事务发事实、审计带 `document_number`） |
| `balances.go` | `ListBalances`：截至某月每个科目的期初、借方、贷方、期末（本位币，借正贷负；已过账与已冲销的凭证都算，冲销是另一张凭证） |

## 依赖与被依赖

- 依赖：`core`。
- 被依赖：`vouchers`（`Write`、`Rewrite`、`Issue`、`Get`、`Read`、`Line`）；以后接收事实的模块（`Resolve`、`Write`、`Issue`）；入口登记路由。

## 规则在哪

`docs/design.md` 的“会计事实契约”（规则键与角色）与“手工凭证与期间”（VC01、VC03）；凭证号连续：装配项目协议 14 与 be-protocol P14.5（审计记下号，恢复后由工具把序列推过它）；金额：协议 14（加减与比较在 SQL 或 `apd` 里做，不用浮点）。

## 测试

集成：`itest/ledger_integration_test.go`（`chart`：科目表、过账规则、映射与它的版本）、`itest/vouchers_integration_test.go`（`vouchers`：VC01、VC03 与余额）。
