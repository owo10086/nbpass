#!/bin/sh
set -eu
umask 077

fail() { echo "$*" >&2; exit 1; }
[ "$(uname -s)" = Linux ] && [ "$(uname -m)" = x86_64 ] || fail '仅支持 Linux amd64'
[ "$(id -u)" = 0 ] || fail '请以 root 运行安装命令'
command -v systemctl >/dev/null
command -v useradd >/dev/null

binary=''
binary_url=''
digest=''
public_origin=''
control_url=''
while [ "$#" -gt 0 ]; do
  [ "$#" -ge 2 ] || fail '安装参数不完整'
  case "$1" in
    -binary) binary=$2 ;;
    -binary-url) binary_url=$2 ;;
    -sha256) digest=$2 ;;
    -public-origin) public_origin=$2 ;;
    -control-url) control_url=$2 ;;
    *) fail '未知参数；支持 -binary 或 -binary-url/-sha256、-public-origin、-control-url' ;;
  esac
  shift 2
done
https_origin() {
  case "$1" in https://*) ;; *) fail '公网源与控制地址必须使用 HTTPS' ;; esac
  authority=${1#https://}
  case "$authority" in ''|*[!a-zA-Z0-9.:-]*|:*|-*) fail '地址只支持 HTTPS 域名或 IPv4 与可选端口，不带路径' ;; esac
}
[ -z "$public_origin" ] || https_origin "$public_origin"
[ -z "$control_url" ] || https_origin "$control_url"
if [ -n "$binary" ]; then
  [ -z "$binary_url" ] || fail '-binary 与 -binary-url 不能同时使用'
  [ -f "$binary" ] && [ ! -L "$binary" ] || fail '本地程序必须为可信的普通文件'
else
  case "$binary_url" in https://?*) ;; *) fail '请用 -binary 指定本地程序，或用 -binary-url 指定 HTTPS 下载地址' ;; esac
  case "$digest" in ''|*[!a-fA-F0-9]*) fail '下载必须提供 64 位 SHA256' ;; esac
  [ "${#digest}" = 64 ] || fail '下载必须提供 64 位 SHA256'
  command -v curl >/dev/null
  command -v sha256sum >/dev/null
fi

# 所有可由安装器写入的系统目录均拒绝符号链接及非 root 可写目录。
root_directory() {
  [ ! -L "$1" ] || fail "拒绝符号链接目录：$1"
  if [ -e "$1" ]; then
    [ -d "$1" ] && [ "$(stat -c %u "$1")" = 0 ] || fail "目录须归 root 所有：$1"
    [ -z "$(find "$1" -prune \( -perm -0020 -o -perm -0002 \) -print)" ] || fail "目录不可由其他用户写入：$1"
  fi
}
for path in /usr/local /usr/local/bin /usr/local/libexec /var/lib /etc/systemd/system /etc/nbpass; do
  root_directory "$path"
done
for path in /usr/local/bin/nbpass /etc/nbpass/panel.env /etc/systemd/system/nbpass.service; do
  [ ! -L "$path" ] || fail "拒绝符号链接文件：$path"
  [ ! -e "$path" ] || [ -f "$path" ] || fail "路径不是普通文件：$path"
done
[ ! -L /var/lib/nbpass ] || fail '拒绝数据目录符号链接'
if [ -e /var/lib/nbpass ]; then
  [ -d /var/lib/nbpass ] || fail '数据路径不是目录'
  [ -z "$(find /var/lib/nbpass ! -type d ! -type f -print)" ] || fail '数据目录含链接或特殊文件'
fi
if [ ! -f /etc/nbpass/panel.env ]; then
  [ -n "$public_origin" ] || fail '首次安装必须指定 -public-origin'
  [ -n "$control_url" ] || control_url=$public_origin
fi

install -d -m 0755 /usr/local/bin /usr/local/libexec
staging=$(mktemp -d /usr/local/bin/.nbpass-panel-install-XXXXXX)
trap 'rm -rf "$staging"' EXIT
trap 'exit 1' HUP INT TERM
if [ -n "$binary" ]; then
  install -m 0700 "$binary" "$staging/nbpass"
else
  curl --proto '=https' --proto-redir '=https' --tlsv1.2 -fsSL "$binary_url" -o "$staging/nbpass"
  printf '%s  %s\n' "$digest" "$staging/nbpass" | sha256sum -c - >/dev/null || fail '面板程序 SHA256 校验失败'
fi
if [ -e /usr/local/bin/nbpass ]; then
  cmp -s "$staging/nbpass" /usr/local/bin/nbpass || fail '已安装其他程序；请先按部署文档用新程序执行 upgrade 备份与迁移，再原子替换程序'
fi
id nbpass >/dev/null 2>&1 || useradd --system --user-group --no-create-home --shell /usr/sbin/nologin nbpass
install -d -m 0750 /etc/nbpass
chown root:nbpass /etc/nbpass
install -d -m 0700 /var/lib/nbpass
chown -hR nbpass:nbpass /var/lib/nbpass
find /var/lib/nbpass -type d -exec chmod 0700 {} \;
find /var/lib/nbpass -type f -exec chmod 0600 {} \;
if [ ! -f /etc/nbpass/panel.env ]; then
  printf 'NBPASS_PUBLIC_ORIGIN=%s\nNBPASS_CONTROL_URL=%s\n' "$public_origin" "$control_url" > /etc/nbpass/panel.env
fi
chown root:nbpass /etc/nbpass/panel.env
chmod 0640 /etc/nbpass/panel.env
if [ ! -f /etc/systemd/system/nbpass.service ]; then
  cat > "$staging/nbpass.service" <<'EOF'
[Unit]
Description=nbpass panel
After=network-online.target
Wants=network-online.target

[Service]
User=nbpass
Group=nbpass
EnvironmentFile=/etc/nbpass/panel.env
ExecStart=/usr/local/bin/nbpass -public-origin ${NBPASS_PUBLIC_ORIGIN} -control-url ${NBPASS_CONTROL_URL} -http-listen 127.0.0.1:8080 -control-listen 127.0.0.1:8081 -data-dir /var/lib/nbpass
Restart=on-failure
RestartSec=5
UMask=0077
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/var/lib/nbpass
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
EOF
  install -m 0644 "$staging/nbpass.service" /etc/systemd/system/nbpass.service
fi
chown root:root /etc/systemd/system/nbpass.service
chmod 0644 /etc/systemd/system/nbpass.service
# 原子发布，避免对正在执行的程序原地写入。
install -m 0755 "$staging/nbpass" "$staging/publish"
chown root:root "$staging/publish"
mv -f "$staging/publish" /usr/local/bin/nbpass
systemctl daemon-reload
systemctl enable nbpass.service
systemctl restart nbpass.service
echo '面板已安装；通过配置的 HTTPS 地址初始化管理员。已有 panel.env、unit 与数据库已保留。'
