# 规范冻结清单

本目录是本项目编码与 review 的**唯一规范事实源**。冻结后 skill 库存更新不回改本目录；需更新时由用户显式决定。

## 冻结清单（冻结日期：2026-11-01）

| 文件 | 来源 | 版本/日期 | 许可证 | 适用范围 |
|---|---|---|---|---|
| `common.md` | letwork skill 库存 `references/standards/common.md`（语言无关通用 checklist，规则 ID 前缀 `C-`） | skill 内置版本，2026-11-01 复制冻结 | skill 随附 | 全部代码与提交（安全规则 C-SEC 优先级最高，不可被语言分册覆盖） |
| `go-uber.md` | [Uber Go Style Guide](https://github.com/uber-go/guide/blob/master/style.md)，叠加 [Effective Go](https://go.dev/doc/effective_go) 与 [CodeReviewComments](https://github.com/golang/go/wiki/CodeReviewComments)（规则 ID 前缀 `G-`，索引式精简） | 在线原文抓取日 2026-11-01 | MIT（Uber Go Guide）；Go 官方文档以 CC BY 许可 | 全部 Go 代码：`cmd/`、`internal/`、`test/e2e/` 中的 Go 文件 |

## 使用约定

- 冲突时语言分册（go-uber.md）优先于 common.md，但 C-SEC 安全规则不可被覆盖。
- 机械化门禁（每个 Go 编码任务提交前必跑）：`gofmt -l .`（必须空输出）、`go vet ./...`、`go build ./...`；测试相关任务另跑 `go test ./... -race`。
- review 报告结论必须引用规则 ID（如 G-ERR-02、C-SEC-01）。
- 规范更新：由用户显式提出，更新后在本文件追加新行并在 journal 记录 DECISION。
