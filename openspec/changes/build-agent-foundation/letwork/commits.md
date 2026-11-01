# Commits — build-agent-foundation

> 每次提交后立即登记。SHA 用 `git rev-parse --short HEAD`。

| 任务/步骤 | SHA | Subject | 主要文件 |
|---|---|---|---|
| step | f289666 | chore(openspec): 初始化 OpenSpec 工作流并冻结编码规范 | .claude/、openspec/config.yaml、openspec/standards/ |
| step | 7e5259f | docs(openspec): 新增 build-agent-foundation 提案规格与任务计划 | openspec/changes/build-agent-foundation/（不含代码） |
| 1.1 | 3d30f2a | build: 初始化 Go module 与静态二进制构建骨架 | go.mod、Makefile、.gitignore、cmd/cagent/、internal/version/ |
| 1.2 | 7a2b5ab | feat(proto): 定义注册与控制通道 gRPC 契约 | proto/cagent/v1/（2 proto + 4 pb.go）、proto/README.md、Makefile、go.mod/sum |
| 1.3 | b576780 | feat(config): 实现 agent/server YAML 配置加载与校验 | internal/config/、go.mod/sum（yaml.v3） |
