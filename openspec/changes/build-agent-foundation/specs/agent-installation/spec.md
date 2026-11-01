## Purpose

本能力提供把一台空 Linux 机器变成受控 agent 的唯一标准路径：一条 install.sh 完成放置二进制、写配置、注册身份、安装并启动 systemd 服务，并固化全机文件布局，使安装结果可重复、可审计、可被自升级流程管理。

## ADDED Requirements

### Requirement: One-command guided installation
install.sh MUST 接收 server 地址、enrollment token、server CA 证书（三者必填）与初始标签，依次完成：释放/下载二进制、写配置、向 server 注册、安装并启动 systemd 单元。缺少任一必填参数时必须失败退出，且不得留下半安装状态。

#### Scenario: 参数齐全安装成功
- **GIVEN** 一台支持矩阵内的空 Linux 机器，以 root 执行 install.sh 且 server/token/CA 均有效
- **WHEN** 安装脚本执行完成
- **THEN** 二进制与配置就位、注册成功，cagent.service 处于 active 且已配置开机自启

#### Scenario: 缺少信任锚点直接失败
- **GIVEN** 执行 install.sh 时未提供 server CA 参数
- **WHEN** 脚本启动校验参数
- **THEN** 在发生任何注册动作之前失败退出，并提示该参数必填

### Requirement: Standard filesystem layout
安装结果 MUST 固定为：`/usr/local/bin/cagent`（二进制）、`/etc/cagent/config.yaml`（配置）、`/var/lib/cagent/`（身份、证书、旧版本副本、升级状态，仅 root 可读写）；agent 运行日志交由 journald 收集。

#### Scenario: 升级沿用标准布局
- **GIVEN** 一台按标准布局安装的 agent
- **WHEN** 自升级保留旧版本副本
- **THEN** 旧二进制保存在 /var/lib/cagent 下，且普通用户不可读、不可写

### Requirement: systemd lifecycle integration
cagent.service MUST 配置开机自启与异常退出自动重启（Restart=always）。guard 执行升级期间由 guard 负责服务重启，服务管理层不得额外拦截或重启到半替换状态的二进制。

#### Scenario: 进程崩溃被自动拉起
- **GIVEN** cagent.service 正在运行
- **WHEN** agent 进程异常退出
- **THEN** systemd 自动重启该服务，agent 重新连接 server 并恢复在线

### Requirement: Supported platform matrix
首期支持平台 MUST 限定为使用 systemd + glibc 的 Ubuntu 20.04+、Debian 11+、RHEL 8+，架构为 amd64 与 arm64；在不满足条件的环境中安装时，脚本必须明确报错而不是继续执行。

#### Scenario: 不支持环境报错退出
- **GIVEN** 一台无 systemd 的 Linux 环境
- **WHEN** 执行 install.sh
- **THEN** 脚本检测失败并明确提示平台不受支持，不做任何安装改动
