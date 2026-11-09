# 独立 root 更新服务只通过固定 Unix socket 接受绑定清单。
for path in /usr/local/libexec /var/lib/nbpass-update /etc/systemd/system/nbpass-update.service; do
  [ ! -L "$path" ] || { echo '拒绝更新服务符号链接路径' >&2; exit 1; }
done
install -d -m 0755 /usr/local/libexec
install -d -m 0700 /var/lib/nbpass-update
install -d -m 0700 /usr/local/bin/.nbpass-update
chown root:root /var/lib/nbpass-update /usr/local/bin/.nbpass-update
[ ! -L /usr/local/libexec/nbpass-update ] || { echo '拒绝更新器符号链接' >&2; exit 1; }
# 与更新器目标同一文件系统发布，避免跨挂载 mv 退化为复制运行中的程序。
update_staging=$(mktemp -d /usr/local/libexec/.nbpass-update-install-XXXXXX)
trap 'rm -rf "$staging" "$update_staging"' EXIT
install -m 0755 "$staging/nbpass-node" "$update_staging/update-ready"
chown root:root "$update_staging/update-ready"
mv -f "$update_staging/update-ready" /usr/local/libexec/nbpass-update
cat > "$staging/nbpass-update.service" <<EOF
[Unit]
Description=nbpass fixed node updater
After=network-online.target
Wants=network-online.target

[Service]
User=root
Group=nbpass-node
ExecStart=/usr/local/libexec/nbpass-update -update-service -update-origin __UPDATE_ORIGIN__
Restart=on-failure
RestartSec=5
RuntimeDirectory=nbpass-update
RuntimeDirectoryMode=0750
UMask=0077
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ReadWritePaths=/usr/local/bin /var/lib/nbpass-update /run/nbpass-update

[Install]
WantedBy=multi-user.target
EOF
install -m 0644 "$staging/nbpass-update.service" /etc/systemd/system/nbpass-update.service
chown root:root /etc/systemd/system/nbpass-update.service
