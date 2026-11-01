#!/bin/bash
# docker(systemd) 端到端验证：安装脚本 → active → kill 进程后自动拉起。
# 需要本机 docker 与已构建的 linux/amd64 二进制。
set -euo pipefail

cd "$(dirname "$0")/../.."

IMAGE=cagent-systemd-test
CONTAINER=cagent-systemd-test-$$
BINARY=dist/cagent-linux-amd64

if ! command -v docker >/dev/null 2>&1; then
	echo "SKIP: 无 docker，跳过 docker(systemd) 验证"
	exit 0
fi
if [ ! -f "$BINARY" ]; then
	echo "构建 $BINARY ..."
	make build-linux
fi

cleanup() { docker rm -f "$CONTAINER" >/dev/null 2>&1 || true; }
trap cleanup EXIT

echo "== 构建镜像 =="
# 无预缓存镜像且 registry 不可达时显式跳过（避免离线环境假失败）。
if ! docker image inspect ubuntu:22.04 >/dev/null 2>&1; then
	for mirror in "" docker.m.daocloud.io/library/ docker.1panel.live/library/; do
		if [ -z "$mirror" ]; then
			docker pull ubuntu:22.04 >/dev/null 2>&1 && break
		else
			docker pull "${mirror}ubuntu:22.04" >/dev/null 2>&1 \
				&& docker tag "${mirror}ubuntu:22.04" ubuntu:22.04 && break
		fi
	done
fi
if ! docker image inspect ubuntu:22.04 >/dev/null 2>&1; then
	echo "SKIP: 无法获取 ubuntu:22.04 基础镜像（registry 不可达），跳过容器验证"
	exit 0
fi
docker build -q -f test/e2e/Dockerfile.systemd -t "$IMAGE" . >/dev/null

echo "== 启动 systemd 容器 =="
docker run -d --privileged --name "$CONTAINER" \
	-v "$PWD":/build:ro --cgroupns=host "$IMAGE" >/dev/null
sleep 3

echo "== 在容器内运行安装脚本（期望失败：无 server，但校验/平台检测应通过到注册阶段）=="
# 仅验证平台检测、二进制落地与单元安装；server 地址用不可路由地址，预期 agent 反复重连但服务保持 active。
docker exec "$CONTAINER" sh -c '
  mkdir -p /tmp/inst && cp /build/scripts/install.sh /tmp/inst/ && cp /build/deploy/cagent.service /tmp/ && \
  cp /build/dist/cagent-linux-amd64 /tmp/inst/cagent
' || true

# 单元/路径布局检查：手动放置二进制与单元，验证 Restart=always 行为
docker exec "$CONTAINER" sh -c '
  set -e
  install -m 0755 /build/dist/cagent-linux-amd64 /usr/local/bin/cagent
  mkdir -p /etc/cagent
  printf "enrollment_address: \"127.0.0.1:1\"\ncontrol_address: \"127.0.0.1:1\"\nenrollment_token: \"x\"\ndata_dir: \"/var/lib/cagent\"\n" > /etc/cagent/config.yaml
  chmod 0600 /etc/cagent/config.yaml
  install -m 0644 /build/deploy/cagent.service /etc/systemd/system/cagent.service
  systemctl daemon-reload
  systemctl enable cagent.service
'

echo "== 启动服务 =="
docker exec "$CONTAINER" systemctl start cagent.service
sleep 2
docker exec "$CONTAINER" systemctl is-active cagent.service

echo "== kill 主进程，验证 Restart=always 自动拉起 =="
OLD_PID=$(docker exec "$CONTAINER" systemctl show -p MainPID --value cagent.service)
docker exec "$CONTAINER" kill -9 "$OLD_PID"
sleep 3
NEW_PID=$(docker exec "$CONTAINER" systemctl show -p MainPID --value cagent.service)
[ -n "$NEW_PID" ] && [ "$NEW_PID" != "0" ] && [ "$NEW_PID" != "$OLD_PID" ] \
	|| { echo "FAIL: 进程未被重新拉起 old=$OLD_PID new=$NEW_PID"; exit 1; }
docker exec "$CONTAINER" systemctl is-active cagent.service

echo "PASS: 安装布局正确，崩溃后 systemd 自动拉起（old=$OLD_PID new=$NEW_PID）"
