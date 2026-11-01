## Purpose

本能力让 server 能安全地批量更新 agent 自身：升级包必须经 ed25519 签名验真，按标签灰度圈选影响面，升级时不杀运行中的用户任务；新版本启动后须通过健康门控，否则由独立 guard 流程自动回滚。它要防止的灾难场景是一次下发把一批运维核心机器打挂。

## ADDED Requirements

### Requirement: Signed release packages
发布物 MUST 为 tar.gz（内含二进制与 manifest：版本号、SHA256）并附带对包内容的 ed25519 签名；agent 下载后必须先验签并校验 SHA256，任一失败则拒绝安装、继续运行旧版本、升级 job 置为 FAILED 并上报原因。

#### Scenario: 合法签名包升级成功
- **GIVEN** server 上一个签名与 SHA256 均正确的新版本发布物
- **WHEN** 匹配 agent 收到升级任务并完成下载
- **THEN** 验签与哈希校验通过并进入替换重启流程

#### Scenario: 篡改或非法签名包被拒绝
- **GIVEN** 下载到的包字节被改动，或签名不被 agent 信任公钥验证通过
- **WHEN** agent 校验发布物
- **THEN** 拒绝安装，继续运行旧版本，升级 job 为 FAILED 且上报可辨识的失败原因

### Requirement: Label-based gradual rollout
升级 MUST 通过标签选择器（AND 语义）圈选目标 agent，一次下发只影响被选中的机器；未被选中的 agent 不升级。发给离线 agent 的升级任务遵循与普通 job 相同的离线排队与 24 小时过期规则。

#### Scenario: 灰度只影响被选机器
- **GIVEN** 10 台 agent 中 2 台带标签 canary=true
- **WHEN** 操作者以 `--selector 'canary=true'` 发起升级
- **THEN** 仅这 2 台收到升级任务，其余 8 台版本不变

### Requirement: Grace for running jobs
升级任务到达时若 agent 有运行中的 job，agent MUST 最多等待 2 分钟宽限；宽限内 job 结束则继续升级，宽限超时则升级 job 置为 FAILED 并上报，agent 绝不能终止用户 job 来完成升级。

#### Scenario: 宽限内任务结束则升级
- **GIVEN** agent 有一个预计 30 秒后结束的运行中 job，升级任务此时到达
- **WHEN** job 在 2 分钟宽限内结束
- **THEN** agent 开始执行升级

#### Scenario: 宽限超时放弃本次升级
- **GIVEN** agent 有运行中 job 且 2 分钟内未结束
- **WHEN** 宽限超时
- **THEN** 升级 job 为 FAILED 并提示存在运行任务，用户 job 与旧版本 agent 均不受影响

### Requirement: Guarded restart with health-gated rollback
替换与重启 MUST 由独立短生命周期的 guard 流程编排：替换二进制 → 重启服务 → 新版本须在 60 秒内完成本地自检并与 server 完成一次 mTLS 心跳；判定不健康时 guard MUST 自动恢复旧二进制并重启。回滚事件必须上报 server，升级任务总超时为 10 分钟。

#### Scenario: 新版本通过健康门控
- **GIVEN** 新版本二进制已通过验签与哈希校验
- **WHEN** 替换重启后新版本在 60 秒内自检通过并上报心跳
- **THEN** 升级 job 置为 SUCCEEDED，旧版本作为回滚副本保留

#### Scenario: 新版本启动失败自动回滚
- **GIVEN** 新版本启动后反复崩溃，或 60 秒内未通过健康判定
- **WHEN** guard 判定不健康
- **THEN** 自动恢复旧二进制并重启，旧版本恢复在线
- **AND** server 收到回滚事件，升级 job 为 FAILED

### Requirement: Downgrade rejection
Agent MUST 默认拒绝安装版本号低于当前运行版本的发布物；只有操作者发起升级时显式携带 allow-downgrade，agent 才允许降级安装。

#### Scenario: 默认拒绝降级
- **GIVEN** agent 当前运行 1.4.0，server 下发 1.3.0 且未带 allow-downgrade
- **WHEN** agent 比较版本号
- **THEN** 拒绝安装，升级 job 为 FAILED 并注明降级被拒
