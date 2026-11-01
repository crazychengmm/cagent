# Design

## Context

项目从零开始，单仓 Go 实现，交付物为一个静态二进制 `cagent` 的三个子命令：

- `cagent agent`：装在受控 Linux 机器上，root 运行，systemd 常驻
- `cagent server`：单节点控制端，内置 CA、gRPC 服务、SQLite 存储
- `cagent ctl`：运维人员的管理 CLI

规模目标：单 server 支撑 ≤1000 agent。设计不堵死后续水平扩展（agent 始终主动出站），但首期不引入集群、消息队列或外部数据库。

## Goals

- agent 启动即凭信任锚点完成注册，获得 mTLS 身份，证书到期自动续签
- 不稳定网络下指令不丢（离线排队补投）、不重（job_id 幂等）
- 远程 shell 任务异步执行、流式回收输出、可超时可取消、全程审计
- 自升级全链路安全：ed25519 签名、标签灰度、健康门控、失败自动回滚、不杀用户任务
- 一条 `install.sh` 在支持矩阵内的空机器上得到可重复的安装结果
- 代码分层使监控、配置管理等后续角色作为新层接入，不改通信与升级底座

## Non-Goals

- 监控指标采集、配置管理/状态收敛、日志采集（后续独立 change）
- Web UI/控制台；Prometheus `/metrics` 端点
- server 自身的自升级（首期手工升级）、server 集群/HA、多租户
- PostgreSQL/MySQL 等外部数据库；deb/rpm 包；非 systemd、容器、Windows/macOS 上运行 agent
- 命令白名单、审批工作流、细粒度 RBAC（协议预留操作者字段）
- 真·外部插件机制（Go plugin / 插件协议）；CI 流水线（仅提供本地 e2e 脚本）
- server 端动态改标签、动态分组（首期标签只来自 agent 配置文件）

## Decisions

| # | 决策 | 理由 | 被否掉的备选 |
|---|---|---|---|
| D1 | Go 单仓单语言；gRPC 双向流 + protobuf；CGO_ENABLED=0 静态二进制，linux/amd64+arm64 | 无运行时依赖，部署与自升级最干净；交叉编译简单；bidi stream 天然承载心跳/下发/回传多类消息 | Rust（速度慢）、Python（打包脏）；REST+轮询（实时性与连接语义差） |
| D2 | 单二进制三子命令 `agent/server/ctl`，版本号经 `-ldflags -X` 注入 | 升级包只含一个文件；ctl 与 server 同版本避免协议漂移 | 多二进制（升级包与版本对齐更复杂） |
| D3 | 协议面分两个 gRPC service：`EnrollmentService`（单向 TLS + token，仅 Enroll/Renew 可达）与已认证通道；注册后一条 `Control` 双向流承载 Heartbeat、JobDispatch、JobEvent（状态/输出块）、UpgradeCommand、RenewCert | 一条多路复用流连接数最少、离线判定最直接；Enroll 单独暴露满足「注册前还没有证书」 | 多个独立流/端口（连接管理复杂） |
| D4 | server 内置 CA：ECDSA P-256 证书体系（根密钥数据目录 600）；发布物签名用 ed25519（独立密钥对，公钥内置+可配置追加） | 算法各取所长：x509 生态兼容性 vs 签名简单；两类密钥隔离，升级签名密钥泄露不等于 CA 泄露 | 外置 Vault/企业 CA（首期过重）；两场景共用一把密钥 |
| D5 | SQLite（WAL 模式）持久化；表：agents、enrollment_tokens、jobs、job_output、audit_logs、releases | ≤1000 规模零运维依赖；WAL 下读写并发足够；备份=复制文件 | PostgreSQL（首期 DBA 负担）；纯文件（查询/审计弱） |
| D6 | job 状态机落库驱动：dispatch 后以 job 行状态为准，agent JobEvent 回传推进；重发同一 job_id 时 agent 返回缓存结果不重新执行 | at-least-once + 幂等是「不丢不重」的最简可靠组合 | 分布式事务/恰好一次（复杂度不值） |
| D7 | executor 独立成接口：`Run(ctx, spec) -> event stream`；进程以独立进程组启动，超时/取消杀整个进程组；并发用本地 worker 池（默认 4）排队 | 单测可 mock 执行器；进程组防孤儿；背压保护机器 | shell 直接 exec 不设组（留下 sleep 孤儿） |
| D8 | 升级 guard 为同二进制 fork 出的短生命周期进程（`cagent agent --guard-mode <state-file>`）：负责替换、`systemctl restart`、轮询健康、必要时回滚，不依赖被升级进程自身逻辑 | 新二进制若启动即崩溃，无法靠它回滚自己；guard 与业务逻辑解耦、可单测状态机 | systemd reverse trust / 旧进程自我替换（崩溃即失联） |
| D9 | 健康判据 = 替换重启后 60s 内本地自检（配置可加载、端口/状态正常）+ 与 server 完成一次 mTLS 心跳；旧二进制保留在 `/var/lib/cagent/releases/` | 光进程存活不等于通信正常（证书错、配置坏照样活着） | 只看进程 active（假健康） |
| D10 | 升级与运行任务：2 分钟 grace，超时升级 FAILED 上报，绝不杀任务；默认拒绝降级，`allow-downgrade` 显式覆盖 | 升级是运维动作，不能为此损坏在跑的业务；防降级攻击 | 立即中断任务强升 |
| D11 | ctl↔server：server 首启生成 admin token（数据目录 600），ctl 用管理地址 + token 经 gRPC/本地配置调用；所有写操作记操作者审计 | 本地远程一套机制；unix socket 无法远程 | 仅 unix socket、SSH 隧道 |
| D12 | 插件首期只做代码层分层：`internal/proto`（协议）、`internal/agent`（连接/身份/执行器/升级）、`internal/server`（CA/存储/任务调度）、`internal/ctl`；角色能力以接口边界预留 | rule of three：第二个角色（监控）出现时再抽真插件协议，避免预判错误 | 首期 Go plugin / 外部插件 RPC |
| D13 | 三层测试：① `go test` 单测（executor、升级状态机、存储、幂等去重，接口 mock，不依赖 root/systemd）；② macOS 本地双进程 e2e（真 mTLS：注册→run→用伪造下一版二进制走完整升级/回滚）；③ Linux docker(systemd) 验证 install.sh、服务重启与回滚 | 开发机无 systemd；单测保逻辑、双进程 e2e 保协议、容器保部署形态 | 只做单测（systemd 路径无覆盖）；首期上 CI（后续 change） |
| D14 | 可观测性：两端结构化 JSON 日志（agent 由 systemd 进 journald）；关键审计一律落 SQLite audit_logs | 首期审计是安全刚需；metrics 随监控 change 再做 | 首期 Prometheus 端点（越界） |

### 目标仓库结构

```
github.com/crazychengmm/cagent
├── cmd/cagent/main.go                 # agent|server|ctl 子命令入口
├── proto/cagent/v1/*.proto            # Enrollment / Control 协议
├── internal/
│   ├── agent/        # enroll, control loop, executor, upgrade(guard), identity, config
│   ├── server/       # grpc services, ca, store(SQLite), job queue/dispatch, audit
│   ├── ctl/          # token/agent/job/release/upgrade 子命令
│   └── version/      # ldflags 注入
├── scripts/install.sh
├── deploy/cagent.service
└── test/e2e/         # 本地双进程脚本 + docker(systemd) 用例
```

## Risks / Trade-offs

- **root + 远程 shell 是高价值入口**：残余风险靠 mTLS、token TTL 与吊销、强制审计、签名升级共同收敛；白名单/审批明确留到有真实合规需求时（协议已预留操作者字段）。
- **内置 CA 根密钥在 server 单机**：600 权限 + 单机部署是首期可接受的折中；服务器被拿下即全局失守，后续可换外置 CA/KMS（agent 信任锚点支持追加公钥，轮换路径已预留）。
- **升级仍可能批量失败**：灰度标签 + 自动回滚把爆炸半径限制在圈选批次；但错误签名密钥/错误配置类事故回滚也救不了——靠流程上先 canary 小批（操作约定，非代码强制）。
- **mTLS 依赖机器时钟准确**：证书校验对时钟漂移敏感，安装文档要求 NTP；注册失败日志需明确区分时钟错误。
- **可复用 enrollment token 泄露可冒名装机**：7 天 TTL + 随时吊销 + 注册审计；不做一机一 token 是为批量装机可操作，属有意取舍。
- **SQLite 是单点**：规模内性能无虞（千条级连接、job_output 批量提交）；数据安全靠文件备份，HA 留给后续 change。
- **macOS e2e 不覆盖 systemd**：由 docker(systemd) 层补齐；两层环境差异（如路径权限）可能漏网，接受首期手工跑容器验证。
