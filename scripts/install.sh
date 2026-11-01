#!/bin/sh
# cagent 一键安装脚本（systemd + glibc，amd64/arm64）。
#
# 用法:
#   install.sh \
#     --enrollment-addr host:9443 \
#     --control-addr host:9444 \
#     --token cagt_xxx \
#     (--server-ca ca.pem | --fingerprint <sha256-hex>) \
#     [--label k=v]... [--binary path]
#
# 不存在也不会提供任何 --insecure 选项。
set -eu

BINARY_SRC=""
ENROLLMENT_ADDR=""
CONTROL_ADDR=""
TOKEN=""
CA_FILE=""
FINGERPRINT=""
LABELS=""

usage() {
	cat >&2 <<'USAGE'
参数:
  --enrollment-addr <host:port>  注册服务地址（必填）
  --control-addr    <host:port>  控制服务地址（必填）
  --token           <cagt_...>   一次性引导令牌（必填）
  --server-ca       <file.pem>   server CA 证书（与 --fingerprint 二选一）
  --fingerprint     <sha256-hex> server 证书 SHA256 指纹（与 --server-ca 二选一）
  --label           <k=v>        静态标签，可重复
  --binary          <path>       待安装二进制（默认脚本目录下的 cagent）
USAGE
	exit 2
}

while [ "$#" -gt 0 ]; do
	case "$1" in
		--enrollment-addr) ENROLLMENT_ADDR="$2"; shift 2 ;;
		--control-addr)    CONTROL_ADDR="$2"; shift 2 ;;
		--token)           TOKEN="$2"; shift 2 ;;
		--server-ca)       CA_FILE="$2"; shift 2 ;;
		--fingerprint)     FINGERPRINT="$2"; shift 2 ;;
		--label)
			if [ -n "$LABELS" ]; then LABELS="$LABELS
$2"; else LABELS="$2"; fi
			shift 2 ;;
		--binary)          BINARY_SRC="$2"; shift 2 ;;
		-h|--help)         usage ;;
		*) echo "未知参数: $1" >&2; usage ;;
	esac
done

fail() { echo "安装失败: $*" >&2; exit 1; }

[ -n "$ENROLLMENT_ADDR" ] || fail "--enrollment-addr 必填"
[ -n "$CONTROL_ADDR" ]    || fail "--control-addr 必填"
[ -n "$TOKEN" ]           || fail "--token 必填"
if [ -z "$CA_FILE" ] && [ -z "$FINGERPRINT" ]; then
	fail "必须提供 --server-ca 或 --fingerprint 作为信任锚点"
fi
if [ -n "$CA_FILE" ] && [ -n "$FINGERPRINT" ]; then
	fail "--server-ca 与 --fingerprint 二选一"
fi

# ---- 平台检测 ----
[ "$(id -u)" = "0" ] || fail "需要 root 运行"
command -v systemctl >/dev/null 2>&1 || fail "未找到 systemctl（需要 systemd）"
command -v ldd >/dev/null 2>&1 || fail "未找到 ldd（需要 glibc 环境）"
ldd --version 2>&1 | grep -qi glibc || fail "仅支持 glibc 发行版"

arch="$(uname -m)"
case "$arch" in
	x86_64|amd64) ;;
	aarch64|arm64) ;;
	*) fail "不支持的架构: $arch（仅 amd64/arm64）" ;;
esac

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
[ -n "$BINARY_SRC" ] || BINARY_SRC="$SCRIPT_DIR/cagent"
[ -f "$BINARY_SRC" ] || fail "二进制不存在: $BINARY_SRC"

# ---- 布局 ----
BIN_DST=/usr/local/bin/cagent
CONF_DST=/etc/cagent/config.yaml
CA_DST=/etc/cagent/ca.crt
UNIT_SRC="$SCRIPT_DIR/../deploy/cagent.service"
UNIT_DST=/etc/systemd/system/cagent.service

ROLLBACK_FILES=""
cleanup() {
	rc=$?
	if [ "$rc" -ne 0 ]; then
		echo "执行回滚..." >&2
		for f in $ROLLBACK_FILES; do rm -f "$f"; done
		systemctl reset-failed cagent.service >/dev/null 2>&1 || true
	fi
}
trap cleanup EXIT INT TERM

install -d -m 0755 /etc/cagent
install -d -m 0700 /var/lib/cagent
install -d -m 0700 /var/lib/cagent/releases

# 信任锚点
if [ -n "$CA_FILE" ]; then
	[ -f "$CA_FILE" ] || fail "CA 文件不存在: $CA_FILE"
	install -m 0644 "$CA_FILE" "$CA_DST"
	ROLLBACK_FILES="$ROLLBACK_FILES $CA_DST"
	CA_LINE="server_ca_file: \"$CA_DST\""
else
	CA_LINE="server_ca_fingerprint: \"$FINGERPRINT\""
fi

# 标签 YAML
LABEL_YAML="{}"
if [ -n "$LABELS" ]; then
	LABEL_YAML=$(printf '%s\n' "$LABELS" | awk -F= '
		BEGIN { print "labels:" }
		NF==2 { gsub(/"/,"\\\"",$2); printf "  %s: \"%s\"\n", $1, $2 }')
fi

# 二进制（先落临时文件再替换，失败不动旧版）
TMP_BIN="${BIN_DST}.new.$$"
install -m 0755 "$BINARY_SRC" "$TMP_BIN"
ROLLBACK_FILES="$ROLLBACK_FILES $TMP_BIN"
"$TMP_BIN" --version >/dev/null 2>&1 || fail "二进制无法执行（架构不匹配或损坏）"
mv -f "$TMP_BIN" "$BIN_DST"
ROLLBACK_FILES="$ROLLBACK_FILES $BIN_DST"

# 配置（0600，不写令牌到日志）
umask 077
cat > "$CONF_DST" <<EOF
# 由 install.sh 生成
enrollment_address: "$ENROLLMENT_ADDR"
control_address: "$CONTROL_ADDR"
enrollment_token: "$TOKEN"
$CA_LINE
data_dir: "/var/lib/cagent"
job_concurrency: 4
$LABEL_YAML
EOF
chmod 0600 "$CONF_DST"
ROLLBACK_FILES="$ROLLBACK_FILES $CONF_DST"

# systemd 单元
if [ -f "$UNIT_SRC" ]; then
	install -m 0644 "$UNIT_SRC" "$UNIT_DST"
else
	cat > "$UNIT_DST" <<'UNIT'
[Unit]
Description=cagent
After=network-online.target
Wants=network-online.target
[Service]
Type=simple
ExecStart=/usr/local/bin/cagent agent --config /etc/cagent/config.yaml
Restart=always
RestartSec=2s
[Install]
WantedBy=multi-user.target
UNIT
fi
ROLLBACK_FILES="$ROLLBACK_FILES $UNIT_DST"

systemctl daemon-reload
systemctl enable cagent.service >/dev/null 2>&1
systemctl restart cagent.service

sleep 2
systemctl is-active --quiet cagent.service || fail "服务未进入 active（journalctl -u cagent 查看）"

echo "安装成功：$BIN_DST（配置 $CONF_DST），服务 cagent.service active"
echo "首次注册将在启动后自动进行；用 cagent ctl agent list 确认上线"
trap - EXIT INT TERM
exit 0
