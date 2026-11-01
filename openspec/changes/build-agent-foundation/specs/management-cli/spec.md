## Purpose

本能力是 server 的唯一人工管理入口：运维人员通过 cagent ctl 完成注册令牌管理、机器清单查询、远程任务下发与跟踪、升级包签名发布与标签灰度。首期不提供 Web 界面；所有写操作都必须携带操作者身份并留下审计。

## ADDED Requirements

### Requirement: CLI authentication
cagent ctl MUST 通过配置文件或环境变量获得 server 管理地址与 admin token；admin token 在 server 首次启动时生成并以 600 权限保存。凭据缺失或错误时所有管理命令必须被拒绝。

#### Scenario: 有效凭据执行管理操作
- **GIVEN** ctl 配置了正确的 server 地址与 admin token
- **WHEN** 执行任意管理子命令
- **THEN** 命令通过鉴权并正常执行

#### Scenario: 凭据缺失被拒绝
- **GIVEN** ctl 未配置 admin token
- **WHEN** 执行管理子命令
- **THEN** 命令在发起业务请求前失败，并提示配置凭据

### Requirement: Token management commands
CLI MUST 提供 `token create --ttl`（默认 168h）、`token list`、`token revoke <id>`。token 明文仅在创建时输出一次，list 输出不得包含 token 明文。

#### Scenario: 创建后吊销令牌
- **GIVEN** 管理员已通过 ctl 鉴权
- **WHEN** 创建一个 token 随后执行 revoke
- **THEN** token list 中该令牌显示为已吊销，且使用它的新注册被 server 拒绝

### Requirement: Agent inventory commands
CLI MUST 提供 `agent list` 与 `agent show <id>`，输出至少包含 UUID、标签、agent 版本、在线状态与最后心跳时间。

#### Scenario: 查询机器清单
- **GIVEN** server 上已有若干注册 agent
- **WHEN** 执行 agent list
- **THEN** 输出每台机器的 ID、标签、版本、在线状态与最后心跳时间

### Requirement: Job operation commands
CLI MUST 提供 `run --selector --cmd [--timeout]`（返回 job 标识）、`job list`、`job show <id>`（含状态、退出码与输出）、`job cancel <id>`。

#### Scenario: 下发并跟踪任务
- **GIVEN** 存在在线且标签匹配的 agent
- **WHEN** 执行 run 后用 job show 查看
- **THEN** 可观察到 job 状态流转及最终退出码与输出

#### Scenario: 取消任务
- **GIVEN** 一个尚未进入终态的 job
- **WHEN** 执行 job cancel
- **THEN** job 进入 CANCELED 且 agent 侧对应进程组被终止

### Requirement: Release and rollout commands
CLI MUST 提供 `release sign <pkg>`（管理端对发布物做 ed25519 离线签名）、`release publish <pkg> --sig <file>`、`release list`，以及 `upgrade --release <ver> --selector <选择器>` 发起标签灰度升级；upgrade 命令默认不得携带 allow-downgrade。

#### Scenario: 签名发布并灰度升级
- **GIVEN** 一个构建好的发布物包与带 canary=true 标签的 agent
- **WHEN** 管理员依次执行 release sign、release publish，并以 canary=true 选择器执行 upgrade
- **THEN** 仅匹配标签的 agent 收到该版本升级，其余 agent 版本不变
