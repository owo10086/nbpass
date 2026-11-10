<!-- Source URL: https://www.zeroforwarder.com/docs/dns-webhook/ | Capture date: 2026-11-10 -->

# DNS Webhook 接入

下载与面板版本匹配的接收端，验证签名与记录集协议，接入自建 DNS 或自定义 DNS API。

DNS Webhook 适合自建 DNS、支持 RFC 2136 的权威服务，或需要自行适配的 DNS API。调用方向为 **ZFC 面板 → 你部署的 Webhook 接收端 → 实际 DNS 系统**。接收端负责验签、保存版本和实际写入；普通通知机器人或“收到消息就回 200”的 URL 不能直接用于这里。

同一个 DNS 配置可用于 [DDNS 绑定](../dynamic-dns/index.md)、[服务器组 DNS 自动切换](../server-groups/index.md#配置组-dns-自动切换)和[证书 DNS-01 验证](../certificates/index.md#用-dns-验证申请证书)。先独立验证接收端，再让这些功能使用它。

## 适用版本

| 项目 | 本页适用范围 |
| --- | --- |
| Webhook 协议 | JSON 字段 `version: 1`，即协议 v1；它与面板软件版本号是两回事。 |
| 面板构建 | Webhook 表单内具有 **完整接入与协议文档**、**可运行接收端示例**和 **下载完整示例（.tar）** 入口的版本。仅看到 Webhook 服务商选项不代表具备这些入口。 |
| 示例运行环境 | Linux / macOS、Python 3.10+；真实 RFC 2136 写入还需要 `nsupdate`。Windows 或多实例需另外提供等价共享锁。 |

内置指南功能已于 2026-10-21 合入，首个包含它的正式发布版本号尚未确认；**不要仅凭 `1.2.6` 或更大的版本数字推断已包含该功能**。先按上表检查实际面板；没有下载入口时，升级到包含该功能的发布版本后再使用本页流程。

本站提供接入流程。**完整协议、状态码重试规则和接收端源码，以已安装面板内置的说明和下载包为准。** 升级面板后重新核对这两份内容，不混用另一版本的示例，也无需访问代码仓库。

## 下载对应版本的接收端

1. 管理员进入 **系统设置 → DNS 与证书 → DNS配置管理**，新增配置并选择 **Webhook**。不同界面可能把该入口列在“DNS API 配置”中。
2. 打开 **可运行接收端示例**，点击 **下载完整示例（.tar）**。解压包中包含中英文协议、运行步骤、接收端、RFC 2136 适配器、签名发送工具和测试。
3. 将下载包放入用于验证的空目录，再执行：

```sh
tar -xf zfc-dns-webhook-example.tar
cd zfc-dns-webhook/examples/dns-webhook
```

接下来的命令均在解压后的这个目录中运行。`README.zh-CN.md` 是随当前版本提供的完整运行步骤；`../../docs/user/dns-webhook.md` 是对应协议。站点没有提供一个固定指向最新版源码的替代下载地址。

## 先在本地跑通协议

先用演示模式验证签名、顺序和重放处理。它只保存 SQLite 模拟记录，**不会修改真实 DNS**。

在第一个终端执行，保留这个 shell 中的变量：

```sh
umask 077
export WEBHOOK_SECRET="$(python3 -c 'import secrets; print(secrets.token_hex(32))')"
python3 - <<'PY'
import json, os
from pathlib import Path

Path('config.json').write_text(json.dumps({
    'secret': os.environ['WEBHOOK_SECRET'],
    'zones': ['example.com'],
    'database': str(Path('state.sqlite3').resolve()),
    'listen': '127.0.0.1', 'port': 8090, 'demo': True,
}, indent=2))
base = {'version': 1, 'source': '3b9e45ae-7156-4b08-8e7d-fca8e7edb453'}
Path('ping.json').write_text(json.dumps({
    **base, 'event': 'ping', 'zones': ['example.com'],
}))
Path('start.json').write_text(json.dumps({
    **base, 'event': 'generation.start', 'generation': '1',
}))
put = {
    **base, 'event': 'record_set.put', 'generation': '1',
    'zone': 'example.com', 'name': 'home.example.com', 'type': 'A',
    'ttl': 600, 'values': ['203.0.113.7', '203.0.113.8'], 'seq': '1',
}
Path('put.json').write_text(json.dumps(put))
Path('delete.json').write_text(json.dumps({**put, 'seq': '2', 'values': []}))
PY
python3 receiver.py --config config.json
```

在第二个终端进入同一个解压目录，读取演示配置的 Secret 并依次发送：

```sh
export WEBHOOK_SECRET="$(python3 -c 'import json; print(json.load(open("config.json"))["secret"])')"
python3 send_example.py http://127.0.0.1:8090/dns ping.json
python3 send_example.py http://127.0.0.1:8090/dns start.json
python3 send_example.py http://127.0.0.1:8090/dns put.json
python3 send_example.py http://127.0.0.1:8090/dns put.json
python3 send_example.py http://127.0.0.1:8090/dns delete.json
python3 send_example.py http://127.0.0.1:8090/dns put.json
```

预期前五次返回 **204**，最后一次返回 **409**：重复 put 是幂等成功，删除后的旧 put 不能复活记录。发送工具在最后一次 HTTP 错误时会非零退出，这是本次验收的预期结果。

也可在此目录运行 `PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s . -v`。标准库测试不需要真实 DNS 账号；需要 `dnspython` 和 `nsupdate` 的可选测试在依赖缺失时跳过。

## 连接实际 DNS

停止演示接收端。生产使用单独的 Secret 和数据库，保留其状态文件；`demo: true` 必须改成同步执行 DNS 适配器，才能让真实解析变化。

### 使用 RFC 2136 与 TSIG

随包的 `nsupdate_adapter.py` 可连接支持 RFC 2136 / TSIG 的 BIND 等权威 DNS。先准备一个自己管理、已正确委派的专用测试 zone，并安装、确认 `nsupdate` 可用。以下名称、IP 和路径均需替换成自己的环境。

在权威 DNS 主机生成 TSIG 密钥：

```sh
umask 077
tsig-keygen -a hmac-sha256 zfc-webhook > /etc/bind/zfc-webhook.key
```

在现有 BIND 配置中引用密钥，为受管区域及所需类型授权：

```text
include "/etc/bind/zfc-webhook.key";
zone "example.com" {
    type primary;
    file "/var/lib/bind/db.example.com";
    update-policy { grant zfc-webhook zonesub A AAAA TXT CNAME NS; };
};
```

zone 文件应已存在且有效，BIND 应能写入动态更新 journal。按现有管理流程检查并重载配置；按实际记录用途收窄授权与网络来源。通过安全渠道将密钥复制到接收端 `/etc/zfc-webhook/update.key`，只允许运行接收端的账号读取。**TSIG 密钥与 Webhook Secret 是两份不同凭证。**

在接收端的解压目录设置环境并生成真实写入配置，服务管理器也需要保留这些环境变量：

```sh
export DNS_SERVER=192.0.2.53
export DNS_PORT=53
export DNS_TSIG_KEY=/etc/zfc-webhook/update.key
export NSUPDATE_BIN="$(command -v nsupdate)"
umask 077
export WEBHOOK_SECRET="$(python3 -c 'import secrets; print(secrets.token_hex(32))')"
python3 - <<'PY'
import json, os, sys
from pathlib import Path

Path('config-real.json').write_text(json.dumps({
    'secret': os.environ['WEBHOOK_SECRET'], 'zones': ['example.com'],
    'database': str(Path('state-real.sqlite3').resolve()),
    'listen': '127.0.0.1', 'port': 8090, 'adapter_timeout': 8,
    'command': [sys.executable, str(Path('nsupdate_adapter.py').resolve())],
}, indent=2))
PY
python3 receiver.py --config config-real.json
```

`192.0.2.53` 是示例地址，不能直接用于生产。实际服务器、受管 zone、TSIG 授权及接收端配置必须一致。适配器在一次 TCP DNS UPDATE 中替换指定名称/类型的完整集合，收到权威确认后才返回成功。

### 接入其它 DNS API

将配置的 `command` 替换为自己的适配器参数数组，以绝对可执行路径开头，不经过 shell。适配器从 stdin 读取完整 put JSON，同步替换该 zone/name/type 的整个 `values`；空数组只删除该名该类型，保留其它记录。

凭证保存在受保护的环境或文件中。仅在 DNS 确认接受后退出 **0**；确定未写入且以后也不会执行时可退出 **3**。超时、信号终止及其它未知结果会保留 pending，按随包 README 离线恢复。不能提交后台任务后就返回成功。

## 配置面板

用 HTTPS 反向代理暴露 `/dns`，保持接收端监听 `127.0.0.1:8090`。在已有有效 TLS 的 Nginx 虚拟主机中可添加：

```nginx
location = /dns {
    client_max_body_size 256k;
    proxy_pass http://127.0.0.1:8090;
    proxy_read_timeout 12s;
}
```

不要改写请求体或移除 `X-ZFC-*` 请求头。请求由**面板服务器**发出：面板中的 `localhost` 指面板自身的网络环境，不能用它访问你电脑上的示例。

| 面板字段 | 如何填写 |
| --- | --- |
| URL | 面板可直接访问的最终 HTTPS 地址，如 `https://hook.example.com/dns`，包含路径，不依赖重定向。 |
| Secret | 填写 `config-real.json` 中的 `secret` 原文，至少 32 个 UTF-8 字节；不要填演示配置的 Secret。面板生成的十六进制文本应原样使用，不先做 hex 解码。编辑时留空保留原值。 |
| 域名（zone） | 接收端获准管理的区域，如 `example.com`，不是 `home.example.com` 这类单条记录名。多个 zone 逐个输入后回车；移动端也可按逗号、空白或换行分隔。 |
| 超时 | 1–30 秒，默认 15 秒，应覆盖一次同步写入；示例适配器默认最多等待 8 秒。 |
| 最低 TTL | 作为记录 TTL 下限，仍需考虑权威、递归与客户端缓存。 |

点击 **测试连接**，成功后保存并启用配置。测试只发送已签名的 `ping`，域名列表来自表单配置，**不验证 DNS 写权限，也不证明记录已公开解析**。

一条 Webhook URL 只能配置一次；同一记录集应由一个接收端状态库负责，不要让其它 source、数据库或工具并发维护它。

## 验收接入

1. 在 [DDNS](../dynamic-dns/index.md#新建-ddns-绑定)、[服务器组](../server-groups/index.md#配置组-dns-自动切换)或[证书申请](../certificates/index.md#用-dns-验证申请证书)中选择已验证的 Webhook 配置。保存 DNS 配置本身不会写记录。
2. 用专用测试记录触发实际变更。DDNS 和组 DNS 初次配置可先观察状态；开启实际写入后，接收端应先接受 `generation.start`，再处理 `record_set.put`。
3. 查看接收端日志与实际权威记录，例如 `dig @192.0.2.53 home.example.com A +short`；替换成自己的权威地址和记录。再查询公开递归解析器，结合缓存与 TTL 判断。
4. 验证两个值能够共存、减少一个值后只保留剩余值，以及 `values: []` 只删除该名该类型。删除测试仅使用专用记录。
5. 确认重复请求不重复写入、旧版本被拒绝，重启接收端后仍保留代次与版本。示例的自动测试覆盖这些行为。

证书 DNS-01 还要求挑战 TXT 通过面板的公开传播检查；接收端回 2xx 不代表递归缓存立即刷新。记录、委派或传播不正确时，按[证书管理](../certificates/index.md)检查后再申请，避免反复提交。

## 协议要点

完整字段表、请求示例和响应处理规则位于面板内的 **完整接入与协议文档**，也包含在下载包中。自写接收端时应逐项对照，不能只实现下面的验签函数。

### HTTP 与验签

面板向同一个 URL 发送 `POST application/json`，不跟随跳转：

| 请求头 | 用途 |
| --- | --- |
| `X-ZFC-Event` | 事件提示，应与已签名 body 的 `event` 一致。 |
| `X-ZFC-Delivery` | 每次尝试的新 UUID，不能用作记录版本或幂等键。 |
| `X-ZFC-Timestamp` | 本次尝试的 Unix 秒；示例允许 ±300 秒时钟偏差。 |
| `X-ZFC-Signature` | `sha256=` 加 HMAC-SHA256 小写十六进制摘要。 |

签名输入是时间戳头的 ASCII 字节、一个点和**原始请求体字节**。Secret 使用原始 UTF-8 文本；不要把解析后的 JSON 重新序列化来验签或计算幂等摘要：

```python
import hashlib
import hmac

def valid_signature(secret, timestamp, raw_body, received_signature):
    expected = 'sha256=' + hmac.new(
        secret.encode('utf-8'),
        timestamp.encode('ascii') + b'.' + raw_body,
        hashlib.sha256,
    ).hexdigest()
    return hmac.compare_digest(expected, received_signature)
```

处理事件前还需验证时间窗口、协议版本、允许的 zone 与字段内容。保持面板和接收端时钟同步。

### 三类事件与完整集合

| 事件 | 接收端职责 |
| --- | --- |
| `ping` | 校验签名、版本和 zones；不写 DNS，不建立正式代次。其 source 是临时随机 ID。 |
| `generation.start` | 按正式 source 持久化代次水位；更大则抬升，相等幂等成功，更小返回 409 并带当前 watermark。它不清空 DNS。 |
| `record_set.put` | generation 必须等于该 source 水位；比较版本后，将 zone/name/type 的集合替换为全部 values，确认并持久化后回 2xx。 |

`generation` 和 `seq` 都是 `1..9223372036854775807` 范围、无前导零的十进制字符串，应按整数比较；JavaScript 用 `BigInt`。每个 `(source, zone, name, type)` 保存最后成功的 `(generation, seq, 原始 body 摘要)`：相同版本及摘要幂等成功，相同版本而内容不同返回 409，更旧版本拒绝写入。

`values` 是按 UTF-8 字节序去重排序的完整集合，`[]` 表示删除该名该类型。TXT 两个值是两条值，不能逐条覆盖。示例支持 A、AAAA、TXT、CNAME 和 NS，记录名允许最左侧完整标签为 `*`，如 `*.example.com`；zone 和 CNAME/NS 目标不允许通配符。

版本检查、DNS 应用及本地状态提交必须串行衔接；成功前不要回 2xx，也不要用 202 表示尚未执行。结果未知时须阻止后续屏障和写入，直到恢复完成。

## 排障与恢复

| 现象 | 先检查什么 |
| --- | --- |
| 没有文档或下载入口 | 先核对[适用版本](#适用版本)，使用包含内置指南的面板构建。 |
| 连接失败或持续超时 | 面板所在网络到最终 URL 是否可达、TLS 与反代路径是否正确，是否有慢连接或排队。 |
| 401 | Secret 是否按原文一致、时间偏差、原始 body 是否被改写。 |
| ping 成功但解析未变 | 是否仍为演示模式；业务是否选用了该配置并允许实际写入；查看 put、权威查询和公开解析。 |
| 409 | 查看 stale_generation / version_conflict 和 watermark，核对状态后按面板内协议处理“切代”；不能以回 2xx 绕过冲突。 |
| 503 dns_not_applied / adapter_unavailable | 已确认未写入或程序未启动；检查命令路径、环境、密钥与 DNS 授权，修复后允许重试。 |
| 503 pending_delivery_requires_recovery | 写入结果未知；停接收端，按下述离线恢复步骤核实，保留数据库。 |

HTTP 重试行为随面板版本核对，尤其不要假定 **429 或所有 4xx 都自动重试**。本地命令退出码 3 与 HTTP 503 的关系、重试失败推送及重建基线的条件，应以该版本内置协议和 README 为准。

遇到 pending 时，停止接收端后可用 `python3 receiver.py --config config-real.json --inspect-pending` 查看在途内容。与 DNS 管理员核实请求是否已应用，并确认旧请求不可能再迟到执行；仅查询当前值、杀掉本地子进程或等一个超时都不能代替这个确认。然后严格按随包 README 选择 `--recover applied` 或 `--recover not-applied`，重启后再使用面板的 **重试失败推送**。无法确定时保留阻塞，不能删除 pending 或数据库绕过恢复。

接收端使用独立非特权账号，持久化并配套备份数据库、DNS 状态和受保护的凭证。示例是单进程串行接收端：慢客户端可占用它直到 10 秒 socket 超时，再加适配器执行可能超过面板默认 15 秒。应放在可信反代后，限制并发和空闲探活；多副本或高并发需要另外保证共享版本锁及未知结果隔离。

继续阅读：[动态域名与 DDNS](../dynamic-dns/index.md) · [服务器组](../server-groups/index.md) · [证书管理](../certificates/index.md)。
