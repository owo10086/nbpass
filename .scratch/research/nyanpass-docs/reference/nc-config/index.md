<!-- Source URL: https://nyanpass.pages.dev/reference/nc_config/ | Capture date: 2026-11-10 -->

# 配置文件模式

<a id="configyml"></a>
## 节点端配置文件 `config.yml` 说明文档

> **重要说明**
>
> - config.yml 配置文件模式在 nc20260310 以上版本可用。
> - 若要使用配置文件模式，请不带任何参数直接运行节点端程序，并在当前工作目录下放置好 `config.yml`。
> - 配置文件仅在程序启动时读取到内存，后续即使配置文件发生变化，也不会自动刷新。
> - 下方文档为 AI 生成，可能存在一些误导，目前仅保证 `参数名` 是真实存在的，具体行为可能与实际情况有出入。
> - 此模式主要面向自定义安装脚本的高级用户，若您使用 `一键对接` 脚本安装，目前不会采用此模式。

<a id="1"></a>
### 1. 基础认证配置

这部分是程序运行的核心，如果命令行未提供 `-u` 和 `-t` 参数，程序将强制读取这些项。

| **参数名** | **类型** | **说明** |
| --- | --- | --- |
| `base-url` | String | **面板地址**。后端管理界面的 URL 地址。 |
| `token` | String | **认证 Token**。用于身份验证的密钥。 |
| `is-outbound` | Boolean | **运行模式**。`true` 表示作为出口端（Outbound）运行；`false` 表示作为入口端运行。 |
| `use-ech` | Boolean | **使用 ECH**。`false` 与面板通信时，启用 ECH 功能。 |
| `ech-query-name` | String | **ECH 查询域名**。指定面板通信 ECH 用于查询公钥信息的域名。 |

---

<a id="2-listening-ports"></a>
### 2. 服务端监听配置 (Listening Ports)

定义本地服务监听的端口。如果设为 `0` 或留空，程序通常会随机分配端口。

| **参数名** | **类型** | **说明** |
| --- | --- | --- |
| `direct-port` | Uint16 | **Direct 协议端口**。常规 TCP 直接连接监听端口。 |
| `ws-port` | Uint16 | **WebSocket 协议端口**。用于混淆或过 CDN 的 WS 监听端口。 |
| `tls-port` | Uint16 | **TLS 协议端口**。加密传输层监听端口。 |
| `udp-port` | Uint16 | **UDP 协议端口**。用于传输 UDP 数据包的监听端口。 |
| `rev-port` | Uint16 | **反向控制流端口**。仅在入口端有效，用于接收反向连接。 |

---

<a id="3-connection-settings"></a>
### 3. 连接与路由配置 (Connection Settings)

定义如何连接到远程节点。

| **参数名** | **类型** | **说明** |
| --- | --- | --- |
| `connect-host` | String | **连接地址**。远程目标的主机名或 IP。若留空，程序通常会尝试获取本机公网 IP。 |
| `connect-direct-port` | Uint16 | **Direct 连接端口**。远程 Direct 协议端口，默认为同监听端口。 |
| `connect-ws-port` | Uint16 | **WS 连接端口**。远程 WebSocket 协议端口，默认为同监听端口。 |
| `connect-tls-port` | Uint16 | **TLS 连接端口**。远程 TLS 协议端口，默认为同监听端口。 |
| `connect-udp-port` | Uint16 | **UDP 连接端口**。远程 UDP 协议端口，默认为同监听端口。 |
| `connect-rev-port` | Uint16 | **反向连接端口**。远程反向控制流端口，默认为同监听端口。 |
| `default-weight` | Uint16 | **默认权重**。仅出口端有效，用于负载均衡时的流量分配权重（默认值为 1）。 |

---

<a id="configyml_1"></a>
### 配置示例 (`config.yml`)

最小示例：

```yaml
base-url: "https://api.example.com"
token: "your-secret-token-here"
is-outbound: true
```

---

<a id="_1"></a>
### 💡 使用注意事项

1. **优先级**：程序会优先判断命令行是否输入了 `-u` 和 `-t`。如果输入了，则**完全忽略** `config.yml`，直接使用命令行参数构建配置。
2. **必填项**：无论通过哪种方式，`base-url` 和 `token` 必须存在，否则程序将报错退出。
