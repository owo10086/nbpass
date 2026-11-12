#!/bin/sh
set -eu
umask 077

stage='环境检查'
staging=''
update_staging=''
enrollment_attempted=0
started_at=$(date +%s)
step() {
  stage=$2
  printf '\n[%s/6] %s\n' "$1" "$stage"
}
cleanup() {
  result=$?
  trap - EXIT
  [ -z "$staging" ] || rm -rf "$staging"
  [ -z "$update_staging" ] || rm -rf "$update_staging"
  if [ "$result" -ne 0 ]; then
    printf '\n[失败] %s未完成（退出码 %s）。\n' "$stage" "$result" >&2
    if [ "$enrollment_attempted" = 0 ]; then
      printf '%s\n' '票据尚未兑换；排除故障后可在有效期内重试，过期请在面板重新生成安装命令。' >&2
    else
      printf '%s\n' '已尝试兑换票据；再次安装前请在面板重新生成安装命令。' >&2
    fi
    case "$stage" in
      启动服务|检查服务状态)
        printf '%s\n' '查看原因：journalctl -u nbpass-node.service -u nbpass-update.service -n 50 --no-pager' >&2 ;;
    esac
  fi
  exit "$result"
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM

step 1 '环境检查'
[ "$(uname -s)" = Linux ] && [ "$(uname -m)" = x86_64 ] || { echo '仅支持 Linux amd64' >&2; exit 1; }
[ "$(id -u)" = 0 ] || { echo '请以 root 运行安装命令（非 root 用户先执行 sudo -i）' >&2; exit 1; }
for dependency in systemctl curl useradd; do
  command -v "$dependency" >/dev/null || { printf '缺少必需命令：%s\n' "$dependency" >&2; exit 1; }
done

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
printf '%s\n' '系统：Linux amd64；运行身份：root；安装路径检查通过。'
step 2 '下载节点程序'
printf '下载地址：%s\n' __NODE_DOWNLOAD_URL__
printf '%s\n' '下面显示已下载字节、平均速度和耗时；连接最多等待 60 秒，单次下载最多 60 分钟，网络故障最多重试 2 次。' \
  '连续 60 秒低于 1 KiB/s 会中止并重试。票据有效期为 30 分钟，下载过慢可能需要重新生成安装命令。'
curl --proto '=https' --proto-redir '=https' --tlsv1.2 -fL --show-error \
  --connect-timeout 60 --max-time 3600 --retry 2 --retry-delay 3 --retry-connrefused \
  --speed-limit 1024 --speed-time 60 \
  --write-out '\n[下载统计] HTTP %{http_code}；已下载 %{size_download} 字节；平均 %{speed_download} 字节/秒；耗时 %{time_total} 秒\n' \
  __NODE_DOWNLOAD_URL__ -o "$staging/nbpass-node"
chmod 0700 "$staging/nbpass-node"
step 3 '注册节点'
printf '控制地址：%s（使用 TLS 验证连接，正在兑换一次性票据）\n' "$control_url"
# 不把票据写入 unit；兑换失败时保留原有程序和服务。
enrollment_attempted=1
"$staging/nbpass-node" -control-url "$control_url" -ticket "$ticket" -data-dir /var/lib/nbpass-node -enroll-only
printf '%s\n' '节点注册成功，凭据已保存。'
step 4 '安装节点与更新服务'
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
step 5 '启动服务'
systemctl daemon-reload
systemctl enable nbpass-node.service nbpass-update.service
systemctl restart nbpass-update.service
systemctl restart nbpass-node.service
step 6 '检查服务状态'
for service in nbpass-update.service nbpass-node.service; do
  if systemctl is-active --quiet "$service"; then
    printf '%s: active\n' "$service"
  else
    printf '%s: 未运行\n' "$service" >&2
    exit 1
  fi
done
printf '\n节点已安装，本机两个服务均已启动（总耗时 %s 秒）；请在面板确认在线状态。\n' "$(($(date +%s) - started_at))"
