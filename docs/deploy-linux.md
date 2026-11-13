# Linux amd64 部署与运维

面向 Linux amd64 + systemd。面板与节点可分机部署；macOS 仅用于开发。面板只提供明文 HTTP 和 gRPC，公网两条链路都由可信反向代理终结 TLS。控制域名必须通过独立网络直接到达面板代理，**不得依赖本面板管理的转发规则、入口组受管记录或待升级节点**。否则业务故障可能同时切断恢复通道。

下文使用 `panel.example.com`（网页、API、安装与更新下载）、`control.example.com`（gRPC）两个域名；先替换域名、证书路径与发行文件路径。业务只支持 TCP/IPv4。每台服务器的转发端点配置一个连接地址（公网或内网）及可选对外端口，实际映射和路由由管理员配置；自动换 IP 仍要求每个节点一个可直接入站的公网 IPv4，**NAT、多 IP 或使用内网/专线端点地址的部署不启用自动换 IP**。IX 不扩展这项自动化边界。

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

在网页添加节点，复制面板生成的完整命令，以 root 身份到 Linux amd64 节点执行（非 root 用户先执行 `sudo -i`）。命令先完整下载脚本，下载失败不会执行残缺脚本，临时文件会自动清理。需要分步查看时，可执行以下等效命令（填写网页生成的新票据）：

```sh
(
  set -eu
  umask 077
  installer=$(mktemp "${TMPDIR:-/tmp}/nbpass-install.XXXXXX")
  trap 'rm -f "$installer"' EXIT
  trap 'exit 1' HUP INT TERM
  printf '%s\n' '[准备] 正在下载安装脚本…'
  curl --proto '=https' --proto-redir '=https' --tlsv1.2 -fL --show-error \
    --connect-timeout 60 --max-time 180 --retry 2 --retry-delay 3 --retry-connrefused \
    'https://panel.example.com/install-node.sh' -o "$installer"
  sh "$installer" -control-url 'https://control.example.com' -ticket '网页生成的43字符一次性票据'
)
```

安装过程依次显示环境检查、下载、注册、程序安装、服务启动、服务检查。下载显示已接收字节、平均速度和耗时；连接最多等待 60 秒，节点程序单次下载最多 60 分钟，临时故障最多重试 2 次，连续 60 秒低于 1 KiB/s 会中止并重试。失败会显示所在阶段与退出码；服务异常时给出 `journalctl` 命令。成功仅表示本机两个服务处于 active，仍须到面板确认在线。不要使用 `sh -x` 查看详情，以免回显票据。

票据只有 30 分钟有效且只能兑换一次；命令不含长期 token。脚本下载面板内嵌节点，通过经过 TLS 验证的 gRPC 兑换并加密保存凭据，之后才替换程序、注册 unit。下载或兑换失败时不覆盖原有程序和 unit。兑换后若本地写入失败，应在面板重置 token 领取新票据，不能反复重用已消费票据。

脚本同时安装 `nbpass-node.service` 和独立的 `nbpass-update.service`，并启用开机启动。节点以 `nbpass-node` 用户运行，仅保留低端口监听能力；更新器以 root 运行，通过固定 Unix socket 接受清单，并从**网页源**获取更新文件。节点机必须同时能访问网页源、控制源和需要的目标/检测站点。新装或重新安装会中断已有业务连接。

重跑需在网页重置 token 获取新命令，原 node.key、实际配置缓存、事件/流量/换 IP 日志与更新进度保留；仅凭据会重新兑换。若换成另一个节点身份重装（同一台机器在面板新建节点后安装，或改了控制地址），兑换时会清除旧身份的配置缓存、事件、流量、换 IP 与自测记录，保留 node.key；旧身份未上报的记录不会转到新节点名下。原票据重跑会失败，服务普通重启无需票据。脚本重写两个基础 unit，自定义参数或 CA 应放在 `systemctl edit` 的 drop-in 中，重跑会保留 drop-in；不要在基础 unit 中保留手工改动。

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

仅在本地状态格式兼容时，在节点页升级单个在线、更新器可达的节点到面板内嵌程序；格式 1→2 的本次 IX 发行必须按第 8 节同身份重新安装。会断开该节点已有业务连接。同版本也可重装。清单绑定任务 ID、版本、linux/amd64、大小、SHA256；更新器通过验证 TLS 的网页源取清单和程序，不接受任意命令、下载地址或安装路径。

下载、校验和候选自检完成才原子替换节点并重启；启动或 3 分钟内本地业务自检失败自动恢复旧二进制并重启。回滚不覆盖整个节点数据目录，因此不复活已撤销授权、不丢换 IP 去重记录；不承诺任意旧版状态格式兼容。更新器被中断时保留安装事务，重启可恢复；其自身是独立程序，节点升级不替换更新器。

只有新进程回连、产物身份匹配、配置已应用、本地就绪才在面板确认成功。本地就绪但控制失联时保留新程序；面板在任务超过 3 分钟仍未确认时显示“未确认”，等待回连核对，不能据此判定安装失败或自行回滚。维护最多 10 分钟，期间离线不自动入口切换/换 IP；到期后重新观察 90 秒，并用新证据重新评估。

“保留期设置”默认事件 90 天、探针 24 小时，可设事件 1–3650 整天、探针 1–8760 整小时；曲线展示仍是最近 24 小时。启动、修改设置后和空闲每分钟分批清理事件/分钟历史，不清理额度、进行中动作、配置版本、累计流量及去重高水位。延长保留期不能恢复已删数据；不执行 VACUUM，数据库文件不一定立即变小。

下一步按 [Linux 人工验收清单](linux-acceptance.md) 在隔离环境执行并记录，完成后再交付生产。


## 7. IX 逐边地址、映射与业务验收

先在「转发端点」设置各服务器的本机隧道监听端口、唯一连接地址及可选对外端口，再在端点组选择服务器。已删除独立的专线地址功能；升级会清理专线记录并将旧引用改用原主地址，保留主备顺序。直连不经过端点组，单跳选择一个组，链式选择至少两个组组成的共享链；改共享链影响所有引用端口。同一服务器不能出现在入口和任意跳的所有候选中两次，停用端口的引用仍受删除保护。

对每一条可能的相邻边列出前驱、后继成员、端点地址及端口，不只测每组首选。例如 A→{B1,B2}→{C1,C2} 要检查 A→B1、A→B2，以及两个 B 到两个 C 的四条边：

| 前驱 | 后继连接地址（IPv4 / 域名） | 对外 TCP 端口 | 后继本机隧道监听 | 映射/放行负责人 |
|---|---|---|---|---|
| A | B1 的 ix-b1.example.com | 3443 | 22001 | 管理员或专线运营商 |
| B1 | C1 的 10.20.0.11 | 22002（未覆盖） | 22002 | 管理员 |

1. 从**实际前驱机器**检查后继域名的 A 记录与 IPv4 路由；节点只使用 IPv4，不使用 AAAA。域名由前驱解析，面板本机能解析或连通不能代替逐边测试。先用 `getent ahostsv4 ix-b1.example.com` 查看地址，再对各实际 IPv4/端口执行 `nc -4 -vz 地址 3443`；这些仅证明 TCP 可达。
2. 在运营商/路由器/NAT 配置「对外端口→后继本机隧道监听端口」。例如 B1 的 `3443→22001`，后继用 `ss -ltnp` 核对监听在本机 22001；逐项确认 NAT 落点、运营商映射与回程路由，并让主机防火墙和网络防火墙按实际前驱来源放行 TCP。面板不会创建映射、开放防火墙或配置路由。
3. 对外端口不占用后继本机端口，可以与本机业务端口同号；实际业务监听、本机隧道监听和检测端口冲突必须拒绝。节点仍监听全部 IPv4 地址，不提供监听绑定 IP。不能把填写对外端口当作新建本机监听。
4. 以面板下发的实际路径建立 WSS 连接并传业务字节。WSS 证书按后继稳定内部身份及固定证书验证，连接地址不替代证书身份，不关闭验证。逐跳核对当前授权持久应用、确认、本边及后续路径；一条边握手成功不证明整链或最终目标可用。
5. 最终目标只由直连入口或链末跳解析/访问。若填写出站 IP，先在该目标拨号者上用 `ip -4 addr show` 确认地址确实属于本机，并核对路由及目标实际观察到的源地址；非本机地址应显示应用错误，不能静默改用系统源地址。中间跳连下一跳不绑定此出站 IP。
6. 在隔离环境逐个阻断/恢复每跳首选和备用，检查组内主备与全坏失败，再验证 A→B1 可达但 B1→C 坏时可以从 B2 完成全链，A→B1 本边不误判坏。确认目标真实业务、证书验证和每跳地址匹配；换公网 IP 后核对端点地址已更新、对外端口与证书身份保持不变。控制离线时观察标为历史/未知，缓存业务是否继续应以真实业务验证，不能仅凭面板颜色判断。

把实际网络、前驱/后继、映射端口和已验/待验结果记入现有验收票。仓库本机 HTTP/WSS/TCP 测试覆盖隔离映射和故障接缝，**不能代替生产专线、运营商 NAT、防火墙、证书部署与回程路由的人工验收**。公网被墙证据与专线业务证据分开；后跳坏不能据此盲目换入口公网 IP，DNS/换 IP 仍由原协调流程处理。

## 8. IX 缓存 A：一次发行、同身份重装与人工回滚

本次完整 IX 发行读格式 1/2、统一写格式 2，继续使用同目录 `config.enc` 和原 AEAD 附加数据 `nbpass-node-config-v1`，原子写盘；没有格式 1 副本、双写或过渡发行。普通规则、relay-only 授权、出站源 IP 和撤销检查点都使用 2。旧独立更新器的 `CompatibleState` 要求候选写格式仍为本地格式 1，**会拒绝 1→2 在线升级**；不修改或绕过这项保护，也不单独替换节点而保留旧更新器。

### 同身份重新安装

1. 按第 5 节备份并升级面板，使用同一次完整发行构建的面板嵌入前端及节点包；不要使用 01–08 中间票产物。先保存可信旧版节点和更新器的来源、SHA256 与 `-update-check` 声明，核对它们确实是实际生产回滚目标，并在隔离环境执行下方回滚演练。本机固定历史程序的通过不代表任意生产旧包都已验证。
2. 在面板与节点核对没有活动更新任务，查看两服务及更新日志，检查更新进度和事务。`reconnecting`/“未确认”不等于完成；`failed` 也必须核对失败原因。若存在 `pending.json`，先让**原更新器按原版本恢复事务**并核对旧程序/服务恢复；恢复失败继续排查，不能删除事务文件、备份或进度来放行重装。

   ```sh
   sudo systemctl status nbpass-node.service nbpass-update.service
   sudo journalctl -u nbpass-update.service -n 100 --no-pager
   sudo test ! -e /usr/local/bin/.nbpass-update/pending.json
   sudo cat /var/lib/nbpass-update/progress.json
   ```

   从未更新过的机器可以没有 `progress.json`；其他读错误不能当成无事务。上述 `test` 必须成功，且进度/面板任务均已核对，才执行后续步骤。

3. 停止两组件并确认都为 inactive，停后再次检查没有并发产生的未恢复事务。禁止在这段维护期间从面板发起更新。

   ```sh
   sudo systemctl stop nbpass-node.service nbpass-update.service
   sudo systemctl is-active nbpass-node.service nbpass-update.service
   sudo test ! -e /usr/local/bin/.nbpass-update/pending.json
   ```

   `is-active` 对已停止服务会返回非零，应逐个确认输出为 inactive。保留整个 `/var/lib/nbpass-node`、更新进度与证据，尤其 `node.key`、`credentials.enc`、同身份 `config.enc`、事件/流量/换 IP/自测状态。需要备份时使用权限 0700 的私有目录，文件 0600，不放到公开下载位置。
4. 在面板原服务器上**重置 token**，取得新的 30 分钟一次性票据，执行第 3 节由本次面板生成的完整安装命令。保持原服务器 ID、控制 URL 和数据目录，不新建服务器身份，不重用旧票据。安装器用实际新程序兑换凭据后替换节点和独立更新器，依次重启更新器、节点；同身份状态与 `node.key` 保留。兑换后本地失败必须再重置取新票据。
5. 核对两服务实际程序均为本次发行，可分别执行 `sudo /usr/local/bin/nbpass-node -update-check` 与 `sudo /usr/local/libexec/nbpass-update -update-check`，同时用 `sha256sum` 对照可信发行摘要。`-update-check` 显示 `state_min=1,state_max=2,state_write=2`、当前节点能力与逐端口同步正常。隔离演练必须证明新版在控制断开时读入旧版真实写出的格式 1，保留高水位、实际规则和撤销边界后原子写 2，旧撤销端口没有复活；同身份事件/流量不能丢失或归给新身份。缓存加密，勿通过打印明文检查；本仓库 `TestIXSameIdentityActualV1ToV2Reinstallation` 直接读取实际加密文件验证这个边界。实际 Linux/systemd 部署还要按第 7 节验证真实业务与源 IP。

### 人工回滚与空状态恢复

1. 先按上节核对/恢复所有更新事务，再停止两服务。使用已核验的**配套旧版节点及独立更新器**替换各自程序，保持密钥、凭据及整个状态目录。先不启动更新器，在控制不可达的隔离演练中，以原 unit 的控制 URL、CA 和数据目录手工运行旧节点一次，避免 systemd 的自动重启循环掩盖拒绝：必须输出“节点配置缓存格式或身份不匹配”并非零退出，业务/隧道监听及下一跳/目标连接均为零。若实际旧程序没有此保证，阻断回滚与发布。
   在已停止两服务且无未恢复事务的前提下，替换命令示例（先将 `/var/tmp/ix-old-node`、`/var/tmp/ix-old-update` 换成已核验旧包路径）：

   ```sh
   sudo install -o root -g root -m 0755 /var/tmp/ix-old-node /usr/local/bin/nbpass-node.rollback
   sudo mv /usr/local/bin/nbpass-node.rollback /usr/local/bin/nbpass-node
   sudo install -o root -g root -m 0755 /var/tmp/ix-old-update /usr/local/libexec/nbpass-update.rollback
   sudo mv /usr/local/libexec/nbpass-update.rollback /usr/local/libexec/nbpass-update
   sudo -u nbpass-node /usr/local/bin/nbpass-node -control-url https://control.example.com -data-dir /var/lib/nbpass-node
   ```

   最后一条预期非零退出；专用 CA 必须添加原 unit 的 `-ca-file`。隔离演练先在网络层阻断该节点到控制端点，不改控制 URL 或删除凭据来模拟离线。用 `ss -ltnp` 和下一跳/目标端实际连接观察同时验证停止转发；不要在正常运行的节点目录并发启动第二进程。

2. 旧版不会在格式拒绝后自动重连。确认进程已退出，**仅为这次格式不兼容回滚**人工把 `config.enc` 原样移至不加载位置；保留节点密钥、当前凭据及缓存内的授权高水位/撤销证据。示例路径使用唯一的私有隔离目录，操作后核对 SHA256 与原文件一致：

   ```sh
   sudo sha256sum /var/lib/nbpass-node/config.enc
   sudo install -d -o nbpass-node -g nbpass-node -m 0700 /var/lib/nbpass-node/ix-rollback-20261112
   sudo test ! -e /var/lib/nbpass-node/ix-rollback-20261112/config.enc
   sudo mv /var/lib/nbpass-node/config.enc /var/lib/nbpass-node/ix-rollback-20261112/config.enc
   sudo chmod 0600 /var/lib/nbpass-node/ix-rollback-20261112/config.enc
   sudo sha256sum /var/lib/nbpass-node/ix-rollback-20261112/config.enc
   ```

   每次使用新的唯一目录；任一步失败先排查。**不得把 version 改为 1、恢复更早的缓存、删除 node.key，或把普通损坏/写盘失败当作自动清缓存理由。**隔离文件含秘密，不公开或上传日志。
3. 在控制仍不可达时启动旧节点空状态，验证业务继续停止，不恢复旧授权；随后恢复控制，只接受面板按旧节点实际能力过滤的当前快照。旧节点位于链入口、中间或末跳、或需源 IP 的端口应被阻塞，其他兼容端口可正常同步。不能为了恢复业务解除源限制或改为直连；任何显式规则更改按正常管理流程操作。
4. 核对兼容当前配置、程序 SHA、业务和更新事务后再成套启动两服务。离线回滚中断业务是 A 的既定代价，控制恢复前不承诺 IX 可用。真实旧读取器、完整旧二进制、relay/末跳源 IP/撤销检查点及人工隔离/重连矩阵在现有票中留证；生产目标身份、实际双服务及真实网络未执行时明确标为待验。
