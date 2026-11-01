## Purpose

本能力让操作者经 server 向一批选定 agent 异步下发 shell 任务，并可靠回收输出与退出状态，是「运维核心」的动手能力。所有执行以 root 身份进行，因此每一次执行都必须留下包含操作者、命令全文与结果的审计记录，使任何远程操作事后可追溯。

## ADDED Requirements

### Requirement: Label-targeted asynchronous job dispatch
操作者通过标签选择器（多个 `key=value`，AND 语义）圈选目标；server MUST 为每台匹配 agent 创建一个独立 job 并立即返回其 job 标识。无任何 agent 匹配时必须明确告知，不允许静默成功。

#### Scenario: 按标签批量下发
- **GIVEN** 3 台 agent 的标签同时满足 env=prod 与 role=db
- **WHEN** 操作者以 `--selector 'env=prod,role=db'` 下发命令
- **THEN** server 创建 3 个独立 job 并返回各自标识

#### Scenario: 无匹配目标
- **GIVEN** 没有任何 agent 满足选择器
- **WHEN** 操作者下发 job
- **THEN** 命令明确返回「0 个目标」，不创建任何 job

### Requirement: Shell execution model
Agent MUST 以 root 身份经 `/bin/sh -c` 执行 job 命令，并支持指定工作目录与追加环境变量。单 job 默认超时 5 分钟、可按 job 配置，硬上限 60 分钟；单 agent 默认并发 4（可配置），超出的 job 在 agent 本地排队执行。

#### Scenario: 正常执行并返回退出码
- **GIVEN** 一个命令为 `echo ok` 的 job
- **WHEN** agent 执行该 job
- **THEN** job 状态为 SUCCEEDED、退出码 0，输出中包含 ok

#### Scenario: 超时终止整个进程
- **GIVEN** 一个运行 10 分钟的 job，其超时配置为 1 分钟
- **WHEN** 执行超过超时时间
- **THEN** agent 终止该 job 的整个进程组并将状态置为 TIMEOUT

### Requirement: Streaming output with truncation
Job 的 stdout 与 stderr MUST 流式回传 server 并持久化；单个 job 输出总量上限 1MB，超出部分必须截断且任务记录带 truncated 标记。

#### Scenario: 大输出被截断
- **GIVEN** 一个产出 2MB stdout 的 job
- **WHEN** job 执行完成
- **THEN** 保存的输出不超过 1MB 且该 job 标记 truncated=true

### Requirement: Job cancellation
操作者可以取消尚未进入终态的 job；agent 收到取消信号后 MUST 终止该 job 的整个进程组并将状态置为 CANCELED，不得遗留孤儿进程。

#### Scenario: 取消运行中任务
- **GIVEN** 一个正在运行、派生子进程的 job
- **WHEN** 操作者执行 job cancel
- **THEN** 命令及其子进程随进程组全部终止，job 状态为 CANCELED

### Requirement: Job terminal state machine
每个 job 的状态 MUST 沿 PENDING、RUNNING 与终态 {SUCCEEDED, FAILED, TIMEOUT, CANCELED, EXPIRED} 合法流转；终态必须记录退出码（适用时）且不可回退。

#### Scenario: 非零退出标记失败
- **GIVEN** job 命令以退出码 2 结束
- **WHEN** 进程结束并回传
- **THEN** job 状态为 FAILED 且记录退出码 2

### Requirement: Execution audit trail
每个 job 的下发与结束 MUST 生成审计记录，至少包含：操作者、时间、目标 agent、命令全文、终态、退出码与输出摘要。审计记录在 server 持久化，agent 本地同时保留执行留痕。

#### Scenario: 事后追溯一次执行
- **GIVEN** 任一已结束的 job
- **WHEN** 操作者或审计人员查询审计记录
- **THEN** 可查到操作者、目标 agent、完整命令、时间与结果摘要
