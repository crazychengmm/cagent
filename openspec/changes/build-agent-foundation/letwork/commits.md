# Commits — build-agent-foundation

> 每次提交后立即登记。SHA 用 `git rev-parse --short HEAD`。

| 任务/步骤 | SHA | Subject | 主要文件 |
|---|---|---|---|
| step | f289666 | chore(openspec): 初始化 OpenSpec 工作流并冻结编码规范 | .claude/、openspec/config.yaml、openspec/standards/ |
| step | 7e5259f | docs(openspec): 新增 build-agent-foundation 提案规格与任务计划 | openspec/changes/build-agent-foundation/（不含代码） |
| 1.1 | 3d30f2a | build: 初始化 Go module 与静态二进制构建骨架 | go.mod、Makefile、.gitignore、cmd/cagent/、internal/version/ |
| 1.2 | 7a2b5ab | feat(proto): 定义注册与控制通道 gRPC 契约 | proto/cagent/v1/（2 proto + 4 pb.go）、proto/README.md、Makefile、go.mod/sum |
| 1.3 | b576780 | feat(config): 实现 agent/server YAML 配置与校验 | internal/config/、go.mod/sum（yaml.v3） |
| 1.4 | 7103fe8 | feat(cli): 接入子命令分发与 slog 结构化日志 | cmd/cagent、internal/cli、internal/logging |
| 2.1 | 8991f3a | feat(store): SQLite WAL 六表迁移与 CRUD | internal/server/store/、go.mod/sum（modernc.org/sqlite） |
| 2.2 | a369230 | feat(ca): 内置 ECDSA P-256 CA 与证书吊销状态检查 | internal/server/ca/、store schema/agents |
| 2.3 | 8d2b201 | feat(enrollment): enrollment token 签发与校验领域服务 | internal/server/enrollment/ |
| 2.4 | ac89189 | feat(admin): 首启 admin token 与管理 API 鉴权拦截器 | internal/server/admin/ |
| 3.1 | 1e39064 | feat(enroll): 实现 EnrollmentService.Enroll 注册受理 | internal/server/enrollservice/、ca |
| 3.2 | e58e515 | feat(server): mTLS 控制端口与三端口启动组装 | internal/server/{tlsconfig,serve,ca}、cli、config |
| 3.3 | 74b0594 | feat(control): 实现 mTLS 证书续签 RenewCert | internal/server/controlservice/、store、serve |
| 4.1 | eef5652 | feat(agent): 本地身份模块 | internal/agent/identity/ |
| 4.2 | 49c75ff | feat(agent): 注册信任锚点与 Enroll 客户端 | internal/agent/{trust,register}、config |
| 4.3+3.3a | 05acaf6 | feat(agent): mTLS 控制循环、退避重连与自动续签 | internal/agent/control、cli |
| 4.4 | fc4ffe0 | feat(control): 心跳落库、在线状态与离线扫描 | internal/server/controlservice、serve |
| 5.1 | 83bef40 | feat(agent): shell 执行器（进程组、超时/取消杀组、输出事件流） | internal/agent/executor |
| 5.2 | 5e86a03 | feat(agent): 本地 worker 池与输出管道竞态修复 | internal/agent/jobworker、executor |
| 5.3 | bfb00f1 | feat(server): job 事件状态机与流式输出 1MB 截断 | internal/server/controlservice |
| 5.4 | 2042f64 | feat(agent): job 终态结果缓存与重发幂等 | internal/agent/jobworker |
| 5.5 | 6b528d6 | feat(server): 标签 AND 选择器调度与在线下发 | internal/server/dispatch |
| 5.6 | 0836d47 | feat(server): 重连补投与 24h 排队过期扫描 | internal/server/controlservice、store、serve |
| 5.7 | 3e029ac | feat(agent): 本地执行审计 JSONL 与 worker 接线控制循环 | internal/agent/audit、jobworker、cli |
| 6.1 | 6cb7c6d | feat(release): tar.gz 发布包与 manifest 打包 | internal/release |
| 6.2 | 0a1002b | feat(release): ed25519 签名库、管理面 proto 与流式发布 | internal/release、management、proto |
| 6.3 | 616ebfd | feat(upgrade): 发布包流式下载与三重验真 | internal/agent/upgrade、controlservice |
| 6.4 | 33a7736 | feat(release): 语义版本比较与防降级决策 | internal/release |
| 6.5 | 39377ba | feat(server): 升级任务调度与按类型补投 | internal/server、jobs schema |
| 6.6 | ce3ff2f | feat(agent): 升级 grace 等待 | internal/agent/{jobworker,upgrade} |
| 6.7 | 97c604f | feat(upgrade): guard 健康门控状态机 | internal/agent/upgrade |
| 6.8 | f127344 | feat(upgrade): guard 系统集成与 detach | internal/agent/upgrade、cli |
| 7.1-7.5 | 110ee6c | feat(ctl): 管理 CLI 全套命令与 management RPC | internal/ctl、management、Makefile |
