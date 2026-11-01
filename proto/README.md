# proto 契约

`cagent.v1` 定义 agent 与 server 之间的全部通信契约。

## 服务划分

| 服务 | 端口/TLS | 说明 |
|---|---|---|
| `EnrollmentService.Enroll` | 注册端口，服务器单向 TLS + enrollment token | 仅注册可达，凭 token + CSR 换首份证书 |
| `ControlService.Control` | mTLS 双向流 | agent 主动出站，多路复用心跳/任务/升级 |
| `ControlService.RenewCert` | mTLS unary | 证书临期续签 |
| `ControlService.DownloadRelease` | mTLS server-streaming | 升级包分片下载 |

## 重新生成代码

工具（版本以实际使用为准）：

```bash
brew install protobuf
go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
```

生成（仓库根目录执行，`$GOPATH/bin` 需在 PATH）：

```bash
make proto
```

生成物 `*.pb.go` / `*_grpc.pb.go` 与 `.proto` 同目录并**提交入库**，使普通构建不依赖 protoc。修改契约后必须重新生成并在同一任务内提交。

服务划分：EnrollmentService（注册端口，仅 Enroll）、ControlService（mTLS 双向流 + RenewCert + DownloadRelease）、ManagementService（admin 端口，全部经 admin token 拦截器：token/agent/job/release/upgrade 管理）。
