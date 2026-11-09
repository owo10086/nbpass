#!/bin/sh
set -eu
umask 077

[ "$(uname -s)" = Linux ] && [ "$(uname -m)" = x86_64 ] || { echo '仅支持 Linux amd64' >&2; exit 1; }
[ "$(id -u)" = 0 ] || { echo '请以 root 运行安装命令' >&2; exit 1; }
command -v systemctl >/dev/null
command -v curl >/dev/null

control_url=''
ticket=''
while [ "$#" -gt 0 ]; do
  [ "$#" -ge 2 ] || { echo '安装参数不完整' >&2; exit 1; }
  case "$1" in
    -control-url) control_url=$2 ;;
    -ticket) ticket=$2 ;;
    *) echo '未知安装参数' >&2; exit 1 ;;
  esac
  shift 2
done
# systemd 参数不经过 Shell；限制端点字符，避免插入 unit 指令或转义符。
case "$control_url" in https://*) ;; *) echo '控制地址必须使用 HTTPS' >&2; exit 1 ;; esac
case "$control_url" in *[!a-zA-Z0-9.:/-]*) echo '控制地址含不支持的字符' >&2; exit 1 ;; esac
[ -n "$ticket" ] || { echo '缺少安装票据' >&2; exit 1; }

id nbpass-node >/dev/null 2>&1 || useradd --system --no-create-home --shell /usr/sbin/nologin nbpass-node
install -d -m 0700 /var/lib/nbpass-node
[ ! -L /usr/local/bin/nbpass-node ] && [ ! -L /etc/systemd/system/nbpass-node.service ] || { echo '拒绝覆盖符号链接' >&2; exit 1; }
[ ! -L /usr/local/libexec ] || { echo '拒绝安装暂存目录符号链接' >&2; exit 1; }
install -d -m 0755 /usr/local/libexec
staging=$(mktemp -d /usr/local/libexec/.nbpass-install-XXXXXX)
trap 'rm -rf "$staging"' EXIT HUP INT TERM
curl --proto '=https' --proto-redir '=https' --tlsv1.2 -fsSL __NODE_DOWNLOAD_URL__ -o "$staging/nbpass-node"
chmod 0700 "$staging/nbpass-node"
# 不把票据写入 unit；兑换失败时保留原有程序和服务。
"$staging/nbpass-node" -control-url "$control_url" -ticket "$ticket" -data-dir /var/lib/nbpass-node -enroll-only
install -m 0755 "$staging/nbpass-node" /usr/local/bin/nbpass-node
chown -hR nbpass-node:nbpass-node /var/lib/nbpass-node
cat > "$staging/nbpass-node.service" <<EOF
[Unit]
Description=nbpass node
After=network-online.target
Wants=network-online.target

[Service]
User=nbpass-node
Group=nbpass-node
AmbientCapabilities=CAP_NET_BIND_SERVICE
CapabilityBoundingSet=CAP_NET_BIND_SERVICE
ExecStart=/usr/local/bin/nbpass-node -control-url $control_url -data-dir /var/lib/nbpass-node
Restart=on-failure
RestartSec=5
UMask=0077
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
RuntimeDirectory=nbpass-node
RuntimeDirectoryMode=0700
ReadWritePaths=/var/lib/nbpass-node /run/nbpass-node

[Install]
WantedBy=multi-user.target
EOF
install -m 0644 "$staging/nbpass-node.service" /etc/systemd/system/nbpass-node.service
__UPDATE_INSTALL__
systemctl daemon-reload
systemctl enable nbpass-node.service nbpass-update.service
systemctl restart nbpass-update.service
systemctl restart nbpass-node.service
echo '节点已安装，请在面板确认在线状态。'
