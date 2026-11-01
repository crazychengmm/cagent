## Purpose

本能力在不可靠网络下维持 agent 与 server 的实时指令通道，并保证「指令不丢、执行不重」：agent 主动出站长连接、心跳判活、断线指数退避自动重连；离线期间指令在 server 侧排队，重连后补投；配合以 job_id 为键的幂等去重，杜绝同一任务被重复执行。

## ADDED Requirements

### Requirement: Outbound persistent connection and reconnect
Agent MUST 仅以主动出站方式与 server 建立 gRPC 长连接，server 不得主动向 agent 发起网络连接。连接断开时 agent MUST 以指数退避自动重连，重连成功后恢复全部指令通道。

#### Scenario: 断网恢复后自动重连
- **GIVEN** agent 与 server 的 mTLS 长连接正常
- **WHEN** 网络中断 2 分钟后恢复
- **THEN** agent 经退避重试自动重新建立连接并恢复在线，全程无需人工干预

### Requirement: Heartbeat and online status
Agent MUST 每 15 秒发送一次心跳，心跳携带 UUID、agent 版本与当前标签全集。server 在连续 45 秒未收到某 agent 心跳时 MUST 将其标记为 offline，恢复心跳后标记为 online。

#### Scenario: 失联判定与恢复
- **GIVEN** 某 agent 处于 online 状态
- **WHEN** server 连续 45 秒未收到其心跳
- **THEN** agent list/show 中该 agent 状态为 offline，最后心跳时间停止更新
- **AND** 心跳恢复后状态回到 online

#### Scenario: 标签变更经心跳同步
- **GIVEN** agent 配置文件中的标签被修改并重启
- **WHEN** agent 重新连接并发出心跳
- **THEN** server 侧该 agent 的标签更新为心跳所带全集

### Requirement: Offline queueing and redelivery
Agent 离线期间下达的 job（含升级任务）MUST 在 server 侧排队；agent 重连后排队任务 MUST 被补投。排队超过 24 小时未投递的任务 MUST 置为 EXPIRED 终态且不再投递。

#### Scenario: 离线任务重连补投
- **GIVEN** agent 处于 offline
- **WHEN** 操作者对其下发一个 job，且 agent 在 24 小时内重连
- **THEN** 重连后 agent 收到并执行该 job，操作者可查到执行结果

#### Scenario: 超时任务过期
- **GIVEN** 一个发给离线 agent 的 job 在队列中已超过 24 小时
- **WHEN** server 扫描到该排队任务
- **THEN** 任务状态置为 EXPIRED 且永远不再投递

### Requirement: Idempotent job delivery
Server 与 agent 之间采用 at-least-once 投递语义；agent MUST 以 job_id 做幂等去重，同一个 job_id 绝不能被执行两次。

#### Scenario: 重复投递不重复执行
- **GIVEN** agent 已执行完 job J 并回传结果
- **WHEN** server 因未收到确认而重发 job J
- **THEN** agent 不再次执行命令，直接回传 job J 的既有结果与终态
