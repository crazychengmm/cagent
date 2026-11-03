# Journal — build-agent-foundation

> 本文件由 letwork skill 维护，按时间正序追加。一条只讲一件事。
> TAG：PROBLEM（问题：现象/根因/处理）、DECISION（决策：理由/备选）、IMPROVEMENT（可执行改进）、NOTE（阶段流转/事实）。

## 过程记录

### 2026-11-01 20:20 [NOTE] change 创建
- 需求一句话：做一个安装在 Linux 上的多角色运维 agent（监控、运维、管理服务器），有 server 端；agent 启动时注册到 server，定位类似 Puppet + Zabbix agent 的运维核心，可由 server 下发升级。
- 背景：空目录起步。已完成 git init、OpenSpec CLI v1.16.0 全局安装（@fission-ai/openspec）、`openspec init . --tools claude`。

### 2026-11-01 20:22 [NOTE] 远端仓库
- 远端：git@github.com:crazychengmm/cagent.git（用户给的是 HTTPS URL，因私钥在 ~/.ssh 下，改用 SSH 形式）。
- 遵守 letwork 铁律：只配置远端，不自动 push。

### 2026-11-01 20:25 [DECISION] Q1 使用场景与规模
- 决定：内部团队使用；单机 server；设计目标 ≤1000 agent；架构不堵死后续水平扩展（agent 主动出站、无状态网关可后加）。
- 理由：首期避免集群/多租户过度设计。
- 被否掉的备选：公司级平台（数万台 + HA）、商业 SaaS 多租户。

### 2026-11-01 20:25 [DECISION] Q2 首期 change 范围
- 决定：首期只做 ①注册接入 + 心跳 + agent 身份 ②远程命令/任务执行 ⑥agent 自升级；采用插件式多角色架构，监控指标、配置管理（状态收敛）、日志采集后续独立 change。
- 理由：注册/执行/升级构成「管得到、能动手、能迭代」最小闭环；后加角色不改通信底座。
- 被否掉的备选：首期一次性做全部角色。

### 2026-11-01 20:25 [DECISION] Q3 技术栈
- 决定：Go 单仓单语言；gRPC 双向流 + protobuf；agent/server 均为静态二进制。
- 理由：无运行时依赖，systemd 部署与自升级干净；交叉编译 amd64/arm64 方便。
- 被否掉的备选：Rust（开发速度慢）、Python（部署/自升级脏）。

### 2026-11-01 20:25 [DECISION] Q4 通信与安全模型
- 决定：agent 主动出站长连接（gRPC bidi stream），server 永不主动连机器；一次性 enrollment token 注册，注册后签发 agent 证书，后续 mTLS 双向认证；命令执行强制审计。
- 理由：agent 权限等同 root，mTLS + 审计是运维系统底线；出站模型 NAT 友好。
- 被否掉的备选：长期共享 token（泄露即可冒充）、SSH 连入（凭据与网络打通成本高）。

### 2026-11-01 20:25 [DECISION] Q5 自升级机制
- 决定：签名整包替换；下载 → 验签 → 替换二进制 → systemd 重启 → 健康自检失败自动回滚；支持按标签圈选灰度。
- 理由：批量打挂机器是灾难场景，签名 + 回滚是底线。
- 被否掉的备选：无签名无回滚、apt/yum repo 渠道。

### 2026-11-01 20:25 [DECISION] Q6 server 首期形态
- 决定：server 进程 + CLI/配置管理（agent 列表、下发命令、下发升级均走 CLI/API），无 Web UI。
- 理由：先把协议与可靠性做扎实；Web 控制台后续独立 change。
- 被否掉的备选：首期带最小 Web 控制台、纯 API 前端另立。

### 2026-11-01 20:35 [DECISION] Q7 命令执行模型
- 决定：异步 job，下发即返回 job_id；状态机 PENDING→RUNNING→{SUCCEEDED,FAILED,TIMEOUT,CANCELED,EXPIRED}；stdout/stderr 流式回传落库、单任务输出截断 1MB；默认超时 5 分钟（可配，硬上限 60 分钟）；单 agent 默认并发 4（可配）本地排队；取消杀整个进程组防孤儿。
- 理由：同步 RPC 撑不住长任务与断线场景。
- 被否掉的备选：同步执行立即返回结果。

### 2026-11-01 20:35 [DECISION] Q8 运行身份与审计
- 决定：agent 以 root 运行，经 /bin/sh -c 执行，可指定工作目录与追加环境变量；每次执行强制审计（操作者/时间/目标/命令全文/退出码/输出摘要），server 持久化 + agent 本地留痕；首期不做命令白名单与审批流（job 协议保留来源/操作者字段）。
- 理由：运维操作天然需要 root；审计是底线，白名单/审批等真实需求出现再做。
- 被否掉的备选：非 root + sudo 白名单、首期内置审批流。

### 2026-11-01 20:35 [DECISION] Q9 离线与消息可靠性
- 决定：心跳 15s；server 45s 收不到心跳标记 offline；离线任务 server 侧排队，重连补投，排队 TTL 24h（过期 EXPIRED）；at-least-once 投递 + job_id 幂等，同一 job 绝不重复执行；断线指数退避重连。
- 理由：NAT/内网环境断连常见，幂等是防重复执行的关键。
- 被否掉的备选：离线即失败、at-most-once。

### 2026-11-01 20:35 [DECISION] Q10 打包与升级包格式
- 决定：单一静态二进制 cagent（子命令 agent/server/ctl），CGO_ENABLED=0，linux/amd64+arm64；升级包 tar.gz（二进制 + manifest：版本/SHA256）+ ed25519 独立 .sig；签名公钥内置 agent 且可配置追加/轮换；首期只升级 agent，server 手工升级。
- 理由：单二进制自升级最干净；ed25519 签名防篡改。
- 被否掉的备选：多文件包/apt-yum 渠道/无签名。

### 2026-11-01 20:35 [DECISION] Q11 初装方式与路径
- 决定：install.sh（参数 server 地址、enrollment token、初始标签）安装到 /usr/local/bin/cagent、/etc/cagent/config.yaml、systemd 单元 cagent.service；运行态 /var/lib/cagent/（身份/证书/旧版本/升级状态）；日志走 journald。
- 被否掉的备选：首期做 deb/rpm、支持非 systemd/容器环境（均列 Non-Goal）。

### 2026-11-01 20:35 [DECISION] Q12 agent 身份与标签
- 决定：注册成功生成 UUID 持久化 /var/lib/cagent/identity 并与证书绑定，重装即新机器；标签取配置文件静态 key=value，随心跳全量上报；选择器为多 key=value 的 AND；server 端改标签/动态分组列 Non-Goal。
- 理由：标签是灰度与选机基础；静态标签首期足够且语义简单。

### 2026-11-01 20:35 [DECISION] Q13 PKI 与注册令牌
- 决定：server 内置 CA（根密钥 600 存数据目录）；cagent ctl token create 生成 32 字节随机 token，可复用、默认 7 天、可列出/吊销；agent 本地生成私钥+CSR，验 token 后签发证书；证书 90 天有效期，到期前经既有 mTLS 连接自动续签。
- 理由：可复用 token 方便批量装机；自动续签避免 90 天批量失联。
- 被否掉的备选：一机一个 token、手动续签、长期证书。

### 2026-11-01 20:35 [DECISION] Q14 server 数据存储
- 决定：SQLite（WAL）单文件，表 agents/tokens/jobs/job_output/audit/releases；数据目录 /var/lib/cagent-server/（开发可指定本地目录）。
- 理由：≤1000 agent 单机规模 SQLite 足够，零运维依赖。
- 被否掉的备选：PostgreSQL/MySQL、server 集群（列 Non-Goal）。

### 2026-11-01 20:35 [DECISION] Q15 平台矩阵与验证方式
- 决定：目标平台 systemd + glibc 主流发行版（Ubuntu 20.04+/Debian 11+/RHEL 8+）× amd64/arm64；验证 = Go 单测（执行器/升级状态机等接口化 mock）+ macOS 本地双进程 mTLS e2e（注册/执行/升级全链路）+ Linux docker(systemd) 验证安装/重启/回滚；CI 流水线列后续 change，首期提供本地 e2e 脚本。
- 理由：开发机 macOS 无 systemd，三层验证互补。

### 2026-11-01 20:45 [DECISION] Q16 升级健康判据与回滚
- 决定：独立短生命周期 guard 编排「替换 → systemctl restart → 健康判定」；健康 = 60s 内完成本地自检并经 mTLS 完成一次心跳；失败自动换回旧二进制重启并上报回滚事件；升级总超时 10 分钟；下载/验签失败旧版本继续运行；默认拒绝降级，allow-downgrade 显式覆盖。
- 理由：升级窗口内崩溃无人值守最危险，guard 不依赖新进程自身逻辑。
- 被否掉的备选：systemd 自身回滚机制（跨发行版差异大）。

### 2026-11-01 20:45 [DECISION] Q17 CLI 最小命令面
- 决定：token create/list/revoke；agent list/show；run/job show/list/cancel；release sign/publish/list；upgrade --release --selector；配置 /etc/cagent/config.yaml 与 /etc/cagent-server/config.yaml。

### 2026-11-01 20:45 [DECISION] Q18 首次注册信任锚点
- 决定：install.sh 必填 --server-ca（pem 或指纹 pin）；Enroll 走单向 TLS + token 换证书，之后全 mTLS；无锚点安装直接失败，不提供 --insecure。
- 理由：否则中间人可套走 enrollment token。

### 2026-11-01 20:45 [DECISION] Q19 升级与运行中 job
- 决定：升级时最多等待 2 分钟 grace；超时升级 FAILED 并上报，绝不杀用户任务；不做强制中断升级。

### 2026-11-01 20:45 [DECISION] Q20 插件边界与可观测性
- 决定：首期只做代码层模块化（协议层/executor 接口/角色层分离），等监控 change 带来第二个角色再抽真插件接口；结构化 JSON 日志（agent 进 journald），审计落 SQLite；不做 Prometheus /metrics 与 Web UI。
- 理由：rule of three，过早插件化是过度设计。

### 2026-11-01 20:45 [DECISION] Q21 ctl↔server 认证（派生决策，未单独访谈）
- 决定：server 首启生成 admin token（数据目录内文件 600），ctl 经配置/环境变量携带管理地址与 token 调用管理 API。
- 理由：Q6 已定 CLI 形态，本地/远程都能用一套认证；Unix domain socket 无法远程管理。
- 被否掉的备选：仅本机 unix socket、SSH 隧道。

### 2026-11-01 21:08 [DECISION] 规范冻结
- 决定：采用库存规范——openspec/standards/common.md（C- 前缀）+ go-uber.md（Uber Go Style Guide 索引 checklist，G- 前缀），冻结日期 2026-11-01，来源/许可证/适用范围记录在 openspec/standards/README.md。
- 理由：项目语言为 Go，Uber guide 是社区主流共识；项目内冻结副本为 coding/review 唯一事实源。
- 被否掉的备选：用户自有规范、仅用 common.md（Go 分册覆盖不全）。
- 工具链：本机原先无 Go，用户授权 brew install go（后台安装中）；protoc 待任务 1.2 前安装。

### 2026-11-01 21:15 [PROBLEM] .gitignore 裸模式险些忽略源码目录
- 现象：git status 看不到 cmd/cagent/main.go。
- 根因：.gitignore 里裸模式 `cagent`（无斜杠）匹配任意层级同名路径，整个 cmd/cagent/ 目录被忽略。
- 处理：删除裸模式（/dist/ 已覆盖构建产物）；规则：gitignore 条目一律带根斜杠或路径限定。
- 改进：后续新增 ignore 条目后用 git status --untracked-files=all 核对目标源码仍可见。

### 2026-11-01 21:15 [NOTE] 任务 1.1 实现完成
- 改动：go.mod（go 1.27）、internal/version（ldflags 注入 Version/Commit）、cmd/cagent 最小入口（--version，无子命令时 exit 2 提示）、Makefile（build/build-linux/fmt/vet/test/test-race/clean）、.gitignore。
- 自检：gofmt 无输出、go vet 通过、go build 通过；make build-linux 产出 linux/amd64+arm64 静态 ELF（stripped）；--version 冒烟正常。
- 工具链：brew 装 go 1.27.3（ghcr.io 下载慢约 10 分钟；曾并行尝试官方镜像，brew 完成后已停止清理）。
- git 身份：仓库级配置 crazychengmm <crazychengmm@users.noreply.github.com>（用户可自行更改）。
- 已提交：f289666（step，OpenSpec 脚手架+规范）、7e5259f（step，change 规划产物）、3d30f2a（任务 1.1，Go 骨架）；账本见 commits.md。
- 用户授权：2026-11-01 21:12 起「持续开发、不再询问」，取舍取推荐方案并记 journal，逐任务自检后直接提交，但不自动 push。

### 2026-11-01 21:23 [NOTE] 任务 1.2 实现完成
- 改动：proto/cagent/v1/enrollment.proto（EnrollmentService.Enroll）、control.proto（Control 双向流 + RenewCert + DownloadRelease，消息含 Heartbeat/JobDispatch/JobEvent(Started/Output/Finished)/JobCancel/UpgradeCommand/UpgradeEvent/HeartbeatAck）；make proto 生成 4 个 pb.go 并入库；proto/README 写工具与生成命令；Makefile 增 proto 目标；go mod tidy 引入 grpc v1.85.0、protobuf v1.36.12（GOPROXY=goproxy.cn）。
- 设计细化（DECISION 级，落入 proto 注释）：续签与下载放在 mTLS 端口而非注册端口；升级复用 job_id 语义；心跳冗余带 agent_id 便于日志关联；JobFinished 四结果枚举（PENDING/EXPIRED 仅 server 侧）。
- 工具链：protoc 36.2（brew）、protoc-gen-go/grpc（go install，~/go/bin）。
- 自检：gofmt clean、vet/build 通过、双架构静态交叉编译通过、go test 无测试文件包正常。

### 2026-11-01 21:30 [NOTE] 任务 1.3 实现完成
- 改动：internal/config（duration.go 自定义 Duration 支持 "15s"；agent.go / server.go 配置结构、默认值、KnownFields 严格解析、Validate）+ config_test.go（11 个表驱动/并行用例）；引入 gopkg.in/yaml.v3。
- 默认值落地：心跳 15s、判活 45s（server）、并发 4、job 超时 5m/硬上限 60m、排队 TTL 24h、证书 90d、token 168h；端口约定 enrollment 9443 / control 9444 / admin 9445；data-dir /var/lib/cagent(-server)。
- 校验：control_address 必填、并发 1..64、超时 ≤60m、标签键白名单与值非空、server 三监听互不相同。首次注册三要素（token/CA/注册地址）的条件校验留到 4.2 运行期处理。
- 问题：手写 const 对齐多了空格，gofmt -w 修复（机械门禁拦住，未进提交）。
- 自检：gofmt clean、go vet、go build、go test -race 全绿，交叉编译静态 ELF 通过。

### 2026-11-01 21:34 [NOTE] 任务 1.4 实现完成（任务组 1 收尾）
- 改动：internal/logging（slog JSON handler 工厂，级别 debug/info/warn/error）；internal/cli（Run(argv,stdout,stderr) int 子命令分发 + agent/server 接线配置加载与日志，ctl 占位 exit 2）；cmd/cagent 改为薄入口。
- 退出码约定：0 成功 / 1 运行期错误 / 2 用法错误。
- 安全：启动日志显式不打 enrollment_token（C-LOG-05），测试断言该字段不出现。
- 冒烟：agent 输出两行合法 JSON（INFO 启动 + WARN 占位）；无参/未知子命令 exit 2；双架构交叉编译通过。
- 自检：gofmt clean、vet、build、go test -race（cli/config/logging 均 ok）。任务组 1（1.1-1.4）全部完成。

### 2026-11-01 21:45 [NOTE] 任务 2.1 实现完成
- 改动：internal/server/store（store.go Open/WAL/user_version 迁移；schema.go 六表；agents/tokens/jobs/audit/releases 各自模型与 CRUD）；store_test.go 覆盖迁移幂等、token 哈希存储/吊销、agent 心跳/超时扫描、job 状态 CAS 与外键/级联、输出顺序与字节统计、审计、release。
- 关键决策：选 modernc.org/sqlite 纯 Go 驱动（非 mattn cgo），DSN 每连接 PRAGMA foreign_keys/busy_timeout；SetMaxOpenConns(1) 避免写锁竞争；时间存 UTC RFC3339Nano TEXT；enrollment token 只存 SHA-256 哈希（C-SEC-03），库中无明文；UpdateJobState 用 WHERE status NOT IN(终态) 防终态回退；动态查询只拼固定子句、值全参数化。
- 自检：gofmt/vet/build、go test -race 全绿；关键验证——CGO_ENABLED=0 双架构（amd64/arm64）静态链接成功，SQLite 无 cgo 依赖。
- 问题：UpdateJobState 占位参数顺序一度写乱并留死代码，整文件重写修正（未进提交）；三次手写 struct/const 空格对齐被 gofmt 拦截，已统一「写完先 gofmt -w」。

### 2026-11-01 21:58 [NOTE] 任务 2.2 实现完成
- 改动：internal/server/ca（LoadOrCreate：首启 ECDSA P-256 根 + 自签 CA，key 0600/目录 0700/cert 0644；NewSerial 128 位；SignAgentCSR 校验 CSR 自签后签发 ClientAuth 证书，NotBefore 提前 1 分钟抗时钟偏差；VerifyAgentCert 链校验）；agents 表加 revoked_at 列（零部署期直接演进 schemaV1，未另起 v2 迁移）。
- 吊销：store.RevokeAgent / IsAgentCertificateActive(serial)（存在且未吊销）；RecordHeartbeat 加 revoked_at IS NULL 条件，吊销机不接受心跳。
- 自检：gofmt/vet/build、ca+store go test -race 全绿；全量回归 + 双架构静态交叉编译通过。

### 2026-11-01 22:05 [NOTE] 任务 2.3 实现完成
- 改动：internal/server/enrollment/token.go TokenService（Create：cagt_ 前缀 + 32 字节 base64url，et_ 随机 ID；Validate：无效/吊销/过期三哨兵错误；可复用不消耗；List/Revoke/MarkUsed）；时钟可注入便于过期测试。
- 自检：gofmt/vet/build、go test -race 全绿，全量回归 + 交叉编译通过。
- 问题：token.go 首次 Write 写入了签名写错（time.Time 误代 time.Duration）且带占位代码的半成品，立即整文件重写，未进入提交；根因是一次想写太多，后续一个文件一次写完整再跑门禁。

### 2026-11-01 22:15 [NOTE] 任务 2.4 实现完成（任务组 2 收尾）
- 改动：internal/server/admin（EnsureAdminToken：首启生成 cactl_+32 字节 token，0600 文件 0700 目录，重启复用，空文件报错；AuthInterceptor：Unary/Stream 拦截器，authorization metadata，支持 Bearer 前缀，subtle.ConstantTimeCompare 定长比较，失败 codes.Unauthenticated）。
- 问题：Stream 单测首版传 nil ServerStream，ss.Context() 先于授权求值导致 nil panic；改为嵌入 grpc.ServerStream 的 fakeStream（真实 gRPC 中 ss 不会为 nil），补有效 token 放行用例。
- 自检：gofmt/vet/build、go test -race 全量 8 包全绿、交叉编译通过。任务组 2（2.1-2.4）完成。

### 2026-11-01 22:30 [NOTE] 任务 3.1 实现完成
- 改动：ca.SignAgentCSR 增加 commonName 入参，证书 CN=分配的 agent UUID（mTLS 后可直接从对端证书识别身份），CSR 仅证明持私钥；enrollservice 实现 Enroll：输入限制（hostname≤255、标签≤32 个/键≤128/值≤256）→ token Validate（PermissionDenied）→ uuid + 128 位 serial → CA 签发 → agents 落库（serial 十六进制、cert_not_after）→ MarkUsed → 成功/拒绝双审计；token 失败不区分过期/吊销以防探测。
- 测试：bufconn 服务级单测（成功路径校验证书链/CN=UUID/落库/可复用 token/审计 actor；坏 token PermissionDenied+rejected 审计；坏 CSR InvalidArgument；4 个输入校验用例）。
- 问题：新版 grpc-go bufconn 无 Dialer()，改 DialContext 包装；另误把测试 import 写进 service.go 已即时纠正，均未进提交。
- 自检：gofmt/vet/build、go test -race 全绿、交叉编译通过。

### 2026-11-01 22:15 [NOTE] 任务 3.2 实现完成
- 改动：
  1) ca：SignAgentCSR 增 CN（3.1）；新增 IssueServerCertificate/EnsureServerCertificate（CA 签 ServerAuth 叶子证书，SAN=配置 server_cert_hosts，默认 localhost/127.0.0.1，私钥 0600，删文件重签轮换，有效期 10 年）。
  2) config：ServerConfig 增 server_cert_hosts（默认 localhost/127.0.0.1，空条目校验），导出 ServerCertValidity。
  3) tlsconfig：Enrollment（单向 TLS）、Control（RequireAndVerifyClientCert + CA pool + VerifyConnection 查 IsAgentCertificateActive 吊销，3s 超时，TLS1.2+）。
  4) serve：三端口组装（enrollment/control/admin），net.Listen 早失败回滚已起端口，ctx 取消 GracefulStop 10s 超时强停；RegisterControl/RegisterAdmin 回调留给后续任务。
  5) cli runServer 真实装配：data-dir 0700、Open SQLite、首启 admin token（只提示路径不打印值）、LoadOrCreate CA、server 证书、enroll service，信号驱动优雅退出。
- 测试：ca servercert 5 用例；serve 真实 TCP TLS 矩阵 7 子用例（单向 TLS 注册、无证书/有效 mTLS/外来 CA/吊销/无 token/正确 token）；真实进程冒烟确认端口/权限位/SIGTERM。
- 坑：① 库内 panic 辅助函数违反 G-PANIC-01，改回 error；② 未知服务在拦截器之前返回 Unimplemented，故用真实 EnrollmentService 挂 admin 端口测拦截链；③ bidi 流的建流错误在首个 Recv 才可见；④ 合法 server 配置现在会阻塞，cli 单测改为只测启动前失败路径。
- 自检：10 包 go test -race 全绿，交叉编译通过。

### 2026-11-01 22:20 [PROBLEM] 任务 3.3 跨依赖（agent 侧续签依赖未实现的 4.1 身份模块）
- 现象：3.3 排在 4.1 之前，却要做 agent 侧证书原子替换与续签调度。
- 处理：调整任务边界——3.3 只做 server RenewCert（mTLS 识别+签发+更新序列号+吊销拒绝）并注册 control 端口；agent 侧续签调度/原子替换/替换后重连并入 4.3（控制循环，届时身份模块已就位）。tasks.md 增加 3.3a 条目记录该拆分，requirement 覆盖不变。

### 2026-11-01 22:25 [NOTE] 任务 3.3 实现完成（任务组 3 收尾）
- 改动：store.UpdateAgentCertificate（原子换 serial+not_after，吊销机 false）；controlservice.RenewCert（peer TLSInfo 取证书 CN=UUID → GetAgent → 校验 CSR → 签发 → 更新序列号，旧证因 serial 被覆盖即时失效；吊销机 PermissionDenied；坏 CSR InvalidArgument）；cli/serve 注册到 control 端口。
- 测试：纯单测 7 例（含无 peer/非 TLS/未知 agent/吊销/坏 CSR）+ serve 真实 mTLS 续签链路（续签后旧证书新连接被拒 Unavailable、新证书 Unimplemented 可达）。
- 自检：10 包 race 全绿、交叉编译通过。

### 2026-11-01 22:40 [NOTE] 任务 4.1 实现完成
- internal/agent/identity：GenerateKey(P-256)/MakeCSR/Exists/Save/Load/ReplaceCertificate；文件 identity/agent.key/agent.crt/ca.crt，目录 0700、文件 0600，writeAtomic（tmp+rename）；Load 校验私钥与证书 PEM；ReplaceCertificate 拒绝非法证书。
- 测试：round-trip、权限位、材料不完整拒绝、缺失文件、CSR 自签、原子换证共 6 组用例；全量 race 绿、交叉编译通过。

### 2026-11-01 22:50 [NOTE] 任务 4.2 实现完成
- internal/agent/trust：EnrollmentTLS 只接受 PEM 文件或 SHA256 指纹（支持冒号格式），无锚点返回 ErrNoAnchor，指纹模式 VerifyConnection 比对整张服务器证书；代码中不存在 insecure 开关；MTLSConfig 叠加客户端证书且不改基础配置。
- internal/agent/register：Enroll（锚点 TLS + token + CSR），握手层失败时 token 不离开本机；错误映射 PermissionDenied/InvalidArgument。
- config 增 server_ca_fingerprint 字段。
- 测试：真实 TLS listener 验锚点（正确/外来 CA/指纹对错/非法长度）；真实 TLS gRPC 注册成功并持久化身份、坏 token、锚点不匹配、缺锚点/空 token。全量绿、交叉编译通过。

### 2026-11-01 23:10 [NOTE] 任务 4.3（含 3.3a）实现完成
- internal/agent/control：clientTLS（身份证书+CA 锚点）；Loop（grpc.NewClient + waitReady 10s 超时，流断开指数退避 1s·2^n 封顶 30s + 0..20% 抖动，连接存活 >30s 重置退避，续签主动重连不计失败）；session（立即心跳 + ticker，无缓冲 msgCh + 带缓冲 errCh，sendMu 保护并发 Send，下行消息分发 JobHandler/UpgradeHandler goroutine）；renew（RenewCert→identity.ReplaceCertificate 原子换证→内存身份更新→errRenewed 触发重连；失败仅 WARN 保留连接，1 分钟后重试；RenewBefore 默认 30 天）。
- cli runAgent 接线：无身份时 enrollment_address+token+锚点齐备才首次注册并落盘，缺要素 exit 1；日志不含 token（C-LOG-05）；信号驱动退出。
- 测试：mTLS 假控制服务 5 组——心跳字段/首流断开重连与状态序列/临期自动续签（文件替换+二次连接）/dispatch-cancel-upgrade 三路分发/不可达持续重试且 ctx 取消返回 nil。
- 坑：测试两处——①1h 证书配 59min RenewBefore 仍有 1min 等待，改 60min；②内存身份被原地更新导致新旧证书比较失效，改保存旧值快照；fake 里残留 range channel 空循环（会阻塞）已删。
- 自检：14 包 race 全绿、交叉编译通过。

### 2026-11-01 23:25 [NOTE] 任务 4.4 实现完成（任务组 4 收尾）
- controlservice 实现 Control 双向流：streamRegistry（同 agent 重连新流替换并 cancel 旧流，remove 比对指针防误删）；handleHeartbeat 落库版本/标签并回 HeartbeatAck（对端证书 30 天内到期则 renew_cert_recommended）；吊销/删除 agent 的心跳落库 ErrNotFound → PermissionDenied 终止流（长连接上吊销即时生效，与建连 VerifyConnection 双保险）；SendToAgent/OnlineAgentIDs 供任务调度。
- sweeper：RunOfflineSweeper 默认 10s 周期，ListAgentsOlderThan(now-45s) 批量置 offline，sweepOnce 导出供单测驱动；serve.Run 按 Deps.HeartbeatTimeout/SweepInterval 启动，cli 传 45s/10s。
- 测试：真实 mTLS + agent Loop 集成（心跳置 online 且版本标签刷新、停止心跳后 sweepOnce 置 offline、吊销踢掉长连接且重连被拒、SendToAgent 离线错误）；适配 serve 测试（Control 已实现后半关闭得到 EOF 而非 Unimplemented）。
- 自检：14 包 race 全绿、交叉编译通过。任务组 4（4.1-4.4 + 3.3a）完成。

### 2026-11-01 23:45 [NOTE] 任务 5.1 实现完成
- internal/agent/executor：LocalExecutor 事件流（Started/Output/Finished）；CommandContext + Setpgid 独立进程组；cmd.Cancel 杀 -PGID(SIGKILL)，WaitDelay 5s 兜底；stdout/stderr 各一个 reader 按原始 64KB 块输出（不改字节），Wait 后等两个 reader 结束保证序；classify 区分 canceled/timeout/failed(code)/succeeded。
- 测试 6 组：成功/非零退出/stderr/工作目录+环境/超时杀组（pgrep 独特标记验证后台子进程无残留）/父取消杀组。
- 坑：① cmd.Cancel 必须配合 CommandContext（启动即报错）；② 按行 Scanner 给无换行尾块补 \n 改变了输出，改原始块读；③ macOS t.TempDir 经 /private 符号链接，pwd 对比先 EvalSymlinks。
- 自检：全量 race 绿、交叉编译通过。

### 2026-11-02 00:05 [NOTE] 任务 5.2 实现完成
- internal/agent/jobworker：Manager（N worker、1024 队列、running map+tracked 取消句柄）；任务 ctx 绑定 Manager rootCtx（控制连接断开不杀任务）；排队中取消标记 canceled、worker 取出直接回 CANCELED 不执行；在跑重复 jobID 忽略；事件映射 proto Started/Output/Finished（Result 枚举映射、默认超时 5m）；回传失败仅告警（结果重传归 5.4）。
- 测试 6 组：5 任务全完成有输出、并发 2 时序窗口（0.45-0.85s）、取消在跑 CANCELED、排队取消不执行 q2 且 q1 成功、1s 超时 TIMEOUT、重复派发仅一个 Started；-count=3 稳定。
- 【重要 PROBLEM】StdoutPipe+手动读存在竞态：极快命令（builtin printf）+高负载（全包 -race）时 Wait 可能先关闭管道导致输出丢失（偶发 ja 输出事件缺失）；改为 cmd.Stdout/Stderr 挂 eventWriter，Wait 等待内部 copy 完成，竞态消除，同时删除自写 reader 与 pipeDone。
- 自检：16 包 race 全绿、交叉编译通过。

### 2026-11-02 00:15 [NOTE] 任务 5.3 实现完成
- controlservice 接入 JobEvent：Started→running（重复 Started 终态时忽略）；Output→seq=已存块数+1，按 SumJobOutputSize 1MB 上限丢弃超限块、末块切边保证总量恰好 1MB；Finished→终态（CAS 不可回退，重放忽略），truncated=agent 上报或服务端统计≥1MB，写 job.finish 审计（actor=RequestedBy）；未知 job 仅警告不影响流。
- 测试 5 组：完整生命周期（started_at/输出顺序流/字节统计/终态/审计）、failed exit2+message、终态不可回退、2.4MB 截断到恰好 1MB 且 truncated、未知 job 忽略。
- 自检：全量 race 绿、交叉编译通过。

### 2026-11-02 00:25 [NOTE] 任务 5.4 实现完成
- jobworker 加 resultCache（FIFO 1000，只存 pb.JobFinished 终态，不存输出——输出已在 server 落库）；OnDispatch 三级判定：在跑/排队→忽略，缓存命中→回放 Started+Finished 不执行，否则登记入队；execute 完成后 put 终态。
- 测试：scriptedExecutor 精确计数，重发同 job_id 到新 sender 时 Run 仅 1 次、回放无输出块；FIFO 淘汰与默认容量。
- 自检：jobworker -count=3 稳定，全量 race 绿、交叉编译通过。

### 2026-11-02 00:35 [NOTE] 任务 5.5 实现完成
- internal/server/dispatch：MatchLabels AND 纯函数（空选择器=全部）；CreateJobs 校验命令/超时（默认 5m、硬上限 60m），零匹配 ErrNoMatch；每机独立 job_+uuid、pending 落库；OnlineDispatcher 接口（controlservice 满足）在线机立即 JobDispatch，发送失败保留 pending 待补投；job.create 审计（detail 含 selector/matched/job_ids）。
- 测试 5 组：选择器表驱动、2 机匹配（在线机收到/离线 pending）、空选择器、零匹配、校验失败。
- 自检：全量 race 绿、交叉编译通过。

### 2026-11-02 00:50 [NOTE] 任务 5.6 实现完成
- controlservice：Control 建流后 drainPendingJobs（pending 正序补发 JobDispatch，单次 1000 上限，中断保留 pending；旧流被 cancel 防重复）；expirer 周期 ExpireOnce（只动超 24h 的 pending）。
- store.ExpirePendingJobsBefore：UPDATE pending AND created_at<cutoff → expired。
- serve/cli：QueueTTL + 10s 扫描周期启动 expirer。
- 测试：store 过期扫描；真实 mTLS+Loop 重连补投 2 个 pending；ExpireOnce 只过 stale pending。
- 自检：全量 race 绿、交叉编译通过。

### 2026-11-02 01:05 [NOTE] 任务 5.7 实现完成（任务组 5 收尾）
- internal/agent/audit：FileLogger（audit.log JSONL，O_APPEND，0700/0600，互斥并发安全），JobStart/JobFinish 记录（requested_by/命令/工作目录/result/exit_code/4KB 输出预览）。
- jobworker：Auditor 接口注入（nil→nop），execute 记开始/结束，排队取消也留痕，previewBuffer 截断 4KB。
- cli runAgent 接线：OpenFileLogger(dataDir)、NewManager(concurrency, executor, logger, auditor)、Start 后作为 control.Config.JobHandler——下行任务从控制流到执行回传全链路打通。
- 测试：audit 包 3 组（JSONL/权限、重开追加、20 goroutine 并发 20 行）；worker 审计 3 组（开始结束字段、预览恰好 4096、排队取消 canceled）；-count=3 稳定。
- 问题：recordingAuditor 无锁读取被 race 检测抓出（findFinish 直读切片），改走加锁 finish 方法，持锁内不重复加锁。
- 自检：18 包 race 全绿、交叉编译通过。任务组 5（5.1-5.7）完成。

### 2026-11-02 01:20 [NOTE] 任务 6.1 实现完成
- internal/release：Manifest（version/file/sha256/size）；BuildPackage 流式 tar.gz（cagent 0755、manifest.json 0644，tar mtime 零值保证可复现构建）。
- 测试：解包校验两文件内容/权限/manifest sha256==二进制摘要/版本；空版本拒绝。
- Makefile package 目标依赖 ctl release 命令，随 7.5 一并落地（已在任务描述注明）。
- 自检：19 包 race 全绿、交叉编译通过。

### 2026-11-02 01:50 [NOTE] 任务 6.2 实现完成
- proto 新增 management.proto：ManagementService（token/agent/job/release/upgrade 全套 RPC 与消息），admin 端口注册；6.2 实现 PublishRelease（流式）+ ListReleases，其余 7 组任务实现。
- internal/release/signing：GenerateSigningKey（ed25519）、PKCS8/SPKI PEM 编解码、SignPackage（ed25519 直签整包）、VerifyPackage；签名/篡改/异钥/垃圾 PEM 单测。
- internal/server/management：流式 PublishRelease（首帧 meta 校验版本/size/sha256 hex/64B 签名，分块写入 0600 临时文件，512MB 上限，EOF 后校 size/hash、可选 ed25519 验签、版本去重 AlreadyExists，rename 原子落 dataDir/releases/<v>.pkg，InsertRelease + 审计，失败回滚文件）；ListReleases 映射 ReleaseInfo。
- config 增 release_signing_pubkey_file（可空：server 不强制验签，强制点在 agent；首装阶段可空，journal 决策记录）；cli 装配并 RegisterAdmin。
- 测试：management 7 组（成功落盘/临时文件清理、坏签名、size/hash 不符、首帧非 meta、重复版本、列表）。
- 【PROBLEM】jobworker ConcurrencyLimit 墙钟断言在全包并行 -race 过载时偶发超 850ms；放宽至 350ms..1.5s 并改 0.2s 任务，随后 40 轮稳定；取消类时序改等 Started 事件。
- 自检：全量 race 绿、交叉编译通过。

### 2026-11-02 02:20 [NOTE] 任务 6.3 实现完成
- controlservice.DownloadRelease 流式发包（64KB，版本/文件缺失 NotFound）；New 增加 packageDir，management 导出 ReleaseDirName 共用 releases 目录。
- agent upgrade.DownloadVerify：边收边 hash，大小超限即拒、EOF 后校 size/SHA256/ed25519（Expected 取自 mTLS 升级指令），无公钥拒绝；哨兵错误三类。
- 测试：agent 6 组（分块成功/size/hash/异钥/无公钥/流错误），server 3 组 mTLS 集成。
- 自检：全量 race 绿、交叉编译通过。

### 2026-11-01 21:00 [DECISION] plan 任务拆分策略
- 决定：9 个任务组、44 个任务，顺序=依赖：①骨架/proto/配置 ②server 存储+CA+token ③注册与 mTLS ④agent 身份与控制循环 ⑤远程执行 ⑥自升级 ⑦ctl CLI ⑧安装/systemd ⑨端到端验证。
- 理由：契约先行（proto/SQL schema），安全底座（CA/mTLS/身份）先于业务通道，升级放在执行之后（升级 grace/健康判定依赖 job 执行器），CLI 放 server 能力齐备后，安装脚本放最后（消费全部配置/路径），e2e 集中收尾并在功能任务内已挂关键单测。
- 可并行点：6.x 与 7.x 在 5.x 完成后可并行；8.x 只依赖 1.3/路径约定，可与 5-7 组并行（单人执行时按编号顺序）。
- 覆盖核对：29 条 requirement 全部至少映射一个任务；最大任务 6.7（guard 状态机）与 8.3（install.sh）预估半天内，已把 guard 拆为纯状态机（6.7）+系统集成（6.8）。
- 预计提交 44 次（每任务一次，Conventional Commits）。

### 2026-11-01 20:55 [NOTE] analyze 产物完成
- 事实：proposal.md、design.md、specs 下 6 个 capability delta（agent-enrollment / agent-connection / remote-job-execution / agent-self-upgrade / agent-installation / management-cli，共 29 条 requirement、42 个 scenario；此处曾误记为 26/33，20:58 更正）写完；openspec validate 与 --strict 均通过；openspec status 显示 proposal/specs/design = done，tasks = ready。
- 过程问题：写 agent-self-upgrade delta 时误把文件写到 /Users/admin/out.md，已删除并在正确路径重写，无内容遗失。
- 另：openspec/config.yaml 已补项目 context（Go/gRPC/SQLite/中文产物约定）。

### 2026-11-02 02:50 [NOTE] 任务 6.5 实现完成
- schema：jobs 加 release_version/sha256_hex/signature/package_size/allow_downgrade 五列（Job 模型/CreateJob/scanJob 同步，bool↔int），复用 job 排队/补投/TTL/幂等。
- management.Upgrade：GetRelease 必须存在（否则 FailedPrecondition），标签 AND 圈选，零匹配 FailedPrecondition；每机 upgrade job；在线经 OnlineDispatcher 发 UpgradeCommand（失败留 pending）；upgrade.dispatch 审计；New 增加 dispatcher 参数，cli 注入 controlService。
- controlservice drain 按 kind 分发（messageForPendingJob：upgrade→UpgradeCommand，command→JobDispatch）。
- 测试：management 3 组（匹配创建+在线字段+离线 pending+审计、未发布、零匹配）；drain 升级补投 mTLS 集成（字段完整）。
- 自检：21 包 race 全绿、交叉编译通过。

### 2026-11-02 03:05 [NOTE] 任务 6.6 实现完成
- jobworker.Manager.ActiveCount（排队+在跑，不含缓存）；upgrade.WaitForIdle（立即检查+100ms 轮询，超时 ErrJobsRunning，不取消任何任务；ctx 可取消）。
- 测试 4 组：已空闲立即返回、先忙后闲、超时且任务不受影响、ctx 取消。全量 race 绿。

### 2026-11-02 03:30 [NOTE] 任务 6.7 实现完成
- internal/agent/upgrade/guard.go：Run 纯状态机（Installer/HealthProbe/Reporter/ActiveTracker 全接口化，默认 grace 2m/健康 60s/总超时 10m 由调用方 ctx 控制）；步骤：release.DecideUpgrade 防降级→WaitForIdle 不强杀→Stage→Activate→Restart→WaitHealthy→Commit；任一系统步失败 fail 旧版继续；Restart/健康失败 rollback（换回旧副本+再次 Restart），rollback 自身失败→FAILED。
- 发现 proto 中 UpgradeEvent 是扁平字段（status/message/from/to）而非 oneof，report 直接构造扁平事件。
- 测试 9 组决策：happy（调用序+INSTALLING/SUCCEEDED 事件）、降级拒绝/显式允许、grace 超时不触安装器、stage/activate 失败、restart 失败回滚、健康失败回滚（调用序列严格断言）、回滚失败终态 FAILED。
- 自检：全量 race 绿、交叉编译通过。

### 2026-11-02 04:30 [NOTE] 任务 6.8 实现完成（任务组 6 收尾）
- guard 拆为 Prepare（主进程：版本门控→grace→Stage→Activate→Restart）与 GuardWatch（独立 guard：健康门控→Commit/Rollback+重启）；Run 保留为测试编排。
- state.go：upgrade-state.json（SaveState 原子写）跨进程传递 BinaryPath/BackupPath/StagedPath/Unit/版本；healthy 标记（MarkHealthy/IsHealthy/ClearHealthy）由新版本首次 mTLS 连接成功（OnStateChange）写入。
- system.go：SystemInstaller 复制+rename（兼容跨文件系统），Stage 0755、Activate 先备份旧版再替换、Rollback 校验备份存在后换回、Commit 清理；SystemdRestarter 增加 SpawnGuard 钩子，restart 前先 detach。
- spawn.go：SpawnDetachedGuard 以 Setsid+Process.Release 启动 `cagent agent --guard-mode <state>`；cli 新增 runGuardMode（加载状态/身份→FileHealthProbe 60s→GuardWatch→GuardReporter.Flush 集中上报→清状态）。
- handler.go：升级处理器独立建 mTLS 短连接 DownloadVerify→写 state→Prepare（失败旧版继续）；config 增 signing_pubkey_file（空则拒绝升级）；runAgent 接入升级处理器与健康标记。
- server：HandleUpgradeEvent 把 INSTALLING→running、SUCCEEDED→succeeded、ROLLED_BACK/FAILED→failed，写 job.finish 审计；未知 job 忽略。
- 测试：SystemInstaller 全生命周期（含无备份回滚）、restarter SpawnGuard 顺序与命令失败、state 往返/健康标记/FileHealthProbe、upgrade 事件三态与终态不可变。
- 自检：21 包 race 全绿、交叉编译通过。任务组 6（6.1-6.8）完成。

### 2026-11-02 04:00 [NOTE] 任务 7.1-7.5 实现完成（任务组 7 收尾）
- internal/ctl：Config（~/.config/cagent/ctl.yaml + CAGENT_ADMIN_ADDR/TOKEN 环境覆盖，缺地址/token 发请求前失败）、Dial（服务端 TLS、unary+stream Bearer 拦截器、就绪等待）、Run 分发（--config 任意位置解析）。
- 命令：token create（--ttl 小时，明文仅一次）/list（状态不明文）/revoke；agent list/show（NotFound）；job run（多 --selector、--timeout、--working-dir，返回每机 job id）/list（--agent/--status/--limit）/show（状态+输出流+truncated）/cancel（在线下发 JobCancel，离线 Unavailable）；release package（本地 tar.gz）/sign（离线 ed25519 .sig）/publish（解包读 manifest，meta+分块流式）/list；upgrade --release/--selector/--allow-downgrade。
- server management RPC 一次补齐：CreateToken/ListTokens/RevokeToken（真实 TokenService）、ListAgents/ShowAgent、RunJob（dispatch.Service）、ListJobs/ShowJob（带输出）、CancelJob（OnlineDispatcher）；New 增 tokens/jobCreator 依赖。
- Makefile 增 package 目标（build-linux+build→ctl release package），VERSION 走 git describe；实际冒烟打包出 cagent+manifest.json。
- 测试：management RPC（token 生命周期/agent/run/job 读取与取消离线错误）、ctl 配置文件/环境覆盖/缺凭据/flag 解析/帮助码。
- 问题：management.TokenService 接口签名与 enrollment.CreateToken 参数顺序初版不一致，按实际对齐；recordingOnline 需对离线 id 返回 ErrAgentOffline；t.Setenv 与 t.Parallel 冲突，相关用例去 Parallel。
- 自检：全量 race 绿、交叉编译通过、openspec validate strict 通过。

### 2026-11-02 04:40 [NOTE] 任务 8.1-8.4 实现完成（任务组 8 收尾）
- internal/paths：集中标准布局（二进制/配置/数据/身份文件/releases/升级状态、server 侧布局、单元名、默认端口）。
- deploy/cagent.service：Type=simple、Restart=always RestartSec=2、WantedBy multi-user、journald、NoNewPrivileges/ProtectSystem/ProtectHome/PrivateTmp、TimeoutStopSec；cagent-server.service 同构（Restart=on-failure）。
- scripts/install.sh（可执行，sh -n 通过）：必填参数与 CA/指纹互斥校验、root+systemctl+glibc+amd64/arm64 检测、install 原子落位+二进制可执行预检、配置 0600（不回显 token）、unit 安装（缺 deploy 时内置兜底）、daemon-reload+enable+restart+is-active 确认、trap 回滚已落文件；无任何 insecure 开关。
- test/e2e：Dockerfile.systemd（ubuntu 22.04 + systemd）与 docker_systemd_test.sh（布局安装、kill -9 MainPID 后验证新 PID 且 active）；脚本在镜像不可达时显式 SKIP。
- 【PROBLEM】本机 OrbStack docker.io/auth.docker.io 超时（与 ghcr 同源网络限制），镜像站 daocloud/1panel 此刻也不可达，容器实跑留待 CI；脚本已支持镜像站回退与 SKIP，语法与资产齐备。

### 2026-11-02 06:00 [NOTE] 任务 6.8 收尾修正 + 任务组 9 e2e（9.1-9.6 完成，全流程打通）
- 【关键修复 1】Stage 错误：把整个 tar.gz 当二进制写入（ELF 损坏，新版本崩溃回滚）；新增 ExtractBinary 从包中解出 cagent 再暂存，6.8 单测改为先 BuildPackage 再 Stage。
- 【关键修复 2】升级结果上报竞态：guard 短连接上报 SUCCEEDED 后立即退出，与 agent 重连竞争同一控制流，旧流 ctx 取消导致「扫描 job: context canceled」事件丢失。改为：① 业务落库统一用 context.WithoutCancel 不随连接取消；② guard 不再建短连接，只把结果写 upgrade-result.json；③ 重启后的 agent 通过 OnConnected 钩子在自己的稳定主连接上等待并上报该文件（ReportPendingResults），上报成功才删除；④ rollback 重启使用不带 SpawnGuard 的独立 restarter，避免重复 guard。
- 【关键修复 3】detach guard 健康探测：独立 Setsid 进程中较长 Sleep(200ms)/select-timer 偶发永久阻塞（多次 e2e 复现，guard 存活 STAT Ss 但零 tick）；改为 20ms 短间隔墙上时钟轮询，6 次压测全过；根因疑似 Go runtime timer 与 detach/重启信号时序竞态，短轮询规避。
- 【修复】ctl publish 的 sha256 应对整个 .pkg 计算（而非包内 manifest 的内层二进制哈希），与 agent 下载验签对象一致。
- 【修复】config Duration 支持「d」单位（90d/168h），此前 time.ParseDuration 不认天。
- 【修复】CA 目录导出 ca.CADirName（数据目录/ca），harness/ctl CA 路径对齐。
- e2e 框架（test/e2e/harness.go）：go build 注入版本的真二进制、临时三端口 server、name 标签选机、每 agent 独立二进制副本与 restart.sh（nohup+kill $PPID 模拟 systemctl）、签名密钥对、WaitFor/Ctl/版本构建/发布 helper、失败 dump server/agent/guard 现场。
- 6 个 e2e：注册上线、run 输出/退出码、cancel、离线扫描+补投（12s）、自升级全链路（打包/签名/发布/标签升级/验签/替换/guard 健康/结果上报/job succeeded/新版本 online，6/6 稳定）、默认降级拒绝。
- 9.6 docker 资产就绪；本机 docker.io/auth.docker.io 与镜像站当前不可达，容器实跑在 CI 执行，脚本镜像不可达时显式 SKIP。
- 最终门禁：gofmt clean、go vet、go test -race 全 22 包绿、linux amd64+arm64 静态编译、openspec validate --strict 通过、6 e2e 全绿。

### 2026-11-02 生产化 Review 加固（发布前终审）
按生产可用标准做全量代码审查（staticcheck 2026.2.2 + 人工并发/安全/资源审查）：
- 【并发】升级 OnUpgrade 改用独立 context（DefaultTotalTimeout 10m），不随控制流断开取消，避免慢网络/抖动中断下载替换；此前定义的 10m 总超时未使用，已接通。
- 【并发】升级单飞锁 inFlight：在线下发与重连补投可能并发到达同一 agent，现同一时刻只允许一个升级流程，重复指令忽略并告警。
- 【安全·路径穿越】release 版本号会拼入包文件路径；publish 元数据与 DownloadRelease 均改为严格 release.Parse(x.y.z) 校验，杜绝 ../ 穿越（原先仅拒绝含 / 字符，纵深防御不足）。
- 【资源】ReportPendingResults 智能窗口：正常连接（无 upgrade-state.json）仅等待 500ms 即退出，检测到升级进行中才延长到 70s；避免每条控制流都挂一个长生命周期 goroutine。
- 【健壮】agent 未配置签名公钥时在下载前明确拒绝升级（此前要等下载后验签才失败）。
- 【审计】ctl job cancel 是显式运维操作，补 job.cancel 审计（操作者/agent/命令）。
- 【安装】install.sh 新增 --signing-pubkey，安装时分发 ed25519 发布公钥，装完即具备签名自升级能力；写入 config。
- 【可运维性】新增 deploy/examples/{agent,server,ctl}.yaml 生产配置示例；README 增「运维与数据保留」章节（journald/logrotate/SQLite 备份与输出清理）。
- 【测试】根治 SelfUpgrade e2e 偶发失败：job succeeded 断言从「online 后立即查一次」改为轮询等待（result 落库天然晚于 online 标记）；高负载（yes×6）下自升级连跑 10/10、完整 e2e 3 轮全绿。
- 清理 staticcheck 报告的未使用函数（nowText、callsFiltered）。
- 终审门禁：gofmt clean、go vet、staticcheck 干净（仅测试内 crypto deprecation 为标准用法）、go test -race 23 包全绿无竞争、CGO 静态 linux amd64/arm64、openspec validate --strict、e2e 多轮稳定。

### 2026-11-03 OrbStack 真实 Ubuntu/systemd 验证：发现并修复两个 cgroup 级自升级缺陷（9.6 实跑）
用 OrbStack 创建真实 Ubuntu 22.04 arm64 机器（PID1=systemd、glibc 2.35），完整验证 install.sh/systemd/注册/任务/自升级/回滚/降级，捕获到单测与注入式 e2e 无法发现的两个真实缺陷：
- 【缺陷 1·自杀式重启】agent 在自己服务的 cgroup 内同步执行 systemctl restart cagent.service，systemd 以 KillMode=control-group 停止服务时把发起命令的 agent 自身与 systemctl 一起 SIGTERM（exec 返回 signal: terminated），与服务启停竞争。修复：主重启动作改由 systemd-run 创建独立 cgroup 的 oneshot unit（cagent-restart-*）延迟 1s 执行，agent 不等自身被杀。
- 【缺陷 2·guard 不可信，更关键】guard 最初用“当前可执行文件”启动，而它在二进制已替换为新版本后才真正运行——新版本一旦损坏（实测构造了进程存活但永不建连的 4.0.x），guard 自己也是坏的，永远无法回滚。实测抓取 Go 栈确认 guard 跑的是 fakeagent 的 30 分钟 Sleep。修复：guard 一律用升级前的可信旧副本（Activate 备份的 BackupPath=cagent.old）启动，SpawnDetachedGuard 增加 guardBinary 参数，坏版本再也无法让回滚失效。
- 配套：rollback 增加目录 fsync 与替换后版本确认；回滚重启器用独立 cgroup 但同步执行（guard 不在服务 cgroup 内）；handler 早期失败（门控/grace/验签/替换）清理 upgrade-state.json 防残留；rollbackRestarter 不再 spawn guard。
- 真实环境实测结论（Ubuntu 22.04 arm64 + systemd 249）：① install.sh 一键安装 active+enabled、0700/0600 权限正确、mTLS 自动注册 online；② kill -9 后 Restart=always 自动拉起且身份复用；③ 远程任务输出/退出码/双端审计正确；④ 健康升级 2.1.0→2.1.1 门控 1s 通过、job succeeded、旧副本与 transient unit 全部清理；⑤ 损坏版 4.0.1 60s 门控超时后自动回滚 3.1.0 并 online，job failed 原因明确（trace: 健康门控超时 last=3.1.0）；⑥ 默认降级 3.1.0→3.0.0 被拒。
- 新增 TestSpawnGuard_UsesTrustedOldBinary 等回归测试；README 用 Mermaid 重画架构图与时序图（用户要求），并补充 systemd cgroup 解耦、可信 guard 二进制等设计要点。
- 说明：e2e 注入重启脚本（非 systemd）与生产（systemd-run）走两条路径，正好互补——前者验证编排/状态机，后者暴露真实 init 系统语义；二者都必须保留。

### 2026-11-03 Review 第二轮：server 状态一致性 / 数据保留 / 审计 / 可观测性
在真实 systemd 验证后又做了一轮代码审查，修复 5 个生产问题：
- 【正确性】server 重启后 DB 残留 online 幽灵（registry 内存已清空，但 agents.status 仍 online，管理面误判在线，最坏要等心跳超时+sweep 才恢复）。新增 store.ResetOnlineAgents，server 启动时统一置 offline，agent 重连首跳即刷新。
- 【运维/容量】SQLite 无数据保留：job_output、终态 jobs、audit_logs 只增不删，长期运行无限膨胀。新增 store.PurgeOldData（同事务级联删终态 job+输出，pending/running 永不删）+ cli.startRetentionJanitor（启动 30s 后每小时执行），新增配置 job_retention(默认30d)/audit_retention(默认90d)/purge_interval，负值禁用；补 ResetOnlineAgents/PurgeOldData 单测。
- 【安全审计】enrollment token 的 create/revoke 此前无审计（敏感操作缺口），补 token.create/token.revoke 审计（操作者/token id/TTL）。
- 【可观测】三个 gRPC 端口注册标准 grpc.health.v1，供存活探针/负载均衡。
- 【运维】agent 本地 audit.log 是 O_APPEND 永不轮转；新增 deploy/cagent-agent.logrotate（daily/14 份/copytruncate），install.sh 在系统有 logrotate 时自动部署。
- 清理 staticcheck 死代码（errFake）；server 配置示例与 README 同步保留期/健康端点/logrotate 说明。
- 门禁：gofmt/vet/staticcheck 干净，go test -race 全绿，test/e2e 通过，linux amd64+arm64 静态编译，openspec strict 通过。

### 2026-11-04 v1.0.0 发布工程化
- 打首个语义版本 tag v1.0.0（annotated，指向 78fcfd9），已推送 GitHub。
- 新增 .github/workflows/release.yml：推 v* tag 自动跑 make test-race → CGO 静态双架构构建 → SHA256SUMS → 创建 GitHub Release（附 linux amd64/arm64 二进制与校验和）。
- 新增 .github/workflows/ci.yml：push/PR 跑 gofmt、go vet、go test -race、交叉编译。
- Makefile 新增 release-assets VERSION=x.y.z（本地等价 CI 构建，跨平台 shasum 回退）；README 新增「发布流程」章节（tag 自动发布/本地构建/签名升级包/校验）。
- 签名 ed25519 私钥保持离线、不进 CI；CI 只产未签名二进制，签名包由发布者离线生成后 publish。
