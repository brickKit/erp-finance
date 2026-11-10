# core：全组件共用的名字与写法（共用）

每个模块都引用、自己什么业务也不做的东西：名字、资源定义、错误原因与校验、审计与事件的写法。改这里会影响所有模块。

## 负责与不负责

- 负责：组件 ID、依赖的 ID、流与事件类型、资源与操作、维度（法人；未清项与票据还有部门）、参数、复制部门路径的表（`DepartmentTables`）（`names.go`）；资源定义 `Definitions()`（全组件一张表：列表能筛选的字段、维度的映射按资源定）；本组件的错误原因与校验、授权（`Check`）、审计（`Change`）、发件表（`Emit`）、金额乘法（`Times`）、“能力不支持”的路由（`Unsupported`）、单号（`Number`）、幂等的结果（`IDResult`、`ResultID`）、可选的请求体（`BindOptional`）（`rules.go`）；生命周期表的通用写法（`lifecycle.go`）；`Base`（`base.go`）。
- 不负责：任何业务规则。

## 文件

| 文件 | 内容 |
| --- | --- |
| `base.go` | `Base`：组件的运行时，每个模块函数的第一个参数 |
| `names.go` | 常量；`DepartmentTables`；`fields`、`scoped`、`Definitions()`（每种资源的维度与字段）。列表能排序、筛选的字段要在这里它的资源下声明，不声明 SDK 就答 `FIELD_NOT_QUERYABLE` |
| `rules.go` | `Err…`（本组件的原因）、表单的格式、`DocFacts`、`EntityFacts`、`Number`、`BindOptional`、`IDResult`、`ResultID`、`Check`、`Invalid`、`NotFound`、`Unsupported`、`Decimal`、`Change`、`Emit`、`Minus`、`Per`、`Fact`、`Times` |
| `lifecycle.go` | 生命周期表：`Effect`、`Decision`、`Table`（`Decide`）、`Refused`、造格的 `RefuseCell`、`IgnoreCell`、`ProceedCell`、`MoveCell`；三项核对给各模块的单元测试用：`TableProblems`（每格都定了）、`DocumentProblems`（与设计文档的表逐格相同）、`UntestedRules`（每条规则都有集成测试写明） |

## 依赖与被依赖

- 依赖：只有 SDK 与契约。
- 被依赖：全部模块与入口。

## 规则在哪

资源、操作、维度：安全 06、安全 07、安全 09a；事件与发件表：装配项目协议 02；审计：协议 03；金额：协议 14（不用浮点，乘法经 `Times`，比较与加减在 SQL 里）。

## 测试

入口的单元测试 `finance/contract_test.go`（清单、路由）；其余由全部流程测试间接覆盖。
