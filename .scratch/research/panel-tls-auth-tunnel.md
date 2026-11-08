> 来源：Codex 调研 nekopass/Forwardx 的 TLS、登录与隧道证书

**nekopass / Forwardx：面板 TLS、管理员认证与入口→出口隧道调研**

调研日期：2026-11-08。采用静态源码阅读，未运行项目、测试、构建或安装，未修改两个参考项目。A 的 HEAD 为 `26a8dd847a10a9cf3582b9e235dcaae4502c5388`，B 为 `f4a32c2e1f6d2dff57da19932f4900233a75040b`；检查时两个工作区均干净。结论对应这两个源码快照，不代表实际部署配置或实测结果。

路径缩写：A = `/Users/macmini/Documents/Aixiaoji/源码参考/nekopass`；B = `/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx`。引用提供文件路径和行号。“未发现”限于本次检索到的源码；推断另外标注“推测”。B 没有内嵌完整 GOST 源码，因此额外只读核对了它固定版本的官方上游源码，相关引用使用 GitHub 链接。

先区分三条链路：**浏览器→面板、节点→面板、入口→出口**。三者的证书来源、身份认证和配置作用域不能互相代替。

**一、A：nekopass**

**A1. 面板对外暴露与 TLS**

**1. 网页/API 与节点控制端口**

网页、静态前端和 API 共用 HTTP 服务，默认 `:8080`；节点控制是另一个 gRPC 服务，默认 `:9443`。通过 `-http`、`-grpc` 修改，两个 Go listener 本身不共用端口。依据：[A/cmd/nekopass/main.go:36](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/cmd/nekopass/main.go:36)、[同文件:116](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/cmd/nekopass/main.go:116)、[同文件:124](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/cmd/nekopass/main.go:124)。

公网端口还取决于部署方式。安装脚本的代理模式把后端 gRPC 绑定到环回地址，节点看到的公网端口可以是 443。**推测**：外部代理可将网页和 gRPC 汇聚到同一个公网 443；不能因此说源码里的两个监听器是同端口。依据：[A/scripts/install-panel.sh:343](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/scripts/install-panel.sh:343)。

**2. 面板 TLS 证书来源与模式切换**

网页/API 服务只调用 `ListenAndServe()`，参数说明明确要求反向代理终止 TLS。因此，**网页/API 自身没有 ACME、上传证书或自签 HTTPS 模式**；对公网提供 HTTPS 要依靠外部代理。项目中虽然存在 ACME，但它管理的是隧道出口证书。依据：[A/cmd/nekopass/main.go:36](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/cmd/nekopass/main.go:36)、[同文件:134](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/cmd/nekopass/main.go:134)。

节点控制 gRPC 则有三种方式：

| 方式 | 源码行为 | 切换入口 |
|---|---|---|
| 代理终止 TLS | gRPC 后端使用 h2c，外部代理提供 HTTPS | 安装参数 `--agent-tls proxy`；不提供本地证书 |
| 加载已有证书 | gRPC 直接加载 PEM 文件，最低 TLS 1.2 | `--agent-tls existing`；`NEKOPASS_GRPC_TLS_CERT/KEY` |
| 明文 | 节点以 `http://` 连接 h2c | `--agent-tls plain`；系统设置 `agent_transport=plain` |

`NEKOPASS_GRPC_TLS_CERT/KEY` 均未设置时兼容旧的 `NEKOPASS_TLS_CERT/KEY`。本地证书路径来自进程环境；系统设置的 transport 决定是否选择明文方式。已有证书安装流程要求证书可被系统信任，并检查有效期、密钥和名称。没有自动申请控制平面证书的流程。依据：[A/cmd/nekopass/main.go:118](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/cmd/nekopass/main.go:118)、[A/internal/control/agent_listener.go:29](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/control/agent_listener.go:29)、[A/scripts/install-panel.sh:223](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/scripts/install-panel.sh:223)。

**3. 节点校验面板证书与节点 token**

节点连接 `https://host:port` 或旧式 `host:port` 时使用 Go/gRPC 标准 TLS：系统根证书、主机名校验，最低 TLS 1.2；没有证书固定、自定义 CA 配置或跳过证书校验开关。`http://` 明确选择无 TLS 的 h2c，不是“HTTPS 忽略证书错误”，也不会因 TLS 失败自动降级。依据：[A/internal/agent/control_endpoint.go:15](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/control_endpoint.go:15)。

节点 token 的生成、保存与轮换如下：

- 默认由 `crypto/rand` 生成 32 字节，hex 编码为 64 字符；也允许管理员指定符合校验规则的 token。依据：[A/internal/control/server.go:79](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/control/server.go:79)、[A/internal/control/nodes.go:252](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/control/nodes.go:252)。
- **数据库同时保存明文 `nodes.token` 和 SHA-256 `nodes.token_hash`**；鉴权按哈希查找，管理员仍能取回明文。不是只保存哈希。依据：[A/internal/control/nodes.go:276](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/control/nodes.go:276)、[同文件:300](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/control/nodes.go:300)、[同文件:487](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/control/nodes.go:487)。
- 节点安装脚本把明文写进权限 `0600` 的 `agent.env`，进程读取 `NEKOPASS_NODE_TOKEN`；gRPC metadata 携带 `authorization: Bearer <token>`。依据：[A/scripts/install-agent.sh:267](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/scripts/install-agent.sh:267)、[A/internal/agent/client.go:67](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/client.go:67)。
- 轮换由管理员修改节点 token 完成；未发现定期自动轮换。更新后，旧流在下一轮配置交换检查到 token hash 改变并拒绝，节点需更新本地配置后重连；未使用的安装票据也会失效。依据：[A/internal/control/stream.go:106](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/control/stream.go:106)、[A/internal/control/nodes.go:333](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/control/nodes.go:333)。

**A2. 管理员登录与会话**

**1. 账号与密码存储**

采用本地数据库账号，区分管理员与普通用户，并支持停用。管理员通过 bootstrap 创建，密码使用 `golang.org/x/crypto/bcrypt` 的 `DefaultCost`；登录密码长度要求 12–72 字节。重置管理员密码的本地 CLI 会更新 bcrypt 并删除其会话。依据：[A/internal/store/schema.sql:3](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/store/schema.sql:3)、[A/internal/control/server.go:359](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/control/server.go:359)、[同文件:435](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/control/server.go:435)、[A/internal/control/admin_cli.go:58](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/control/admin_cli.go:58)。

**2. 二步验证与恢复**

当前未发现 TOTP、Passkey/WebAuthn 或其他管理员登录第二因素。登录输入只有用户名与密码，密码通过后直接创建会话，因此也没有二步验证的开启、恢复码或恢复流程。注册邮箱验证码属于注册验证，不等于管理员登录 MFA。依据：[A/internal/control/server.go:348](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/control/server.go:348)、[同文件:381](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/control/server.go:381)。

**3. 会话、限流与 CSRF**

使用**服务端数据库 cookie session，非 JWT**。cookie 保存随机 token，数据库只存会话 token 的 SHA-256。但新建 session 的 `expires_at=NULL`，服务端没有固定到期时间；cookie 的 `MaxAge` 为 365 天，活跃访问最多每日续一次 cookie。不能将它描述为“服务器一年后强制过期”。依据：[A/internal/control/login.go:85](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/control/login.go:85)、[A/internal/control/sessions.go:10](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/control/sessions.go:10)、[A/internal/store/queries.sql:7](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/store/queries.sql:7)。

cookie 设置 HttpOnly、SameSite=Strict；识别为 HTTPS 时设置 Secure。HTTPS 与 HTTP 使用不同 cookie 名，外部协议只从可信代理读取。登出删除会话，修改密码、停用账号或管理员操作可撤销会话。依据：[A/internal/control/proxy.go:39](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/control/proxy.go:39)、[A/internal/control/server.go:404](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/control/server.go:404)、[A/internal/control/users.go:150](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/control/users.go:150)。

防爆破使用 IP、账号两个维度的内存令牌桶，每 12 秒恢复一个令牌，IP burst=5、账号 burst=10；昂贵密码校验最多并发 4 个。不存在或已停用账号也执行 dummy bcrypt，减少时序枚举。依据：[A/internal/control/server.go:67](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/control/server.go:67)、[同文件:340](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/control/server.go:340)、[A/internal/control/login.go:21](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/control/login.go:21)、[同文件:47](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/control/login.go:47)。

CSRF 使用组合检查，未见独立同步 CSRF token：非 GET/HEAD 请求拒绝 `Sec-Fetch-Site: cross-site/same-site`；若存在 Origin，须匹配请求 Host 和外部协议；要求 JSON Content-Type；cookie 为 SameSite Strict。支付 callback 例外。Origin 缺失不是一律拒绝，因此脚本仍可在通过鉴权后调用。依据：[A/internal/control/server.go:283](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/control/server.go:283)。

**4. 脚本 API Token**

有独立管理 API key：站点级单把，绑定生成它的管理员。通过 `POST /api/v1/admin/settings/api-key` 创建或替换，`DELETE` 撤销，数据库只存 SHA-256；通过 Bearer 使用，只接受 `/api/v1/admin/`，并检查所属账号仍启用且仍为管理员。未发现多把 key、细粒度 scope 或自动过期。它必须与节点 token 分离。依据：[A/internal/control/server.go:143](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/control/server.go:143)、[A/internal/control/system_settings.go:268](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/control/system_settings.go:268)、[同文件:328](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/control/system_settings.go:328)。

**A3. 入口→出口隧道的证书、伪装与鉴权**

**1. 传输与运行时**

**当前没有 WS/WSS 隧道。** 源码支持以下组合：

| 传输 | 模式 |
|---|---|
| raw TCP | `plain_tcp`、`tls_tcp`、`random_tcp` |
| HTTP/2 | `plain_h2`、`tls_h2`、`random_h2` |
| raw UDP | `plain_udp`、`dtls_udp`、`random_udp` |
| HTTP/3 | `tls_h3` |

H2 使用 HTTP 流；H3 基于 QUIC，亦支持 CONNECT-UDP。`random` 是另一个加密封装，不能称为 TLS。隧道逻辑在 Go Agent 内实现，链接 http2、yamux、pion/dtls、quic-go、masque-go、uTLS 等库，未见使用 GOST/Xray/sing-box 子进程承担这些隧道。依据：[A/internal/tunnel/mode.go:10](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/tunnel/mode.go:10)、[A/go.mod:6](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/go.mod:6)、[A/internal/agent/h3_server.go:155](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/h3_server.go:155)。

**2. 证书来源、签发与入口验证**

出口节点的 `certificate_mode` 可选 `self_signed`、`import`、`acme_http`、`acme_dns`，默认自签。

- **自签由面板生成**：每个出口独立 P-256 密钥与自签证书，含域名 SAN，有效期两年，`IsCA=true`；面板将该证书作为此出口的额外信任根。不是出口自己生成，也不是统一根 CA 给全部出口签叶证书。依据：[A/internal/control/tls_config.go:62](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/control/tls_config.go:62)、[同文件:205](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/control/tls_config.go:205)、[A/internal/control/tls_nodes.go:50](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/control/tls_nodes.go:50)。
- **手动导入**：提交 PEM 证书、私钥和可选 RootCA，检查密钥匹配、证书覆盖配置名称、有效期。依据：[A/internal/control/tls_nodes.go:63](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/control/tls_nodes.go:63)、[A/internal/control/tls_config.go:230](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/control/tls_config.go:230)。
- **ACME**：使用 `go-acme/lego/v4 v4.35.2`，默认 Let's Encrypt production；HTTP-01/DNS-01 二选一，明确关闭 TLS-ALPN-01。DNS 支持 Cloudflare、阿里 DNS、DNSPod。面板申请并保存证书/私钥，再下发出口；HTTP challenge 下发节点应答，默认端口 80。ACME 在到期前 30 天进入续期扫描。依据：[A/go.mod:6](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/go.mod:6)、[A/internal/control/acme_worker.go:47](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/control/acme_worker.go:47)、[同文件:121](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/control/acme_worker.go:121)、[同文件:228](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/control/acme_worker.go:228)、[A/internal/agent/acme_http.go:78](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/acme_http.go:78)。

面板向出口发送 cert/key，向入口发送所选出口的 RootCA、ServerName 等。入口采用**系统根 + 额外 RootCA，正常校验证书链与名称**；没有跳过校验，也不是严格的 leaf/SPKI 固定，因为系统信任根仍被保留。DTLS/H3 同样使用相应信任配置。`fingerprint=chrome/firefox` 指 ClientHello 指纹，不是证书指纹。依据：[A/internal/control/tls_nodes.go:117](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/control/tls_nodes.go:117)、[同文件:159](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/control/tls_nodes.go:159)、[A/internal/agent/tunnel.go:94](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/tunnel.go:94)、[A/internal/agent/h2_tls_client.go:29](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/h2_tls_client.go:29)、[A/internal/agent/h3_client.go:270](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/h3_client.go:270)。

**3. 伪装参数及配置层次**

主要参数放在**出口节点 `nodes.tls_config`**，入口按选中的出口取得配置；不是每条规则或出口组统一设置一套外层参数。

| 参数 | 实际作用 |
|---|---|
| `server_name` / `client_sni` | 默认使用出口名称，可覆盖客户端 SNI；仍须通过名称校验 |
| `host` / `path` | H2/H3 请求路由；Host 默认出口名称，Path 默认 `/api/stream` |
| `fingerprint` | `off/chrome/firefox`，使用 uTLS Chrome_Auto / Firefox_Auto；H3 不使用该指纹 |
| ALPN | 由传输决定：raw TLS 为 `http/1.1`，H2 为 `h2`，H3 为 `h3`；不是任意填写项 |
| `fallback_url` | 普通请求或不符合隧道请求的流量回落到网站；未配置则 404 |
| pool、window、max streams | 连接池与资源控制 |

依据：[A/internal/control/tls_config.go:24](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/control/tls_config.go:24)、[A/internal/control/tls_nodes.go:169](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/control/tls_nodes.go:169)、[A/internal/agent/h2_tls_client.go:18](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/h2_tls_client.go:18)。

H2 出口检查 Path、Host、请求方法和 HTTP/2。网站回落使用反向代理，转发前移除 Authorization 与隧道元数据；部分尚未完成 TLS 握手的流量也可透传捕获的 ClientHello 给 HTTPS fallback。依据：[A/internal/agent/h2_server.go:125](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/h2_server.go:125)、[A/internal/agent/h2_camouflage.go:42](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/h2_camouflage.go:42)、[A/internal/agent/h2_probe_listener.go:133](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/h2_probe_listener.go:133)。

规则还有 `sni_names`、`tls_alpn`、`origin_tls`，作用于业务 TLS 入站或目标连接，不能与上述入口→出口外层伪装混为一谈。出口组选择候选出口，候选仍使用各自证书与传输参数。依据：[A/internal/control/rules.go:23](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/control/rules.go:23)、[A/internal/control/egress_groups.go:145](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/control/egress_groups.go:145)。

**4. 连接复用**

raw TCP 可选 HashiCorp **yamux**，外面由项目包装连接池与限制；H2 使用原生多流和连接池，默认 pool=2、max streams=256；H3 使用 QUIC/HTTP3 原生流；raw UDP 的 mux 为自写 flow/datagram association，不是 yamux。依据：[A/internal/agent/raw_mux.go:15](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/raw_mux.go:15)、[同文件:475](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/raw_mux.go:475)、[A/internal/agent/h2_client.go:350](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/h2_client.go:350)、[A/internal/agent/native_mux.go:20](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/native_mux.go:20)。

**5. 入口→出口鉴权与防滥用**

面板为每对入口/出口生成独立 `node_tunnel_links.token`，与节点控制 token、管理 API key 分开；link token 明文保存在数据库，切换隧道协议时重新生成。依据：[A/internal/control/nodes.go:359](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/control/nodes.go:359)。

raw TCP 首帧包含入口 ID、规则 ID、link token 和目标；出口既核对凭据，也核对规则归属、用户/配额 epoch、有效期和**面板授权的目标列表**。H2/H3 使用 link token 生成 HMAC，并绑定 TLS exporter、请求元数据、nonce/序列等；H2 还有响应证明。这样，知道隧道端口和 Path 本身不能获得转发权限。依据：[A/internal/agent/tunnel.go:38](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/tunnel.go:38)、[同文件:383](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/tunnel.go:383)、[A/internal/agent/h2_client.go:26](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/h2_client.go:26)、[A/internal/agent/h2_server.go:147](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/h2_server.go:147)、[A/internal/agent/h3_server.go:189](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/h3_server.go:189)。

mux 载体及逻辑流也检查授权；持续连接重新检查策略，raw TCP 每秒检查，撤销后可关闭已有连接。明文 TCP 的首帧 token 没有 TLS 保护，这是该模式的明确边界。依据：[A/internal/agent/raw_mux.go:589](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/raw_mux.go:589)、[A/internal/agent/tunnel.go:414](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/tunnel.go:414)。

**二、B：Forwardx**

**B1. 面板对外暴露与 TLS**

**1. 网页/API 与节点控制端口**

网页、管理 `/api/trpc`、Agent HTTP API、SSE 推送共用同一 Express HTTP(S) 服务，默认 **9810**，可通过 `PORT` 修改；没有独立 gRPC 控制端口。当前 Agent 还把业务请求封装为 `/api/sync`。生产模式使用指定端口，开发模式可向后找最多 20 个可用端口。依据：[B/server/index.ts:126](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/index.ts:126)、[同文件:151](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/index.ts:151)、[同文件:163](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/index.ts:163)、[B/server/agentRoutes.ts:276](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/agentRoutes.ts:276)、[B/agent/main.go:4736](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/agent/main.go:4736)。

**2. 面板 TLS 证书来源与切换**

**可以直接提供 HTTPS**。`panelSslEnabled` 控制启停；`panelSslMode=path|pem` 分别读取证书/私钥文件，或使用保存的 PEM 内容。数据库设置优先于环境变量。自签功能调用 OpenSSL 生成 RSA-2048/SHA-256 证书，含 SAN，默认 825 天，私钥文件权限 0600。依据：[B/server/panelSsl.ts:36](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/panelSsl.ts:36)、[同文件:77](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/panelSsl.ts:77)、[同文件:171](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/panelSsl.ts:171)、[B/server/index.ts:128](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/index.ts:128)。

设置接口先验证后保存，启停或活动证书配置变化会安排面板重启。单独生成自签证书只更新来源/路径，不等于自动启用 HTTPS。未发现面板 ACME 客户端或 HTTP-01/DNS-01 申请流程；真实域名证书应由外部工具申请后导入。也可部署在反向代理后。依据：[B/server/_core/systemRouter.ts:2595](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/_core/systemRouter.ts:2595)、[同文件:2625](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/_core/systemRouter.ts:2625)、[同文件:2634](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/_core/systemRouter.ts:2634)。

特别注意：**已启用 HTTPS 但启动时证书加载/验证失败，代码会回退 HTTP，而非拒绝启动**。依据：[B/server/panelSsl.ts:270](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/panelSsl.ts:270)。

**3. 节点校验面板证书与 token**

Agent 管理连接使用 Go 标准 HTTP Transport，最低 TLS 1.2，保留系统根与名称验证；没有管理连接的 skip verify、证书固定或自定义 CA 开关。**推测**：使用面板自签证书时，需把相应信任材料安装进节点系统信任库，否则 HTTPS 校验会失败。配置为 HTTP 时则没有 TLS。依据：[B/agent/http_client.go:34](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/agent/http_client.go:34)。

Agent token 由 `nanoid(32)` 生成，`agent_tokens.token` 与 `hosts.agentToken` **明文保存**，列表打码不改变存储方式；管理员可取回完整安装 token。节点的 `/etc/forwardx/agent/config.json` 同样保存明文 token，安装时设 0600。依据：[B/server/routers/agentTokens.ts:30](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/routers/agentTokens.ts:30)、[同文件:89](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/routers/agentTokens.ts:89)、[B/drizzle/schema.ts:895](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/drizzle/schema.ts:895)、[B/server/agentInstallScripts.ts:1424](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/agentInstallScripts.ts:1424)。

新鉴权由 token 派生 HMAC 密钥，证明绑定方法、路径和 body hash，支持 nonce/时间戳及一次性 challenge-v2；仍保留原始 Bearer token 的兼容路径。Agent 消息另有 AES-256-CTR + HMAC 信封。这里的 token fingerprint 是查找标识，**不是 TLS 证书固定**。依据：[B/server/agentCrypto.ts:44](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/agentCrypto.ts:44)、[同文件:138](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/agentCrypto.ts:138)、[同文件:213](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/agentCrypto.ts:213)、[B/server/agentAuth.ts:81](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/agentAuth.ts:81)。

未发现原地周期轮换或双 token 平滑过渡机制，也没有 token 到期字段。现有操作是创建新 token、重新配置节点、删除旧 token；删除会清空关联主机 token 并使鉴权缓存失效，但可能被现存规则/转发组引用阻挡，不能描述为无损在线轮换。依据：[B/server/routers/agentTokens.ts:56](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/routers/agentTokens.ts:56)、[B/server/repositories/tokenRepository.ts:186](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/repositories/tokenRepository.ts:186)。

**B2. 管理员登录与会话**

**1. 账号与密码存储**

本地多用户账号，角色为 `admin/user`，支持账号停用；也有 Google OAuth、绑定 Telegram 后的替代登录路径。密码采用 **Node `crypto.scryptSync`**：16 字节随机盐、64 字节派生结果，保存为 `salt:hash`，使用恒时比较；不存在账号也做 dummy scrypt。不是 bcrypt/argon2。依据：[B/drizzle/schema.ts:151](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/drizzle/schema.ts:151)、[B/server/password.ts:3](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/password.ts:3)、[B/server/googleAuthRoutes.ts:50](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/googleAuthRoutes.ts:50)、[B/server/routers/telegram.ts:183](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/routers/telegram.ts:183)。

**2. 二步验证、开启与恢复**

支持 **TOTP**，未发现 Passkey/WebAuthn 或备份恢复码。全局 `twoFactorEnabled` 默认 false；管理员先开放此功能，用户再生成绑定密钥/二维码 URI，使用当前密码与 TOTP 确认启用；正常停用也要求密码和 TOTP。TOTP 为 20 字节随机 secret、HMAC-SHA1、6 位、30 秒周期，默认接受前后一个时间步。secret 以数据库文本保存，未见应用层加密。依据：[B/server/dbSchema.ts:573](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/dbSchema.ts:573)、[B/server/routers/auth.ts:511](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/routers/auth.ts:511)、[同文件:528](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/routers/auth.ts:528)、[同文件:567](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/routers/auth.ts:567)、[B/server/totp.ts:41](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/totp.ts:41)、[B/server/repositories/userRepository.ts:169](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/repositories/userRepository.ts:169)。

密码通过但仍需 TOTP 时，发放 5 分钟、最多 5 次尝试的 challenge，并绑定来源信息。恢复手段是**管理员移除目标用户的 2FA**。若唯一管理员丢失认证器又没有有效会话，未发现专门恢复入口；现有管理员密码重置 CLI 明确保留 2FA，不能用它解除 TOTP。依据：[B/server/twoFactorChallenges.ts:13](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/twoFactorChallenges.ts:13)、[B/server/routers/users.ts:185](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/routers/users.ts:185)、[B/server/resetAdminPasswordCli.ts:186](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/resetAdminPasswordCli.ts:186)。

还存在一个范围边界：密码登录和 Google 回调会检查本地 TOTP；Telegram 登录码、Widget、WebApp 验证各自凭据后，直接签发 Telegram 会话，未追加本地 TOTP。不能笼统说“所有登录路径均强制二步验证”。依据：[B/server/routers/auth.ts:336](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/routers/auth.ts:336)、[B/server/googleAuthRoutes.ts:50](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/googleAuthRoutes.ts:50)、[B/server/routers/telegram.ts:183](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/routers/telegram.ts:183)、[同文件:340](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/routers/telegram.ts:340)。

**3. 会话、限流与 CSRF**

会话为 **JWT + 数据库可撤销记录**。JWT 含 `userId/sid/kind`，由 `cookieSecret` 签名，固定有效期 10 天；浏览器用 HttpOnly、SameSite=Lax cookie，HTTPS 时 Secure=true；也接受 Bearer 登录 JWT。每次认证同时验证 JWT、数据库会话和账号状态。依据：[B/server/routers/auth.ts:113](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/routers/auth.ts:113)、[B/server/session.ts:5](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/session.ts:5)、[B/server/_core/cookies.ts:4](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/_core/cookies.ts:4)、[B/server/_core/context.ts:126](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/_core/context.ts:126)。

注销、改密码等可撤销会话。存在 browser/mobile/telegram 分类与多设备设置，活动租约 120 秒、约 30 秒刷新，但**租约刷新不延长 JWT 有效期**。未发现 refresh token/自动续发 JWT；会话触碰只更新 lastSeenAt。依据：[B/server/session.ts:1](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/session.ts:1)、[B/server/loginSessionService.ts:19](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/loginSessionService.ts:19)、[B/server/repositories/sessionRepository.ts:56](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/repositories/sessionRepository.ts:56)。

防爆破按 30 分钟失败窗口计数：IP+账号 8 次、IP 40 次后，按最后失败时间阻断 15 分钟。密码失败不直接累加全局账号锁定，2FA 失败额外累加账号维度；challenge 发放亦限流。还有默认开启的验证码机制，可选择 Cap 工作量证明或图片验证码，默认失败 3 次后要求验证。上述计数主要存在进程内存。依据：[B/server/authRateLimit.ts:19](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/authRateLimit.ts:19)、[同文件:67](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/authRateLimit.ts:67)、[B/server/authCaptchaConfig.ts:4](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/authCaptchaConfig.ts:4)、[B/shared/authCaptcha.ts:1](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/shared/authCaptcha.ts:1)。

CSRF 方面，未发现管理 tRPC 全局 CSRF token 或统一 Origin/Referer 校验；明确存在 SameSite=Lax、受限 CORS 以及 tRPC 请求形式约束。**这不足以宣称有完整显式 CSRF 防护，也不能仅凭没有 CSRF token 就断言已经可利用。** `/mcp` 另有自己的 Origin 检查。依据：[B/server/index.ts:62](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/index.ts:62)、[同文件:133](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/index.ts:133)、[B/server/_core/trpc.ts:38](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/_core/trpc.ts:38)、[B/server/mcpRoutes.ts:45](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/mcpRoutes.ts:45)。

**4. 脚本 API Token**

存在独立 **MCP Token**：`fxm_` 加 32 随机字节，数据库只存 SHA-256；可选 7/30/90 天，每管理员最多 10 个，可撤销。它仅供 `/mcp` 的管理员只读工具调用，不是通用可写管理 API key。普通 tRPC 可使用登录 JWT 作为 Bearer，但受 10 天期限和会话撤销约束。依据：[B/server/mcpTokens.ts:12](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/mcpTokens.ts:12)、[B/server/routers/mcp.ts:5](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/routers/mcp.ts:5)、[B/server/mcpRoutes.ts:31](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/mcpRoutes.ts:31)、[B/server/_core/context.ts:57](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/_core/context.ts:57)。

**B3. 入口→出口隧道的证书、伪装与鉴权**

**1. 传输与运行时**

B 不是单一隧道实现，应分三族：

| 隧道族 | 暴露的模式与实现 |
|---|---|
| GOST | `tcp/tls/wss/mtcp/mtls/mwss`，由 Agent 管理第三方 GOST 衍生运行时 |
| ForwardX FXP | 项目自写 Go `forwardx-fxp`；v1/v2 为自有加密协议，v3 外层可选 `http/ws/tls` |
| Nginx Stream | `nginx_stream`，生成 Nginx stream 转发配置，TCP 可选 TLS，UDP 仍按 UDP 转发 |

依据：[B/server/routers/tunnels.ts:55](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/routers/tunnels.ts:55)、[B/server/tunnelRuntimePlan.ts:44](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/tunnelRuntimePlan.ts:44)、[B/forwardx-fxp/v3_transport.go:25](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/forwardx-fxp/v3_transport.go:25)、[B/server/agentHeartbeatRoute.ts:319](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/agentHeartbeatRoute.ts:319)。

GOST 衍生运行时固定为 `3.2.6-forwardx.2`，来源 commit `340ba32ef0bebc7293908007cc423dd5f33dd88c`；对应依赖 `go-gost/x v0.8.1`。构建脚本保留上游协议并增加本地补丁。依据：[B/shared/runtimeBuild.ts:2](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/shared/runtimeBuild.ts:2)、[B/scripts/build-runtime.sh:30](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/scripts/build-runtime.sh:30)、[GOST/go.mod:8](https://github.com/go-gost/gost/blob/340ba32ef0bebc7293908007cc423dd5f33dd88c/go.mod#L8)。

**FXP V3 的 `ws` 不等于标准 WebSocket/WSS**：握手使用 Upgrade，但后续直接承载私有字节流，没有 RFC6455 数据帧；且 `ws` 与 `tls` 是互斥外层选项，源码没有组合成自写 WSS 模式。标准 WSS 能力来自 GOST 的 `wss/mwss`。依据：[B/forwardx-fxp/internal/v3/http.go:55](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/forwardx-fxp/internal/v3/http.go:55)、[B/forwardx-fxp/v3_transport.go:61](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/forwardx-fxp/v3_transport.go:61)、[GOST-x/dialer/ws/dialer.go:103](https://github.com/go-gost/x/blob/v0.8.1/dialer/ws/dialer.go#L103)。

**2. 证书来源与入口验证**

| 模式 | 出口证书来源 | 入口怎样校验 |
|---|---|---|
| GOST TLS/WSS 及复用版本 | 当前面板没有下发隧道证书。运行时先尝试默认 `cert.pem/key.pem/ca.pem`，加载失败就生成随机自签默认证书 | 当前生成配置未设置 `secure`、CA 或 pin；上游默认 `InsecureSkipVerify=true`，跳过服务端身份校验 |
| FXP V3 TLS | 运行时生成自签证书；Ed25519 私钥由隧道 PSK 经带用途隔离前缀的 SHA-256 派生 | 关闭普通 PKI 校验，但 `VerifyConnection` 强制对端 Ed25519 公钥与 PSK 派生公钥相等，属于公钥固定 |
| Nginx Stream TCP TLS | 管理员在隧道中导入 `certPem/certKeyPem`，面板下发文件；未配置则普通 TCP | 明确生成 `proxy_ssl_verify off`，不校验证书链/名称；可发送 `certDomain` 作为 SNI |

GOST 证据：本地只给 dialer/listener 传 `type`，没有 TLS 信任配置，见 [B/server/agentHeartbeatRoute.ts:3563](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/agentHeartbeatRoute.ts:3563)、[同文件:3894](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/agentHeartbeatRoute.ts:3894)。上游零值 TLS 配置见 [GOST-x/config/parsing/node/parse.go:89](https://github.com/go-gost/x/blob/v0.8.1/config/parsing/node/parse.go#L89)，`InsecureSkipVerify=!Secure` 见 [GOST-x/internal/util/tls/tls.go:193](https://github.com/go-gost/x/blob/v0.8.1/internal/util/tls/tls.go#L193)；默认证书生成与使用见 [GOST-x/config/parsing/tls.go:36](https://github.com/go-gost/x/blob/v0.8.1/config/parsing/tls.go#L36)、[GOST-x/config/parsing/service/parse.go:68](https://github.com/go-gost/x/blob/v0.8.1/config/parsing/service/parse.go#L68)。这是沿配置与固定上游实现追踪的结论，未运行验证。

FXP 证据：[B/forwardx-fxp/v3_transport.go:68](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/forwardx-fxp/v3_transport.go:68)、[同文件:119](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/forwardx-fxp/v3_transport.go:119)。因此不能只看到其中 `InsecureSkipVerify:true` 就归类为无校验；它没有执行普通证书有效期/域名/CA 校验，但有自己的公钥固定校验。该自签证书模板有效期为 2020–2120 年，也不是自动 ACME 真域名证书。

Nginx 证据：[B/server/routers/tunnels.ts:279](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/routers/tunnels.ts:279)、[B/server/agentHeartbeatRoute.ts:339](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/agentHeartbeatRoute.ts:339)、[同文件:400](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/agentHeartbeatRoute.ts:400)、[同文件:4193](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/agentHeartbeatRoute.ts:4193)。这几种隧道模式均未发现面板统一 CA 签发或内建 ACME 申请/续期流程；手工导入 Nginx 的证书可以来自外部公共 CA。

**3. 伪装配置与层次**

主要位于**隧道对象**，不是每条规则或出口组：FXP V3 保存 `v3Transport/v3Host/v3Path/v3UserAgent/v3ServerName/v3Multiplex`；Nginx 保存 `certDomain/certPem/certKeyPem`。依据：[B/server/routers/tunnels.ts:58](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/routers/tunnels.ts:58)、[同文件:940](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/routers/tunnels.ts:940)。

FXP V3 TLS 使用 `v3ServerName` 作为 SNI，未填写时为 `forwardx.invalid`，ALPN 固定 `http/1.1`，使用标准 Go TLS；未发现 uTLS。HTTP/WS 支持 Path 和 User-Agent，但**`v3Host` 虽然存在于表单、校验及函数参数里，当前请求构造没有输出 Host 头，服务端也未据它核对 Host**。不能仅凭字段名声称 Host 伪装已经生效。依据：[B/forwardx-fxp/v3_transport.go:64](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/forwardx-fxp/v3_transport.go:64)、[B/forwardx-fxp/internal/v3/http.go:61](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/forwardx-fxp/internal/v3/http.go:61)、[同文件:176](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/forwardx-fxp/internal/v3/http.go:176)。

GOST 运行时本身支持 WS Host/Path 等元数据，但当前面板生成的节点配置仅设置传输类型，没有接入这些定制字段；不能把上游全部能力都算作项目面板已支持的功能。未发现这三族有 A 那样的真实网站 fallback 或面板可选 uTLS 指纹。Nginx `certDomain` 只负责其客户端 SNI，不是 HTTP Host/Path。依据：[B/server/agentHeartbeatRoute.ts:3563](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/agentHeartbeatRoute.ts:3563)、[同文件:346](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/agentHeartbeatRoute.ts:346)、[GOST-x/dialer/ws/metadata.go:30](https://github.com/go-gost/x/blob/v0.8.1/dialer/ws/metadata.go#L30)。

**4. 连接复用**

GOST 的 `mtcp/mtls/mwss` 是复用传输，对应 **xtaci/smux**。这里的 `mtls` 不能按通常的 mTLS 缩写理解为“客户端双向证书认证”；当前生成配置也未配置客户端证书。FXP V3 默认打开 `v3Multiplex`，使用项目自写 session/stream/frame 层，单 session 最多 128 流，另有物理连接池；不是 yamux/smux。Nginx Stream 没有这类应用层多流复用。依据：[GOST-x/dialer/mtls/dialer.go:133](https://github.com/go-gost/x/blob/v0.8.1/dialer/mtls/dialer.go#L133)、[GOST-x/dialer/mws/dialer.go:197](https://github.com/go-gost/x/blob/v0.8.1/dialer/mws/dialer.go#L197)、[GOST-x/internal/util/mux/mux.go:9](https://github.com/go-gost/x/blob/v0.8.1/internal/util/mux/mux.go#L9)、[B/forwardx-fxp/v3_transport.go:50](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/forwardx-fxp/v3_transport.go:50)、[B/forwardx-fxp/v3_session.go:29](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/forwardx-fxp/v3_session.go:29)、[B/forwardx-fxp/internal/v3/session.go:81](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/forwardx-fxp/internal/v3/session.go:81)。

**5. 入口→出口鉴权与防滥用**

**GOST**：规则使用带用户名/密码的 relay connector/handler，密码由隧道 secret、tunnel ID、rule ID 经 SHA-256 派生；中间 hop 另有隧道级 relay 凭据。面板给入口 connector 和出口 handler 下发一致凭据。因此，知道端口并不足以通过业务 relay 鉴权。不过，GOST TLS 默认跳过证书校验的结论仍成立，认证与服务端证书验证是两件事。依据：[B/server/gostTunnelProtocol.ts:15](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/gostTunnelProtocol.ts:15)、[同文件:45](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/gostTunnelProtocol.ts:45)、[B/server/agentHeartbeatRoute.ts:3887](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/agentHeartbeatRoute.ts:3887)。

**FXP**：新隧道 secret 使用 32 随机字节。v1/v2 有 PSK 派生 AEAD、握手和防重放；v3 有随机握手材料、HMAC 双向证明、HKDF 派生的分方向 AEAD，关闭 mux 也保留内层认证。V3 TLS 的公钥固定额外验证服务端。依据：[B/server/routers/tunnels.ts:1029](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/routers/tunnels.ts:1029)、[B/forwardx-fxp/main.go:2502](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/forwardx-fxp/main.go:2502)、[同文件:1821](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/forwardx-fxp/main.go:1821)、[B/forwardx-fxp/internal/v3/inner.go:143](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/forwardx-fxp/internal/v3/inner.go:143)、[同文件:269](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/forwardx-fxp/internal/v3/inner.go:269)。

但 FXP 的认证主要证明持有隧道 PSK。出口业务处理读取客户端 hello 的 TargetIP/TargetPort 后直接拨号，未见 A 那样针对每次请求核对面板授权目标清单；因此不应把“PSK 隧道认证”描述为同等强度的“每规则目标授权”。依据：[B/forwardx-fxp/main.go:1852](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/forwardx-fxp/main.go:1852)、[同文件:1886](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/forwardx-fxp/main.go:1886)。

**Nginx Stream**：当前生成配置只监听并转发到固定目标，没有 relay token 或客户端证书验证。**推测**：若出口业务端口能被外部直接访问，且没有额外防火墙/来源限制，第三方可直接使用该端口转发到固定目标；TLS 自身不提供入口身份鉴权。这不等于允许任意指定目标的通用代理。依据：[B/server/agentHeartbeatRoute.ts:319](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/agentHeartbeatRoute.ts:319)、[同文件:4193](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/agentHeartbeatRoute.ts:4193)。

**三、做法对比表**

以下为前述实现结论的归纳，引用见对应项目小节。

| 方面 | A：nekopass | B：Forwardx |
|---|---|---|
| 网页/API 与节点控制 | 默认 HTTP 8080 + 独立 gRPC 9443 | 网页、API、Agent HTTP/SSE 默认共用 9810 |
| 网页面板 TLS | 外部反向代理终止 | 可直接 HTTPS：文件、PEM、自签；也可反代 |
| 面板内建 ACME | 没有；其 ACME 属于隧道 | 未发现 |
| 节点校验面板 | 系统根/名称；可明确选择 h2c | 系统根/名称；亦允许 HTTP URL |
| 节点 token 保存/轮换 | 明文 + SHA-256；管理员更新，旧流下一轮拒绝 | 明文；新建/重配/删除旧 token，删除可能受规则引用阻挡 |
| 管理员密码 | bcrypt DefaultCost | scrypt，随机盐 |
| MFA | 未发现 | TOTP；无恢复码；Telegram 未追加本地 TOTP |
| 会话 | DB cookie session；服务端无固定到期；cookie 365 天、每日续 cookie | JWT + DB 撤销；10 天；未见 refresh token |
| 登录防爆破 | IP/账号限流、bcrypt 并发限制 | 分层失败限流、TOTP challenge 限流、验证码 |
| 显式 CSRF 机制 | Strict + Fetch Metadata/Origin/JSON 检查 | Lax + CORS 等；未见管理 tRPC 统一 CSRF/Origin 校验 |
| 脚本凭据 | 单把管理 API key，哈希存储 | 只读 MCP Token；普通 tRPC 可用登录 JWT |
| 隧道实现 | Agent 内自写协议，链接 Go 协议库；没有 WSS | GOST、FXP、Nginx 三族；标准 WSS 在 GOST |
| 隧道证书 | 面板按出口自签、导入、lego ACME | GOST 默认自签；FXP V3 自签/PSK 公钥固定；Nginx 手动导入 |
| 入口验证出口 | 系统 CA + 额外信任根 + 名称 | GOST/Nginx 默认不验证；FXP V3 TLS 公钥固定 |
| 伪装层次 | 主要按出口节点；SNI/Host/Path/uTLS/fallback | 主要按隧道；FXP V3 部分字段有实现落差，无同等 fallback |
| mux | raw TCP yamux；H2/H3 原生多流；UDP 自写 | GOST smux；FXP V3 自写多流；Nginx 无此类 mux |
| 防止直连出口滥用 | 链路密钥 + 规则/目标授权 + 持续撤销检查 | GOST relay 凭据；FXP PSK；Nginx 未见入口身份鉴权 |

**四、对 nbpass 的建议：管理员自用、Go 面板 + Go 节点、gRPC、仅 WSS 隧道**

以下为设计建议。沿用现有入口组、出口组、转发规则术语，以及已决定的 gRPC 控制链路；参考：[nbpass/GLOSSARY.md](/Users/macmini/Documents/Aixiaoji/nbpass/GLOSSARY.md)、[nbpass/docs/adr/0001-panel-node-grpc-stream.md:1](/Users/macmini/Documents/Aixiaoji/nbpass/docs/adr/0001-panel-node-grpc-stream.md:1)。

**1. 面板与节点控制：优先保证部署、校验和轮换行为明确。**

首版建议网页/API 与 gRPC 使用独立、可配置监听端口，例如 HTTPS 443 和 gRPC TLS 9443；便于分别限流和排错。需要统一公网 443 时再由支持 HTTP/2/gRPC 的代理接入。gRPC 双向流只是通信方式，仍需配置 TLS 和节点身份认证。

证书来源建议比较如下：

| 方案 | 适用情况 | 建议 |
|---|---|---|
| 外部代理/工具申请公共证书，Go 读取文件 | 已有域名和运维工具 | 首版最容易控制复杂度；代理后的明文后端只绑定环回/可信内网 |
| Go 内建 ACME | 希望安装后自动申请与续期 | 后续可用 lego；能开放 80 用 HTTP-01，不能开放或需通配符时用 DNS-01，DNS 凭据尽量只放面板 |
| 私有 CA 或自签加固定信任 | 没有公共域名、完全自用 | 明确分发专用 CA/公钥；需要处理首次信任和证书轮换，不提供默认跳过验证 |

建议节点默认使用系统根与名称校验，证书错误直接失败；私有信任作为显式模式。TLS 配置损坏时保持失败状态，避免自动退回 HTTP。

节点凭据使用每节点独立的 32 字节随机 Bearer token，面板仅存 SHA-256，首次生成只展示一次，节点文件 0600。首版不必照搬额外自写加密信封；已校验的 TLS 加独立节点凭据即可承担基础保护。提供“新旧 token 短期重叠→节点确认新 token→撤销旧 token”的轮换状态，撤销要使已有 gRPC 流重鉴权或主动断开。若之后需要更强节点身份，再增加每节点客户端证书与 mTLS。

**2. 管理员登录：本地账号、可撤销会话、TOTP 与可恢复性一起设计。**

管理员自用可关闭公开注册和第三方登录，首版采用密码 + TOTP；密码用 Argon2id，或使用成熟 bcrypt 实现并按硬件调整成本。提供一次性恢复码，服务端仅存恢复码哈希；TOTP secret 必须可用于验证，宜加密保存，密钥放在数据库之外。另提供仅本机管理员可执行的恢复命令，并明确恢复操作会撤销旧会话。

使用随机、不透明的 cookie session 即可，数据库保存 token 哈希，设置 HttpOnly、Secure、SameSite=Strict、Path=/，同时设空闲期限与绝对期限，例如空闲 12 小时、绝对 7 天。修改密码、重置 MFA、登出均撤销服务端会话。采用同源检查、Fetch Metadata 和 CSRF token，可信代理范围明确配置；密码与 TOTP 共用失败预算，但区分 IP 限流和账号保护，避免轻易造成全局账号锁死。

脚本单独使用有名称、期限、scope、可撤销的 API token，只保存哈希；不要让脚本长期保存浏览器会话或复用节点 token。

**3. WSS 隧道：标准协议、出口身份验证和规则授权同时具备。**

采用成熟 WebSocket 库，使用真正的 RFC6455 握手与数据帧，外面是校验完整的 TLS；不要照搬 FXP V3 的 WS-style 私有裸流。建议默认给出口配置真实域名证书，入口使用系统 CA 与名称验证；没有真实域名时，提供明确的专用 CA 或 SPKI 固定模式，设计新旧信任材料重叠更新，避免靠 `InsecureSkipVerify` 解决部署问题。

将出口域名/SNI、证书来源、Host、Path、可选静态网站回落归入**出口节点的 WSS 配置**；出口组负责成员选择与故障切换；转发规则负责入口监听与允许目标。SNI/Host 默认与证书域名一致，WSS 使用 HTTP/1.1 Upgrade 所需的 ALPN。首版可以不做 uTLS；需要时再加入，不能用指纹伪装代替证书校验。Path 只负责路由，不作为秘密凭据。

为每对入口→出口分配独立隧道凭据，在 TLS 内通过 Upgrade 请求的 Authorization 或后续认证消息完成认证；认证通过后才允许创建业务流。出口按面板下发配置核对入口 ID、规则 ID、配置版本和目标清单，不接受已认证入口任意指定目标。借鉴 A 的持续授权检查，规则禁用、凭据轮换和权限撤销须影响现有连接；控制链路长期失联时使用明确的有限授权期限。

首版可以每个业务连接使用一条 WSS，先控制复杂度；需要降低连接数时，在标准 WSS 字节流上接入成熟 yamux/smux，并明确最大物理连接数、每连接流数、流量窗口、超时和背压。mux 只复用载体，每个逻辑流仍须做规则授权。回落优先返回静态网站或固定可信站点，剥离所有鉴权和隧道头，不把任意目标反代能力暴露给未认证请求。

下一步建议先确定两项部署选择：面板证书由外部工具还是内建 ACME 管理，以及出口默认使用公共证书还是私有信任；其余认证、轮换与规则授权可据此细化。
