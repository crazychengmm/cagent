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
- [ ] 3.2 实现 server Control 端口 mTLS 配置：强制客户端证书、校验证书链与吊销状态，无证书或已吊销连接被拒，附单测（→ Requirement: Mutual TLS enforcement）
- [ ] 3.3 实现证书临期自动续签：agent 侧续签窗口调度、server RenewCert 签发、本地证书原子替换；续签失败告警但不断当前连接（→ Requirement: Automatic certificate renewal）

## 4. Agent 身份与控制循环
- [ ] 4.1 实现 agent 身份模块：本地私钥与 CSR 生成、UUID 持久化到 /var/lib/cagent/identity、证书原子写入、状态目录 700 权限（→ Requirement: Certificate enrollment and persistent agent identity）
- [ ] 4.2 实现首次注册信任锚点：加载 --server-ca（PEM 或指纹 pin），无锚点直接失败且不提供 insecure 开关，锚点不匹配时 TLS 失败且不发送 token，附单测（→ Requirement: Server trust anchor at installation）
- [ ] 4.3 实现控制循环：mTLS 主动出站长连接、断线指数退避自动重连、单条 Control 流多路复用收发（→ Requirement: Outbound persistent connection and reconnect）
- [ ] 4.4 实现心跳与在线状态：agent 每 15s 上报 UUID/版本/标签全集；server 45s 超时标 offline、心跳恢复标 online 并刷新标签/版本，附状态判定单测（→ Requirement: Heartbeat and online status）

## 5. 远程任务执行
- [ ] 5.1 实现 shell executor：root 经 /bin/sh -c 执行、可指定工作目录与追加环境变量、独立进程组启动、超时/取消时杀掉整个进程组，附单测（→ Requirement: Shell execution model, Job cancellation）
- [ ] 5.2 实现 agent 本地 worker 池：默认并发 4（可配）、超出排队、取消信号传播到执行器（→ Requirement: Shell execution model）
- [ ] 5.3 实现 job 状态机与流式输出：PENDING/RUNNING/五终态合法流转（终态不可回退，附单测）；stdout/stderr 分块流式回传落库、总量 1MB 截断并置 truncated 标记（→ Requirement: Job terminal state machine, Streaming output with truncation）
- [ ] 5.4 实现 job 幂等去重：agent 缓存已完成 job 结果，重发同一 job_id 直接回传既有结果不重新执行，附单测（→ Requirement: Idempotent job delivery）
- [ ] 5.5 实现 server 标签调度：key=value 的 AND 选择器匹配（附单测）、每台匹配机创建独立 job、零匹配显式报错不静默成功（→ Requirement: Label-targeted asynchronous job dispatch）
- [ ] 5.6 实现离线排队与补投：在线立即下发、离线入队、agent 重连后 drain 补投、排队超 24h 扫描置 EXPIRED（→ Requirement: Offline queueing and redelivery）
- [ ] 5.7 实现执行审计：job 下发与结束写 audit_logs（操作者、时间、目标 agent、命令全文、终态、退出码、输出摘要），agent 本地同步保留执行留痕（→ Requirement: Execution audit trail）

## 6. Agent 自升级
- [ ] 6.1 实现发布物打包：manifest（版本号、SHA256）生成与 tar.gz（二进制+manifest）Makefile 构建目标（→ Requirement: Signed release packages）
- [ ] 6.2 实现 ed25519 签名与发布：release sign 管理端离线签名、release publish 上传（server 复算校验后落 releases 表/存储）、release list，附签名/验签单测（→ Requirement: Signed release packages）
- [ ] 6.3 实现 agent 下载与验真：下载后先 SHA256 后 ed25519 验签（内置公钥 + 可配置追加公钥），任一失败拒装、继续运行旧版、升级 job FAILED 并上报原因，附单测（→ Requirement: Signed release packages）
- [ ] 6.4 实现版本比较与防降级：语义化版本比较（附单测），默认拒绝低版本，仅显式 allow-downgrade 放行（→ Requirement: Downgrade rejection）
- [ ] 6.5 实现升级任务调度：upgrade 按标签选择器圈选创建升级 job，复用普通 job 的离线排队与 24h 过期规则（→ Requirement: Label-based gradual rollout）
- [ ] 6.6 实现升级 grace：升级任务到达后等待运行中 job 最多 2 分钟，宽限内完成则继续，超时升级 job FAILED 上报且绝不终止用户 job，附单测（→ Requirement: Grace for running jobs）
- [ ] 6.7 实现 guard 状态机纯逻辑：保留旧版→替换→60s 健康窗口（本地自检 + mTLS 心跳双条件）→成功提交/失败回滚决策，状态持久化 state 文件，总超时 10 分钟，附决策表单测（→ Requirement: Guarded restart with health-gated rollback）
- [ ] 6.8 实现 guard 系统集成：cagent agent --guard-mode 短生命周期进程编排 systemctl restart、新进程健康探测、回滚旧二进制重启、回滚事件上报 server（→ Requirement: Guarded restart with health-gated rollback）

## 7. 管理 CLI（cagent ctl）
- [ ] 7.1 实现 ctl 凭据加载：配置文件/环境变量读取管理地址与 admin token，缺失或错误时在发请求前失败并提示（→ Requirement: CLI authentication）
- [ ] 7.2 实现 token create --ttl（默认 168h，明文仅创建时输出一次）、token list（不含明文）、token revoke <id>（→ Requirement: Token management commands）
- [ ] 7.3 实现 agent list 与 agent show <id>：输出 UUID、标签、版本、在线状态、最后心跳时间（→ Requirement: Agent inventory commands）
- [ ] 7.4 实现 run --selector --cmd [--timeout]（打印 job 标识）、job list、job show <id>（状态/退出码/输出/truncated）、job cancel <id>（→ Requirement: Job operation commands）
- [ ] 7.5 实现 release sign/publish/list 与 upgrade --release --selector（默认不携带 allow-downgrade）（→ Requirement: Release and rollout commands）

## 8. 安装与 systemd
- [ ] 8.1 实现标准文件布局：路径常量（/usr/local/bin/cagent、/etc/cagent/config.yaml、/var/lib/cagent/ 700、releases/ 旧版副本目录）与首启目录初始化（→ Requirement: Standard filesystem layout）
- [ ] 8.2 编写 deploy/cagent.service：Restart=always、开机自启、stdout/stderr 进 journald；一并提供 cagent-server 单元模板（→ Requirement: systemd lifecycle integration）
- [ ] 8.3 编写 scripts/install.sh：必填 --server/--token/--server-ca 与可选标签；先做平台检测（systemd、glibc、发行版矩阵、amd64/arm64）不满足即退出；下载放置→写配置→注册→装 unit→enable+start，任一步失败回滚清理不留半安装（→ Requirement: One-command guided installation, Supported platform matrix）

## 9. 端到端验证
- [ ] 9.1 补齐单元测试并跑通全量门禁：gofmt 无输出、go vet、go test ./... 全绿（选择器、状态机、执行器超时/取消、幂等、版本比较、CA/token、grace、guard 决策表）
- [ ] 9.2 编写 test/e2e 框架（macOS 本地双进程）：临时数据目录起真 server（首启 CA/admin token）+ 真 agent，验证 mTLS 注册成功且 ctl agent list 显示 online
- [ ] 9.3 e2e 执行链路用例：run 回收输出与退出码、超过 1MB 输出截断标记、job cancel 终止进程组无残留、五终态流转可见
- [ ] 9.4 e2e 可靠性用例：杀连接后指数退避重连恢复 online、离线期间 job 重连补投、重复投递同一 job_id 不重复执行、排队 TTL 过期置 EXPIRED（用可配置短 TTL 验证）
- [ ] 9.5 e2e 升级用例：伪造下一版二进制走验签升级 SUCCEEDED；伪造启动崩溃版经 guard 自动回滚旧版；坏签名包 FAILED 且旧版存活；默认降级被拒、allow-downgrade 放行
- [ ] 9.6 docker(systemd) 验证：Ubuntu 22.04（amd64）容器内 install.sh 干净安装，服务 active 且 enabled，kill -9 后自动拉起，升级成功路径与崩溃回滚路径各跑一遍
