# cagent

多角色运维 Agent（multi-role operations agent）：装在 Linux 服务器上的单个静态二进制，
通过 **mTLS 双向认证** 向中心 Server 注册，接受远程任务执行，并支持**签名自升级与失败自动回滚**。

类似 Puppet Agent（配置管理）+ Zabbix Agent（监控采集）+ 运维控制通道的组合，
定位为多机运维体系中的受控节点核心。

- 单一二进制 `cagent`，子命令 `agent` / `server` / `ctl`
- 零外部服务依赖（内置 SQLite + CA），一个 server 进程即可支撑上千台 agent
- 所有远程操作经 mTLS 认证、标签选机、全程审计
- 升级包 ed25519 签名校验，新版本 60 秒健康门控失败自动换回旧版

---

## 架构

```
┌──────────────┐   9443 enrollment（单向 TLS + 一次性 token）
│              │ ────────────────────────────────────────────────┐
│              │   9444 control（mTLS 双向流：心跳/任务/输出/升级） │
│   server     │ ◄──────────────────────────────────────────────► │  agent (systemd)
│ (SQLite+CA)  │                                                   │  /usr/local/bin/cagent
│              │   9445 admin（TLS + Bearer token，仅 ctl）        │
└──────┬───────┘ ◄───────────────────────────────────────────────┘
       │
   cagent ctl（token/agent/job/release/upgrade 管理）
```

- **EnrollmentService（9443）**：仅注册可达，一次性 token + 服务器 CA 锚点，不提供任何 insecure 选项
- **ControlService（9444）**：agent 证书强制校验，吊销即时踢线；一条双向流承载心跳、任务下发/事件、升级指令/下载
- **ManagementService（9445）**：管理面，全部方法经 admin token 拦截器鉴权

可靠性设计：agent 断线指数退避自动重连；离线任务在重连后按顺序补投；排队任务 24h TTL 过期；
任务终态机 CAS 防回退；同 job_id 重发幂等不重复执行。

## 能力清单

| 能力 | 说明 |
|---|---|
| 注册接入 | 一次性 enrollment token（可复用、TTL、吊销）、UUID 身份、证书自动续签 |
| 心跳与在线状态 | 15s 心跳、45s 判离线、标签/版本全集上报 |
| 远程执行 | `/bin/sh -c`、工作目录/环境变量、默认 5 分钟（硬上限 60 分钟）、超时/取消以 **SIGKILL 杀整个进程组** |
| 输出回收 | stdout/stderr 分流，单任务输出上限 1MB（超限截断标记） |
| 幂等与补投 | at-least-once 投递 + 终态结果缓存，重发同一 job 不重复执行 |
| 审计 | server 端 audit_logs（操作者/命令/退出码/标签）+ agent 本地 JSONL 留痕 |
| 标签灰度 | 任务/升级均按标签 AND 选机，如 `env=prod,role=db` |
| 签名自升级 | tar.gz 包 + manifest，**大小→SHA256→ed25519 三重验真** |
| 健康门控回滚 | 替换→重启→新版本 60s 内 mTLS 心跳成功才提交；失败独立 guard 自动换回旧版重启 |
| 防降级 | 语义版本比较，默认拒绝降级，仅显式 `--allow-downgrade` 放行 |
| 升级 grace | 升级到达时等待运行中任务最多 2 分钟，绝不杀用户任务 |
| 管理 CLI | token / agent / job / release / upgrade 全套命令 |

平台：systemd + glibc，linux/amd64、linux/arm64，CGO 纯静态（sqlite 为纯 Go 驱动）。

---

## 快速开始

### 1. 构建

```bash
make build            # 本机二进制 dist/cagent
make build-linux      # linux/amd64 + linux/arm64 静态二进制
make test-race        # 全量单测（-race）
make package VERSION=1.2.3   # 生成可签名的升级包
```

需要 Go 1.27+。重新生成 gRPC 代码需 `protoc` + `protoc-gen-go(-grpc)`（见 `proto/README.md`，
生成的 `*.pb.go` 已入库，普通构建不依赖 protoc）。

### 2. 启动 server

```bash
sudo cagent server --config /etc/cagent/server.yaml
```

```yaml
# /etc/cagent/server.yaml
enrollment_listen: ":9443"
control_listen:    ":9444"
admin_listen:      ":9445"
data_dir: "/var/lib/cagent-server"
server_cert_hosts: ["server.example.com", "10.0.0.1"]
```

首启自动生成：内置 CA 与服务器证书（`data_dir/ca/`）、SQLite 库、
**admin token**（`data_dir/admin_token`，0600，仅此时可读）。

### 3. 配置 ctl 并创建安装令牌

```bash
mkdir -p ~/.config/cagent
cat > ~/.config/cagent/ctl.yaml <<EOF
admin_address: "server.example.com:9445"
admin_token:   "$(sudo cat /var/lib/cagent-server/admin_token)"
server_ca_file: "/etc/cagent/ca.crt"
EOF

cagent ctl token create --ttl 24 --description "batch-db"
# token（明文仅此一次显示）:
# cagt_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
# id=et_xxx 过期=...
```

也可用环境变量 `CAGENT_ADMIN_ADDR` / `CAGENT_ADMIN_TOKEN`。

### 4. 安装 agent

把 `cagent`（对应架构）、`deploy/cagent.service`、server CA 与安装脚本放到目标机：

```bash
sudo ./scripts/install.sh \
  --enrollment-addr server.example.com:9443 \
  --control-addr    server.example.com:9444 \
  --token           cagt_xxxx \
  --server-ca       ca.crt \
  --label env=prod --label role=db
```

安装脚本完成：平台检测（root/systemd/glibc/架构）→ 二进制落 `/usr/local/bin/cagent`
→ 写 0600 配置 → 装 systemd 单元 → `enable + start`，失败自动回滚不留半安装。

或手写配置：

```yaml
# /etc/cagent/config.yaml
enrollment_address: "server.example.com:9443"
control_address:    "server.example.com:9444"
enrollment_token:   "cagt_xxxx"
server_ca_file:     "/etc/cagent/ca.crt"   # 或 server_ca_fingerprint
data_dir: "/var/lib/cagent"
labels:
  env: prod
  role: db
```

### 5. 使用

```bash
# 查看机器
cagent ctl agent list
cagent ctl agent show <uuid>

# 按标签下发任务（AND）并跟踪
cagent ctl job run --selector env=prod --selector role=db --timeout 60 'uptime'
cagent ctl job show <job-id>
cagent ctl job list --status running
cagent ctl job cancel <job-id>

# 发布与升级
cagent ctl release keygen --key release.key --pub release.pub  # 首次：离线生成 ed25519 密钥对
cagent ctl release package --binary dist/cagent-linux-amd64 --version 1.2.3 \
  --out cagent-1.2.3.pkg
cagent ctl release sign --key release.key --pkg cagent-1.2.3.pkg
cagent ctl release publish --pkg cagent-1.2.3.pkg --sig cagent-1.2.3.pkg.sig
cagent ctl release list

cagent ctl upgrade --release 1.2.3 --selector env=canary
cagent ctl upgrade --release 1.2.3 --selector env=prod --allow-downgrade
```

token 管理：`cagent ctl token list | revoke <id>`。

---

## 自升级与回滚机制

```
ctl upgrade ─► server 为每台匹配 agent 创建 upgrade job（复用排队/补投/幂等）
                    │
agent 收到 UpgradeCommand
  1. 版本门控（默认拒绝降级）
  2. grace：等待运行中任务结束（≤2 分钟，不强杀）
  3. mTLS 流式下载 → 校验 大小 → SHA256 → ed25519 签名，任一不符拒装
  4. 暂存 → 备份当前二进制 → 原子替换
  5. detach 独立 guard 进程（Setsid）→ systemctl restart
  6. 新版本启动 → 首次 mTLS 心跳成功写健康标记
  7. guard 在 60s 内观察到标记 → 清理旧副本，上报 SUCCEEDED
     未观察到（崩溃/无心跳）→ 换回旧二进制 → 重启 → 上报 ROLLED_BACK
```

设计要点：升级最终结果由**重启后的 agent 经自己的稳定主连接**上报（写
`upgrade-result.json`），避免 guard 短连接与 agent 重连竞争控制流。

---

## 目录结构（agent 侧）

```
/usr/local/bin/cagent              二进制（0755）
/etc/cagent/config.yaml            配置（0600）
/etc/cagent/ca.crt                 server CA 锚点
/var/lib/cagent/                   状态根（0700）
├── identity / agent.key / agent.crt / ca.crt
├── audit.log                      本地执行审计 JSONL
├── upgrade-healthy                新版本健康标记
├── upgrade-result.json            guard → agent 的待上报结果
└── releases/                      cagent.staged / cagent.old
```

server 侧数据在 `/var/lib/cagent-server/`（SQLite、CA、发布包 `releases/`）。

## 测试

```bash
make test-race      # 单元测试（选择器/状态机/执行器进程组/幂等/版本/CA/guard 决策…）
go test ./test/e2e  # 端到端：本机起真 server+agent 双进程，覆盖
                    #   mTLS 注册上线 / run 输出退出码 / cancel /
                    #   离线扫描+补投 / 自升级全链路 / 降级拒绝

test/e2e/docker_systemd_test.sh    # Ubuntu 容器内验证安装布局与 Restart=always
```

e2e 用临时数据目录与 loopback 端口，无需外部依赖；自升级用例通过注入
`restart_command` 模拟 systemctl（生产为 `systemctl restart cagent.service`）。

## 仓库布局

```
cmd/cagent              入口（agent/server/ctl/--guard-mode）
internal/
  agent/                identity trust register control executor
                        jobworker audit upgrade（下载/验签/guard/状态机）
  server/               ca enrollservice controlservice dispatch
                        management enrollment admin store(SQLite)
  ctl/                  管理 CLI
  config/ logging/ paths/ version/
  release/              打包/manifest/ed25519 签名/语义版本
proto/cagent/v1/        gRPC 契约（enrollment/control/management）
deploy/                 systemd 单元
scripts/install.sh      一键安装
test/e2e/               双进程端到端测试
openspec/               需求、规格与变更记录（规格驱动开发）
```

## 安全边界

- 强制 mTLS（注册端口也要求服务器证书），**没有也不会提供 `--insecure`**
- 信任锚点只来自安装参数（CA PEM 或 SHA256 指纹）
- 升级包必须通过 ed25519 签名校验，公钥随 agent 安装分发
- admin/enrollment token 文件 0600；agent 以 root 运行（远程运维需要），
  systemd 单元开启 `NoNewPrivileges`/`ProtectSystem`/`PrivateTmp`
- 每条远程命令双端审计；取消/超时按进程组 SIGKILL，不留孤儿

## License

见仓库 LICENSE。
