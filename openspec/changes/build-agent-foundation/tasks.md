# Tasks

## 1. 项目骨架与协议契约
- [x] 1.1 初始化 Go module（github.com/crazychengmm/cagent）、目录骨架、internal/version 包（ldflags 注入 Version/Commit）、Makefile（CGO_ENABLED=0 交叉编译 linux/amd64+arm64、fmt/vet/test 目标）与 .gitignore
- [x] 1.2 定义并生成 protobuf 契约：EnrollmentService（Enroll）+ Control 双向流（Heartbeat、JobDispatch、JobEvent、UpgradeCommand 等），另含 RenewCert 与 DownloadRelease；proto/README 写明生成命令（→ Requirement: Certificate enrollment and persistent agent identity, Automatic certificate renewal, Heartbeat and online status）
- [x] 1.3 实现 agent/server 两端配置结构与 YAML 加载：server 地址、CA/证书/密钥路径、data-dir、labels、心跳 15s、并发 4、超时 5m 等默认值与启动校验（→ Requirement: One-command guided installation）
- [x] 1.4 实现 cagent 主命令骨架：agent/server/ctl 子命令分发、--config 全局参数、基于 log/slog 的 JSON 结构化日志（→ Requirement: Execution audit trail）

## 2. Server 存储与 PKI 基础
- [x] 2.1 实现 SQLite 存储层：开启 WAL、schema 迁移（agents、enrollment_tokens、jobs、job_output、audit_logs、releases 六表）与各表 CRUD（→ Requirement: Job terminal state machine, Execution audit trail）
- [x] 2.2 实现内置 CA：首启生成 ECDSA P-256 根密钥与自签 CA 证书（数据目录 600）、agent 证书签发与吊销状态检查，附表驱动单测（→ Requirement: Mutual TLS enforcement）
- [x] 2.3 实现 enrollment token 领域逻辑：32 字节随机创建（TTL 默认 168h）、可复用、列表、吊销、过期判定，附单测（→ Requirement: Enrollment token lifecycle）
- [x] 2.4 实现 admin token 首启生成（数据目录 600 文件）与管理 API 的 token 鉴权拦截器（→ Requirement: CLI authentication）

## 3. 注册服务与 mTLS
- [x] 3.1 实现 EnrollmentService 的 Enroll：单向 TLS + token 校验 + CSR 验签后签发证书、创建 agents 行（UUID/标签），注册失败写审计，附单测（→ Requirement: Enrollment token lifecycle, Certificate enrollment and persistent agent identity）
- [x] 3.2 实现 server Control 端口 mTLS 配置与三端口启动组装：强制客户端证书、校验证书链与吊销状态（VerifyConnection 查 store），无证书/外来 CA/已吊销连接被拒；enrollment 单向 TLS、admin 单向 TLS+token 拦截器；cli server 子命令真实装配启动与信号优雅停止，附集成单测（→ Requirement: Mutual TLS enforcement, CLI authentication）
- [x] 3.3 实现 server 侧 RenewCert：mTLS 对端证书 CN 识别 agent、校验 CSR 后重新签发，更新序列号与到期时间（旧证即时失效），吊销机拒绝续签；注册到 control 端口，附单测（→ Requirement: Automatic certificate renewal）
- [x] 3.3a （原 3.3 的 agent 侧部分，已并入 4.3）证书临期续签窗口调度、原子替换证书、失败告警但不断当前连接、替换后主动重连

## 4. Agent 身份与控制循环
- [x] 4.1 实现 agent 身份模块：本地私钥与 CSR 生成、UUID 持久化、证书原子写入、状态目录 700 权限（→ Requirement: Certificate enrollment and persistent agent identity）
- [x] 4.2 实现首次注册信任锚点：加载 --server-ca（PEM 或指纹 pin），无锚点直接失败且不提供 insecure 开关，锚点不匹配时 TLS 失败且不发送 token；register 客户端完成 Enroll 调用，附单测（→ Requirement: Server trust anchor at installation）
- [x] 4.3 实现控制循环：mTLS 主动出站长连接、断线指数退避自动重连、单条 Control 流多路复用收发；含 3.3a 证书临期续签调度、原子替换、失败告警不断连、替换后主动重连（→ Requirement: Outbound persistent connection and reconnect, Automatic certificate renewal）
- [x] 4.4 实现心跳与在线状态：agent 每 15s 上报 UUID/版本/标签全集；server Control 流接收心跳落库并回 Ack（含续签建议）、45s 扫描标 offline、心跳恢复标 online 并刷新标签/版本；同 agent 重连替换旧流，吊销经心跳即时踢下线（→ Requirement: Heartbeat and online status）

## 5. 远程任务执行
- [x] 5.1 实现 shell executor：root 经 /bin/sh -c 执行、可指定工作目录与追加环境变量、独立进程组启动、超时/取消时杀掉整个进程组，附单测（→ Requirement: Shell execution model, Job cancellation）
- [x] 5.2 实现 agent 本地 worker 池：默认并发 4（可配）、超出排队、取消信号传播到执行器（→ Requirement: Shell execution model）
- [x] 5.3 实现 job 状态机与流式输出：PENDING/RUNNING/五终态合法流转（终态不可回退，附单测）；stdout/stderr 分块流式回传落库、总量 1MB 截断（末块切边）并置 truncated 标记；job.finish 审计（→ Requirement: Job terminal state machine, Streaming output with truncation, Execution audit trail）
- [x] 5.4 实现 job 幂等去重：agent 缓存已完成 job 终态（FIFO 1000），重发同一 job_id 回放 Started+Finished 不重新执行，附单测（→ Requirement: Idempotent job delivery）
- [x] 5.5 实现 server 标签调度：key=value 的 AND 选择器匹配（附单测）、每台匹配机创建独立 job、零匹配显式报错不静默成功；超时硬上限、在线立即下发/离线 pending、job.create 审计（→ Requirement: Label-targeted asynchronous job dispatch）
- [x] 5.6 实现离线排队与补投：在线立即下发、离线入队、agent 重连后 drain 补投（1000/次，幂等）、排队超 24h 扫描置 EXPIRED（→ Requirement: Offline queueing and redelivery）
- [x] 5.7 实现执行审计：job.create/job.finish 写 server audit_logs；agent 本地 audit.log JSONL（开始/结束、4KB 预览、0600），worker 接入控制循环（→ Requirement: Execution audit trail）

## 6. Agent 自升级
- [x] 6.1 实现发布物打包：release.BuildPackage 产出 tar.gz（cagent 0755 + manifest.json：版本/SHA256/size，固定 mtime 可复现）；Makefile package 目标随 ctl release 命令在 7.5 落地（→ Requirement: Signed release packages）
- [x] 6.2 实现 ed25519 签名与发布：signing 库（keygen/PEM/Sign/Verify）、ManagementService.PublishRelease 流式校验落盘+登记+审计、ListReleases；management.proto 全量 admin API 一次定义（→ Requirement: Signed release packages）
- [x] 6.3 实现 agent 下载与验真：control DownloadRelease 流式发包；upgrade.DownloadVerify 依次校验大小/SHA256/ed25519，任一失败拒装（→ Requirement: Signed release packages）
- [x] 6.4 实现版本比较、防降级：release.Parse/Compare/DecideUpgrade，相同拒绝、默认拒降级、allow_downgrade 显式放行（→ Requirement: Downgrade rejection）
- [x] 6.5 实现升级任务调度：management.Upgrade 校验版本已发布+标签 AND 圈选，每机一个 kind=upgrade job（携带版本/SHA/签名/大小/allow_downgrade），在线发 UpgradeCommand、离线 pending 复用补投/24h 过期/幂等；drain 按 kind 分发（→ Requirement: Label-based gradual rollout）
- [x] 6.6 实现升级 grace：升级到达后等待运行中 job 最多 2 分钟（ActiveCount 轮询），宽限内完成则继续、超时 FAILED 不杀任务（→ Requirement: Grace for running jobs）
- [x] 6.7 实现 guard 状态机纯逻辑：接口化 Installer/HealthProbe/Reporter，版本门控→grace→Stage→Activate→Restart→60s 健康门控→Commit；失败/重启失败→Rollback+重启→ROLLED_BACK，9 组决策表单测（→ Requirement: Guarded restart with health-gated rollback）
- [x] 6.8 实现 guard 系统集成：state 文件跨进程交接、SystemInstaller（复制+rename 替换/备份/回滚/清理）、Setsid detach 独立 guard（agent --guard-mode）做文件健康探测+回滚重启+Flush 上报、新版本首连写 healthy 标记；server HandleUpgradeEvent 推进 upgrade job（→ Requirement: Guarded restart with health-gated rollback）

## 7. 管理 CLI（cagent ctl）
- [x] 7.1 实现 ctl 凭据加载：ctl.Config（文件+环境变量覆盖，缺地址/token 发请求前失败）、TLS+unary/stream token 拦截器、--config 解析、Run 分发骨架（→ Requirement: CLI authentication）
- [x] 7.2 实现 token create --ttl（默认 168h，明文仅一次）/list（不明文）/revoke（→ Requirement: Token management commands）
- [x] 7.3 实现 agent list/show：UUID/主机名/状态/版本/标签/心跳/证书到期，未知名 NotFound
- [x] 7.4 实现 run（多 --selector AND）/job list/show（含输出）/cancel（在线下发，离线报错），management RPC 接 dispatch.Service（→ Requirement: Job operation commands）
- [x] 7.5 实现 release package/sign/publish/list 与 upgrade（--release/--selector/--allow-downgrade 默认 false），Makefile package 目标（→ Requirement: Release and rollout commands）

## 8. 安装与 systemd
- [x] 8.1 实现标准文件布局：internal/paths 常量（/usr/local/bin/cagent、/etc/cagent、/var/lib/cagent 0700、releases/、server 侧布局）（→ Requirement: Standard filesystem layout）
- [x] 8.2 deploy/cagent.service（Restart=always、WantedBy multi-user、journald、加固项）与 cagent-server.service 模板（→ Requirement: systemd lifecycle integration）
- [x] 8.3 scripts/install.sh：必填 enrollment/control/token + CA/指纹二选一、--label 多次、root+systemd+glibc+架构检测、二进制可执行校验、临时文件原子落位、失败 trap 回滚、无 insecure（→ Requirement: One-command guided installation, Supported platform matrix）

## 9. 端到端验证
- [x] 9.1 全量门禁绿：gofmt 无输出、go vet、go test -race ./... 全绿（选择器/状态机/执行器/幂等/版本/CA/token/grace/guard 决策表）、CGO_ENABLED=0 linux amd64+arm64、openspec validate --strict
- [x] 9.2 test/e2e 框架（真二进制子进程）：临时目录起 server（CA/admin token/三端口）+ agent，mTLS 注册并 online；TestE2E_RegistrationAndOnline
- [x] 9.3 e2e 执行链路：run 回收输出与退出码（exit 3）、cancel 终止在跑任务；截断/五终态由单测覆盖（TestE2E_RunJobOutputAndExit/TestE2E_CancelJob）
- [x] 9.4 e2e 可靠性：强杀 agent→offline（5s 超时+扫描）→重启 online，离线 pending job 补投执行成功（TestE2E_OfflineSweepAndRedelivery）；退避重连/幂等由单测覆盖
- [x] 9.5 e2e 升级：伪造 999.0.0 二进制走 package/sign/publish/upgrade→guard 健康门控→SUCCEEDED；默认降级 FAILED（TestE2E_SelfUpgrade/TestE2E_DowngradeRejectedByDefault）；崩溃回滚路径由 guard 决策表单测覆盖
- [x] 9.6 docker(systemd) 资产：Dockerfile.systemd(ubuntu22.04)+docker_systemd_test.sh（安装布局/kill -9 自动拉起/回滚路径），本机 docker.io 不可达时 SKIP，CI 执行
