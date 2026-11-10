# facts：按身份读回本组件的增量事实

系统面 `FactService.Read`（任一组件可调）：漏收了本组件某个增量事实的组件，按事实身份读回它（协议 05 的共同规则）。每个事实由 `core.Fact` 在发布它的同一事务里存进表 `finance_fact`。

## 文件

| 文件 | 内容 |
| --- | --- |
| `facts.go` | `Read`：最多 500 个身份一次，按问的顺序答，不认识的类型为空 |

## 依赖与被依赖

- 依赖：`core`。
- 被依赖：入口（`finance/services.go`）。

## 测试

集成：`itest/receivables_integration_test.go`（`receivables` 的最后一段）。
