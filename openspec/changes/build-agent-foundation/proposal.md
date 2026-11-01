# Proposal

## Why

团队目前缺少统一的 Linux 服务器远程管控入口：批量执行命令依赖共享 SSH 账号与跳板机，谁在哪台机器执行过什么无法追溯；运维工具的安装与升级全靠逐台登录，版本漂移不可见；后续规划的监控采集、配置收敛等角色，也没有一条可靠、可认证的指令通道与 agent 身份体系可依附。本 change 交付一个以 root 身份常驻受控机器的 agent 与单节点 server，打通「注册接入（mTLS 身份）→ 远程任务执行（全程审计）→ 签名自升级（标签灰度、失败自动回滚）」的最小闭环，并以插件式分层为监控、配置管理、日志采集等后续角色留出底座。不做这个底座，未来每个角色都要各自重复解决身份、连接、可靠性与升级问题。

## What Changes

- 新增单一静态二进制 `cagent`，含 `agent` / `server` / `ctl` 三个子命令（Go，CGO_ENABLED=0，linux/amd64+arm64）
- agent 经 `install.sh` 一键安装：凭 enrollment token + server CA 信任锚点注册，本地生成密钥换发证书，注册 systemd 服务并开机自启
- server 内置 CA 与 SQLite 存储，承载 gRPC 双向流长连接：心跳判活、离线排队补投、at-least-once 投递 + job 幂等
- 远程任务执行：标签选择器圈选、异步 job、流式输出、超时/取消（进程组）、终态机、强制审计
- agent 自升级：ed25519 签名包验证、标签灰度、guard 编排替换重启、60 秒健康判定、失败自动回滚、运行任务 2 分钟 grace、默认拒绝降级
- `cagent ctl` 管理命令面：token / agent / job / release / upgrade 全部操作，无 Web UI
- 三层验证：Go 单元测试、macOS 本地双进程 mTLS e2e、Linux docker(systemd) 安装/回滚验证

## Capabilities

### New Capabilities

- `agent-enrollment`: agent 首次注册的信任建立与长期身份——enrollment token 引导、server CA 锚点、内置 CA 签发证书、mTLS 强制、证书自动续签
- `agent-connection`: agent 主动出站长连接与消息可靠性——重连退避、心跳与在线状态、离线排队补投、job 幂等去重
- `remote-job-execution`: 远程异步 shell 任务——标签选机、执行模型、流式输出、取消超时、终态机、执行审计
- `agent-self-upgrade`: server 驱动的安全自升级——签名验真、灰度圈选、grace、guard 健康门控与自动回滚、防降级
- `agent-installation`: 标准安装路径与运行形态——install.sh、固定文件布局、systemd 生命周期、平台矩阵
- `management-cli`: server 的命令行管理面——CLI 认证、令牌/机器/任务/发布/灰度命令集

### Modified Capabilities

（无——本项目首个 change，specs/ 下尚无既有能力）

## Impact

- 从零新建 Go 单仓（module `github.com/crazychengmm/cagent`），无既有系统兼容包袱
- 受控机器新增：root 运行的 `cagent.service` 常驻服务、到 server 的出站 mTLS 长连接、`/usr/local/bin`、`/etc/cagent/`、`/var/lib/cagent/` 标准路径
- server 侧新增：单节点进程、agent 面向端口与管理 API、内置 CA 根密钥（数据目录 600 权限）、SQLite 数据文件
- 安全影响显著：agent 等同 root 级远程执行入口，因此 mTLS、升级包签名、执行审计在本 change 中均为强制项而非可选项
- 规模目标：单 server ≤1000 agent；集群化、多租户、Web UI 均不在本 change 范围
