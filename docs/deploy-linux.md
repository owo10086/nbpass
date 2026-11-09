# Linux amd64 部署与运维

面向 Linux amd64 + systemd。面板与节点可分机部署；macOS 仅用于开发。面板只提供明文 HTTP 和 gRPC，公网两条链路都由可信反向代理终结 TLS。控制域名必须通过独立网络直接到达面板代理，**不得依赖本面板管理的转发规则、入口组受管记录或待升级节点**。否则业务故障可能同时切断恢复通道。

下文使用 `panel.example.com`（网页、API、安装与更新下载）、`control.example.com`（gRPC）两个域名；先替换域名、证书路径与发行文件路径。v1 业务只支持 TCP/IPv4，每个节点一个可直接入站的公网 IPv4；NAT 或多 IP 部署不启用自动换 IP。

## 1. 准备并安装面板

服务器需要 systemd、`/usr/bin/systemctl`、GNU coreutils/findutils、`useradd`、CA 根证书；网络下载还需要 curl。Debian/Ubuntu 可执行：

```sh
sudo apt-get update
sudo apt-get install -y ca-certificates curl coreutils findutils passwd
```

在可信构建机准备 `go.mod` 指定的 Go（当前 1.26.3）及 Vite 支持的 Node.js/npm（建议 Node.js 22.12+）后，从仓库根目录构建：

```sh
make linux-amd64
```

这会先重建前端和内嵌的 Linux 节点，再得到 `bin/nbpass-linux-amd64`；节点产物为 `bin/nbpass-node-linux-amd64`。仅运行 `go build` 会沿用检出时的内嵌资源，不能用于发布新功能。将面板产物与本仓库的 `scripts/install-panel.sh` 复制到服务器，在同一目录用一条命令安装：

```sh
sudo sh scripts/install-panel.sh -binary ./bin/nbpass-linux-amd64 -public-origin https://panel.example.com -control-url https://control.example.com
```

安装器创建无登录账户 `nbpass`，安装 `/usr/local/bin/nbpass`，注册并启动 `nbpass.service`；网页监听 `127.0.0.1:8080`，gRPC 监听 `127.0.0.1:8081`。若服务器没有仓库目录，保持实际复制的脚本、程序路径即可。没有约定的公共发布下载站；不要从猜测的地址下载安装。

也可从管理员自己的 HTTPS 分发站下载：将上述 `-binary` 换成 `-binary-url https://你的分发域名/nbpass-linux-amd64 -sha256 可信渠道取得的64位摘要`。摘要必须对应该文件，下载与 HTTPS 重定向均验证证书，摘要不符停止安装；脚本本身也应来自可信渠道，不使用 `curl -k`。

重复运行时保留 `/etc/nbpass/panel.env`、现有 unit、数据库与密钥；不重新初始化密码，仍会重启服务。已存在环境文件时传入的域名参数不会覆盖它，修改域名需编辑该文件后重启。安装器拒绝把不同内容的程序覆盖到已安装位置；更换版本必须走第 5 节 CLI。安装器拒绝受管路径的符号链接和不安全系统目录；不要把数据目录改成软链接。

```sh
sudo systemctl status nbpass.service
sudo journalctl -u nbpass.service -n 100 --no-pager
sudo systemctl cat nbpass.service
```

完成下节代理后，经 `https://panel.example.com` 首次设置管理员密码（12–72 字节），再登录。浏览器不能直接通过 HTTP 使用安全会话。

## 2. Nginx：网页与 gRPC 分开代理

以下配置放在 Nginx 的 `http {}` 内（例如被它包含的站点文件），需要 SSL、HTTP/2 与 gRPC 模块。`listen 443 ssl http2` 适用于 Nginx 1.24；新版本也可改为 `listen 443 ssl; http2 on;`。证书应含各自域名和完整链。

```nginx
server {
    listen 443 ssl http2;
    server_name panel.example.com;
    ssl_certificate     /etc/letsencrypt/live/panel.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/panel.example.com/privkey.pem;
    ssl_protocols TLSv1.2 TLSv1.3;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-Proto https;
        # 单层代理覆盖来访者的伪造头；多层代理须逐跳配置可信范围。
        proxy_set_header X-Forwarded-For $remote_addr;
        proxy_set_header Connection "";
        proxy_connect_timeout 5s;
        proxy_read_timeout 300s;
        proxy_send_timeout 300s;
        # 安装与升级文件也从这个域名分发，不能只代理 /api/。
        proxy_buffering off;
    }
}

server {
    listen 443 ssl http2;
    server_name control.example.com;
    ssl_certificate     /etc/letsencrypt/live/control.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/control.example.com/privkey.pem;
    ssl_protocols TLSv1.2 TLSv1.3;
    client_header_timeout 15s;
    client_body_timeout 1h;
    # 流中每条消息仍受 gRPC 限制，但请求体累计量随运行时间增长。
    client_max_body_size 0;
    keepalive_timeout 75s;
    keepalive_requests 100000;

    location / {
        # 必须使用 grpc_pass，后端为 HTTP/2 明文 h2c。
        grpc_pass grpc://127.0.0.1:8081;
        grpc_set_header Host $host;
        grpc_connect_timeout 5s;
        grpc_read_timeout 1h;
        grpc_send_timeout 1h;
        grpc_socket_keepalive on;
        # 不让代理自动重试一次性票据兑换或控制流。
        grpc_next_upstream off;
    }
}
```

```sh
sudo nginx -t
sudo systemctl reload nginx
curl --fail https://panel.example.com/api/auth/status
openssl s_client -connect control.example.com:443 -servername control.example.com -verify_hostname control.example.com -verify_return_error -alpn h2 </dev/null
```

最后一条需显示证书验证成功和 `ALPN protocol: h2`；它只验证 TLS/ALPN，真实双向流还需节点长时间运行。`grpc_*_timeout` 是连续读写间隔的超时，不是每小时强制断流。节点正常每 10 秒发活性，面板另每 60 秒全量同步；这些应用数据维持活动流。`grpc_socket_keepalive` 只开启代理到面板的 TCP 保活，不等于 HTTP/2 PING；内核默认保活周期由系统配置决定，当前节点未显式配置周期性 gRPC PING。不要用短的连接总时长或 CDN 的硬流期限截断控制链路；所有负载均衡、防火墙、代理层都应支持 HTTP/2 双向长流。75 秒空闲连接设置不能用作活动 RPC 的强制寿命。

防火墙只对外开放代理 TLS 端口；8080/8081 保持回环。代理分机时只在明确可信的私网绑定两端口，并用防火墙限制代理源 IP；gRPC 非回环还必须显式加 `-trust-control-network`。该参数只是确认可信网络，**不会启用 TLS**；不可信跨机链路应另加加密传输。不要开放公网 h2c。

登录“安全设置”，把网页代理的真实直连地址（此例 `127.0.0.1/32`）设为可信代理。默认不信任转发头；只有来自可信代理的 `X-Forwarded-For` 才用于来源限流。不信任任意网段或把客户端头原样透传。`-public-origin` 必须匹配浏览器 HTTPS 源，写 API 需要同源 `Origin`；无需伪造或重写 Origin。

## 3. 安装节点与独立更新服务

在网页添加节点，复制面板生成的完整命令，到 Linux amd64 节点执行。命令形式为：

```sh
curl --proto '=https' --proto-redir '=https' --tlsv1.2 -fsSL 'https://panel.example.com/install-node.sh' | sudo sh -s -- -control-url 'https://control.example.com' -ticket '网页生成的43字符一次性票据'
```

票据只有 30 分钟有效且只能兑换一次；命令不含长期 token。脚本下载面板内嵌节点，通过经过 TLS 验证的 gRPC 兑换并加密保存凭据，之后才替换程序、注册 unit。下载或兑换失败时不覆盖原有程序和 unit。兑换后若本地写入失败，应在面板重置 token 领取新票据，不能反复重用已消费票据。

脚本同时安装 `nbpass-node.service` 和独立的 `nbpass-update.service`，并启用开机启动。节点以 `nbpass-node` 用户运行，仅保留低端口监听能力；更新器以 root 运行，通过固定 Unix socket 接受清单，并从**网页源**获取更新文件。节点机必须同时能访问网页源、控制源和需要的目标/检测站点。新装或重新安装会中断已有业务连接。

重跑需在网页重置 token 获取新命令，原 node.key、实际配置缓存、事件/流量/换 IP 日志与更新进度保留；仅凭据会重新兑换。原票据重跑会失败，服务普通重启无需票据。脚本重写两个基础 unit，自定义参数或 CA 应放在 `systemctl edit` 的 drop-in 中，重跑会保留 drop-in；不要在基础 unit 中保留手工改动。

```sh
sudo systemctl is-active nbpass-node.service nbpass-update.service
sudo systemctl cat nbpass-node.service nbpass-update.service
sudo journalctl -u nbpass-node.service -u nbpass-update.service -n 100 --no-pager
```

公网 CA 使用系统根证书。专用 CA 部署需先把 CA 装入系统信任库（例如 Debian 的 `/usr/local/share/ca-certificates/` 加 `update-ca-certificates`），确保 curl、首次兑换、节点和更新器都信任它。若选择程序的 `-ca-file`，节点与更新器**分别**设置该参数，首次兑换也必须信任同一 CA；脚本没有 `-ca-file` 参数，故自动安装优先使用系统信任库。禁止跳过校验或明文降级。

## 4. 用户与最小权限

账户应为安装器创建的专用无登录账户，不复用有其他用途的同名用户。脚本设置持久目录与文件权限；程序按相同权限创建后续密钥、缓存和 SQLite 文件，systemd 创建 runtime 目录。root 更新器可读节点状态，但普通节点用户不可写安装或更新暂存目录。

| 路径 | 所属用户:组 | 权限与用途 |
|---|---|---|
| `/usr/local/bin/nbpass` | root:root | 0755，面板程序 |
| `/etc/nbpass`、`panel.env` | root:nbpass | 目录 0750，文件 0640；域名配置，服务只读 |
| `/var/lib/nbpass` | nbpass:nbpass | 0700，唯一面板数据目录 |
| `panel.key`、`panel.db`、运行中的 `panel.db-wal` / `panel.db-shm`、`panel.lock` | nbpass:nbpass | 0600；密钥必须与同一备份数据库配套 |
| `/usr/local/bin/nbpass-node`、`/usr/local/libexec/nbpass-update` | 首次/重复安装 root:root；节点经更新器替换后 root:nbpass-node | 0755，无组写权限；节点及独立更新器不可由节点改写 |
| 两类服务的 `/etc/systemd/system/*.service` | root:root | 0644，无票据或 token；drop-in 同样由 root 管理 |
| `/var/lib/nbpass-node` | nbpass-node:nbpass-node | 所有目录 0700，密钥和缓存文件 0600；含 `node.key`、`credentials.enc`、`config.enc`、`events.enc`、`traffic.enc`、`self-check.enc`、`rotation.enc`（按使用情况生成） |
| `/var/lib/nbpass-update` | 目录 root:root；`progress.json` root:nbpass-node | 目录 0700，进度文件 0600 |
| `/usr/local/bin/.nbpass-update` | 目录 root:root；更新器生成文件 root:nbpass-node | 0700，固定更新暂存；候选/备份 0755，安装事务 `pending.json` 0600；目录隔离访问 |
| 安装期间的 `/usr/local/bin/.nbpass*install-*`、`/usr/local/libexec/.nbpass-update-install-*` | root:root | 临时目录 0700，退出时清理；各程序在目标所在文件系统原子发布 |
| `/run/nbpass-node`、`health.json` | nbpass-node:nbpass-node | 目录 0700，健康文件 0600；重启重建 |
| `/run/nbpass-update`、`update.sock` | root:nbpass-node | 目录 0750，socket 0660；仅 root 与节点组可连接 |
| CLI 备份目录 | 执行 CLI 的用户:组 | 推荐 nbpass:nbpass，目录 0700，数据/清单/说明 0600，备份程序 0700 |

不要递归放宽数据目录权限；不要把密钥、备份或节点缓存放在网页可访问目录。面板数据库只保存随机 token 的哈希，需要再下发的秘密由 `panel.key` 加密；备份含完整密钥和敏感信息，应加密离机保存。节点目录中的密钥与缓存也需一并保留，删除密钥不能作为“重新安装”。

## 5. 面板 CLI：重置、备份、升级与恢复

CLI 使用明确的数据目录，以 `nbpass` 身份运行，避免 root 创建服务无法写入的文件。下例使用 `sudo -u`。密码重置可以在面板运行时执行，交互输入两次，不把密码写入命令参数：

```sh
sudo -u nbpass /usr/local/bin/nbpass reset-password -data-dir /var/lib/nbpass
```

重置要求已初始化管理员的数据库；会撤销所有会话并清除登录锁定，不要求传 public-origin 或读取密钥。运行中的浏览器需要重新登录。

在线一致性备份：

```sh
sudo install -d -o nbpass -g nbpass -m 0700 /var/backups/nbpass
sudo -u nbpass /usr/local/bin/nbpass backup -data-dir /var/lib/nbpass -backup-dir /var/backups/nbpass/backup-20261109 -program /usr/local/bin/nbpass
```

目标备份目录必须尚不存在，每次用唯一目录名。SQLite `VACUUM INTO` 包含已提交的 WAL 数据，不停止业务、不把旧 WAL/SHM 拷进备份。备份包含 `panel.db`、`panel.key`、`nbpass`、`manifest.json`（SHA256）和 `RECOVERY.md`。备份父目录必须可由 nbpass 写入。

面板升级（新程序例如 `/var/tmp/nbpass-new`，由管理员从可信构建产物提供，root 所有、0755，并允许 nbpass 读取执行）：

```sh
sudo systemctl stop nbpass.service
sudo -u nbpass /var/tmp/nbpass-new upgrade -data-dir /var/lib/nbpass -backup-dir /var/backups/nbpass/pre-upgrade-20261109 -program /usr/local/bin/nbpass
```

**确认命令成功后**才部署新程序：

```sh
sudo install -o root -g root -m 0755 /var/tmp/nbpass-new /usr/local/bin/nbpass.new
sudo mv -f /usr/local/bin/nbpass.new /usr/local/bin/nbpass
sudo systemctl start nbpass.service
```

`upgrade` 先一致性备份旧程序、数据库、密钥，再事务迁移；它不自行替换程序或启动服务。正常服务持有同一数据锁，未停服务时 upgrade 被拒。迁移失败时不执行替换，原程序、事务回滚后的原数据库和原密钥可直接重启；检查错误后修复迁移，不反复重试同一失败迁移。迁移成功后不能随意换回不支持该数据库版本的旧程序。

从旧快照恢复用**支持该快照及恢复保护功能的兼容程序**，不是盲目运行备份中的旧二进制：

```sh
sudo systemctl stop nbpass.service
sudo install -d -o nbpass -g nbpass -m 0700 /var/lib/nbpass-restores
sudo -u nbpass /usr/local/bin/nbpass restore -backup-dir /var/backups/nbpass/backup-20261109 -data-dir /var/lib/nbpass-restores/restored-20261109
```

新数据目录必须不存在，其父目录必须可由 nbpass 写入；不能直接以 nbpass 身份在 root 所有的 `/var/lib` 下创建恢复暂存。restore 先校验备份、配对密钥与数据库，迁移并设置保护后原子发布新目录；不恢复备份程序、不覆盖现有数据。保留原目录，在命令成功后通过 `sudo systemctl edit nbpass.service` 把 `ExecStart` 的 `-data-dir` 改为 `/var/lib/nbpass-restores/restored-20261109`，并把 `ReadWritePaths` 同步改为该目录，其他参数沿用 `systemctl cat` 显示的原值。更改 ExecStart 时先用空的 `ExecStart=` 清除旧值，更改可写目录先用空的 `ReadWritePaths=` 清除旧值；随后 daemon-reload、启动服务。此后所有 CLI 均指定新目录，不再对旧目录操作。

恢复撤销全部旧登录会话与安装票据，暂停自动 DNS 写入、自动换 IP 等写操作。重新登录后，可在该面板页面的浏览器控制台使用当前会话核对：

```javascript
await fetch('/api/recovery').then(r => r.json())
await fetch('/api/recovery/reconcile', {
  method: 'POST', headers: {'Content-Type': 'application/json'},
  body: JSON.stringify({confirm: true})
}).then(r => r.json())
```

核对前先让节点重连、上报配置版本高水位与动作记录并应用新配置；新配置版本高于高水位。核对只读 DNSPod，采用能对应已核对成员的实际记录，无法核对的节点或入口组继续暂停，不重放旧写操作。最近 24 小时换 IP 历史不足时继续暂停自动换 IP，直到记录补齐或恢复满 24 小时；其他未核对或结果未知的门禁不会随时间自动消失。不要用删除动作记录、清空额度或直接覆盖旧数据库来解除保护。

## 6. 节点升级、回滚与留存

在节点页升级单个在线、更新器可达的节点到面板内嵌程序；会断开该节点已有业务连接。同版本也可重装。清单绑定任务 ID、版本、linux/amd64、大小、SHA256；更新器通过验证 TLS 的网页源取清单和程序，不接受任意命令、下载地址或安装路径。

下载、校验和候选自检完成才原子替换节点并重启；启动或 3 分钟内本地业务自检失败自动恢复旧二进制并重启。回滚不覆盖整个节点数据目录，因此不复活已撤销授权、不丢换 IP 去重记录；不承诺任意旧版状态格式兼容。更新器被中断时保留安装事务，重启可恢复；其自身是独立程序，节点升级不替换更新器。

只有新进程回连、版本一致、配置已应用、本地就绪才在面板确认成功。**当前票 34 基线在本地就绪但控制失联时保留新程序和“回连”，不因控制失联回滚；票 35 的确认超时“未确认”与跨版本兼容尚未在本基线交付。**维护最多 10 分钟，期间离线不自动入口切换/换 IP；到期后重新观察 90 秒，并用新证据重新评估。

“保留期设置”默认事件 90 天、探针 24 小时，可设事件 1–3650 整天、探针 1–8760 整小时；曲线展示仍是最近 24 小时。启动、修改设置后和空闲每分钟分批清理事件/分钟历史，不清理额度、进行中动作、配置版本、累计流量及去重高水位。延长保留期不能恢复已删数据；不执行 VACUUM，数据库文件不一定立即变小。

下一步按 [Linux 人工验收清单](linux-acceptance.md) 在隔离环境执行并记录，完成后再交付生产。
