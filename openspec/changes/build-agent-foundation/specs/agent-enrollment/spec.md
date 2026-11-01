## Purpose

本能力定义 agent 与 server 之间从「互不相认」到「双向认证长连接」的信任建立过程：运维人员用带有效期、可吊销的 enrollment token 引导装机，agent 凭 token 换得由 server 内置 CA 签发的身份证书，之后所有业务通信一律走 mTLS，并在证书到期前自动续签。它是远程执行与自升级等一切后续能力的安全前提。

## ADDED Requirements

### Requirement: Enrollment token lifecycle
Server SHALL 提供 enrollment token 的创建、查询与吊销能力。token 为密码学随机生成，可设置有效期（默认 7 天），在有效期内可被多台机器重复用于注册；过期或已吊销的 token 必须被拒绝。

#### Scenario: 有效 token 注册成功
- **GIVEN** 一个未过期、未吊销的 enrollment token
- **WHEN** agent 携带该 token 发起注册
- **THEN** server 接受注册并为其签发身份证书

#### Scenario: 过期或吊销 token 被拒
- **GIVEN** 一个已过期或已被吊销的 enrollment token
- **WHEN** agent 携带该 token 发起注册
- **THEN** server 拒绝注册并返回明确的认证失败错误
- **AND** 该失败尝试被记录到审计日志

### Requirement: Server trust anchor at installation
Agent 安装时 MUST 获得 server CA 证书（PEM 文件或证书指纹）作为唯一信任锚点；未提供信任锚点的安装必须直接失败，系统不得提供任何跳过 server 证书校验的选项。

#### Scenario: 锚点匹配时注册成功
- **GIVEN** install.sh 提供了与 server 证书链匹配的 CA 证书
- **WHEN** agent 首次连接 server 发起注册
- **THEN** TLS 握手成功并继续注册流程

#### Scenario: 锚点不匹配时拒绝连接
- **GIVEN** agent 配置的 CA 与 server 实际证书不匹配（如遭遇中间人）
- **WHEN** agent 发起注册连接
- **THEN** TLS 握手失败，agent 不发送 token 并记录错误日志

### Requirement: Certificate enrollment and persistent agent identity
首次注册时 agent MUST 在本地生成私钥与 CSR；server 校验 token 后签发证书，并为该 agent 分配持久 UUID。UUID 与证书、标签绑定并保存在 agent 本地；删除身份文件后重新注册视为一台新机器。

#### Scenario: 注册生成持久身份
- **GIVEN** agent 首次使用有效 token 注册
- **WHEN** 注册成功
- **THEN** agent 本地保存 UUID、私钥与证书，后续心跳携带同一 UUID

#### Scenario: 同一 token 为多台机器分别签发身份
- **GIVEN** 一个可复用 token 已为机器 A 完成注册
- **WHEN** 机器 B 使用同一 token 注册
- **THEN** server 为机器 B 签发独立 UUID 与证书，两台机器在 agent 列表中分别可见

### Requirement: Mutual TLS enforcement
注册完成后，agent 与 server 之间的所有业务 RPC MUST 使用 mTLS 双向认证；server MUST 拒绝任何无有效客户端证书或证书已吊销的连接。

#### Scenario: 无证书访问业务通道被拒
- **GIVEN** 一个未持有有效 agent 证书的客户端
- **WHEN** 它连接 server 业务端口并发起任意业务 RPC
- **THEN** 在 TLS 握手或鉴权阶段即被拒绝

### Requirement: Automatic certificate renewal
Agent MUST 在证书到期前通过既有 mTLS 连接自动发起续签，新证书原子替换旧证书。续签失败不得中断当前连接，但必须持续输出告警日志直到续签成功或证书过期。

#### Scenario: 临期证书自动续签
- **GIVEN** agent 证书距到期小于续签窗口且 mTLS 连接正常
- **WHEN** agent 发起续签
- **THEN** server 签发新证书，agent 用新证书建立后续连接且 UUID 与身份不变
