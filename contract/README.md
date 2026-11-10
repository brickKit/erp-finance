# erp-finance 的契约

`erp/finance` 发布的契约包：系统面的 protobuf、事件载荷的 JSON Schema（含给来源组件的共享会计事实契约）、用户面的 OpenAPI。放在组件仓库的 `contract/` 子目录里单独发布（标签 `contract/v3.…`）。

依据：装配项目 [be-assembly-standard](https://github.com/brickKit/be-assembly-standard) 的协议 05（会计事实契约、认领模式、应收形成、核销、付款认领、供应商票据）、06（期间与迟到单据）、14（凭证号、金额、列表）；组件的 `docs/design.md`。

## 内容

| 位置 | 是什么 |
| --- | --- |
| `proto/be/erp/finance/v3/finance.proto` | `ClaimService`（付款认领：认领、续期、确认、释放、读取；到期未清项，给收付款）；`FactService.Read`（按事实身份读回增量事实） |
| `gen/go/be/erp/finance/v3/` | Go 代码，包 `financev3`；模块 `github.com/brickKit/erp-finance/contract/v3` |
| `python/` | Python 包 `contract-erp-finance`，模块 `be.erp.finance.v3` |
| `events/shared/` | **共享契约**：会计事实 `accounting_fact.v1`、资金事实 `fund_fact.v1`。来源组件（成本、收付款……）在自己的流上用自己的事件类型（`<领域>.<来源>.accounting_fact.v1`、`…fund_fact.v1`）发布这个形状；财务按装配清单订阅 |
| `events/` | 流 `erp.finance`：凭证已过账、应收已形成、已核销、应收结清、减少、重开、票据单价确认与冲回（增量事实）；期间（完整状态） |
| `openapi.yaml` | 用户面；每个路由的操作写在 `x-operation` |

## 要点

- **来源不写科目**：会计事实带过账规则键与“角色 → 金额”，财务按规则与法人的科目映射出分录。
- **认领只用可用余额**（应结 − 已核销 − 认领中）；正式化与冲回不是调用，而是财务收到带认领身份的资金事实时在核销的事务里完成。
- **数量与金额**是十进制字符串；交易币与本位币都记，汇率快照由来源带来，财务不临时取汇率。
