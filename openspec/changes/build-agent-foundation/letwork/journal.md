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
