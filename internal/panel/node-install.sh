#!/bin/sh
set -eu
umask 077

[ "$(uname -s)" = Linux ] && [ "$(uname -m)" = x86_64 ] || { echo '仅支持 Linux amd64' >&2; exit 1; }
[ "$(id -u)" = 0 ] || { echo '请以 root 运行安装命令' >&2; exit 1; }
command -v systemctl >/dev/null
command -v curl >/dev/null
command -v useradd >/dev/null

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
authority=${control_url#https://}
case "$authority" in ''|*[!a-zA-Z0-9.:-]*|:*|-*) echo '控制地址只支持 HTTPS 域名或 IPv4 与可选端口，不带路径' >&2; exit 1 ;; esac
[ "${#ticket}" = 43 ] || { echo '安装票据格式无效' >&2; exit 1; }
case "$ticket" in *[!a-zA-Z0-9_-]*) echo '安装票据格式无效' >&2; exit 1 ;; esac

# 在下载和兑换票据前检查所有安装/更新路径，避免先消费票据才发现不安全路径。
for path in /usr/local /usr/local/bin /usr/local/libexec /var/lib /etc/systemd/system /var/lib/nbpass-update /usr/local/bin/.nbpass-update; do
  [ ! -L "$path" ] || { echo '拒绝安装/更新目录符号链接' >&2; exit 1; }
  if [ -e "$path" ]; then
    [ -d "$path" ] && [ "$(stat -c %u "$path")" = 0 ] || { echo '安装/更新目录须归 root 所有' >&2; exit 1; }
    [ -z "$(find "$path" -prune \( -perm -0020 -o -perm -0002 \) -print)" ] || { echo '安装/更新目录不可由其他用户写入' >&2; exit 1; }
  fi
done
for path in /usr/local/bin/nbpass-node /usr/local/libexec/nbpass-update /etc/systemd/system/nbpass-node.service /etc/systemd/system/nbpass-update.service; do
  [ ! -L "$path" ] || { echo '拒绝覆盖符号链接' >&2; exit 1; }
  [ ! -e "$path" ] || [ -f "$path" ] || { echo '安装路径不是普通文件' >&2; exit 1; }
done
[ ! -L /var/lib/nbpass-node ] || { echo '拒绝节点缓存目录符号链接' >&2; exit 1; }
if [ -e /var/lib/nbpass-node ]; then
  [ -d /var/lib/nbpass-node ] || { echo '节点缓存路径不是目录' >&2; exit 1; }
  [ -z "$(find /var/lib/nbpass-node ! -type d ! -type f -print)" ] || { echo '缓存目录含链接或特殊文件' >&2; exit 1; }
fi
id nbpass-node >/dev/null 2>&1 || useradd --system --user-group --no-create-home --shell /usr/sbin/nologin nbpass-node
install -d -m 0700 /var/lib/nbpass-node
install -d -m 0755 /usr/local/bin /usr/local/libexec
staging=$(mktemp -d /usr/local/bin/.nbpass-install-XXXXXX)
trap 'rm -rf "$staging"' EXIT
trap 'exit 1' HUP INT TERM
curl --proto '=https' --proto-redir '=https' --tlsv1.2 -fsSL __NODE_DOWNLOAD_URL__ -o "$staging/nbpass-node"
chmod 0700 "$staging/nbpass-node"
# 不把票据写入 unit；兑换失败时保留原有程序和服务。
"$staging/nbpass-node" -control-url "$control_url" -ticket "$ticket" -data-dir /var/lib/nbpass-node -enroll-only
install -m 0755 "$staging/nbpass-node" "$staging/node-ready"
chown root:root "$staging/node-ready"
mv -f "$staging/node-ready" /usr/local/bin/nbpass-node
chown -hR nbpass-node:nbpass-node /var/lib/nbpass-node
find /var/lib/nbpass-node -type d -exec chmod 0700 {} \;
find /var/lib/nbpass-node -type f -exec chmod 0600 {} \;
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
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
EOF
install -m 0644 "$staging/nbpass-node.service" /etc/systemd/system/nbpass-node.service
chown root:root /etc/systemd/system/nbpass-node.service
__UPDATE_INSTALL__
systemctl daemon-reload
systemctl enable nbpass-node.service nbpass-update.service
systemctl restart nbpass-update.service
systemctl restart nbpass-node.service
echo '节点已安装，请在面板确认在线状态。'
