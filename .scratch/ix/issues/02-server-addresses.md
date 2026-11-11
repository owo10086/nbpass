# 02 服务器专线地址、对外端口与公网换 IP 隔离

Status: ready-for-agent
Blocked by: 01
Migration: 47（`ixAddressesMigration`）
Parallel: 与 03 串行；04/05 从 03 完成后再分流，避免同时修改快照、端点类型及拨号代码。

## 目标

一台服务器保存公网地址和多条有稳定身份的专线地址；公网地址支持对外端口，端点地址可填 IPv4 或域名。先交付公网地址的域名/端口映射和专线地址管理，下一票把专线地址接入端点组选择。换公网 IP 永不修改专线地址。

## 具体改动点

- 在新文件 `internal/panel/ix_addresses_migration.go` 定义迁移 47，追加到 `migrations.go` 的 `recordTypeMigration` 后：`tunnel_exits` 新增 `public_port INTEGER NULL CHECK(public_port BETWEEN 1 AND 65535)`；新表 `tunnel_addresses(node_id,id,name,address,public_port,position)`，`node_id` 外键引用 `tunnel_exits(node_id)`，主键 `(node_id,id)`，唯一 `(node_id,position)`。`id` 为稳定随机 ID，保留 `public` 作为公网地址标识，不允许专线占用该 ID。公网地址继续存在 `tunnel_exits.address`，监听端口继续是 `listen_port`；不复制为两套公网数据。
- `tunnel_api.go` 的 `GET /api/tunnel-exits` 在原字段上增加 `public_port: number|null`、`dedicated_addresses: [{id,name,address,public_port}]`；`PUT /api/nodes/{id}/tunnel` 仍接受 `address,listen_port,transport`，增加同名两字段。新增专线条目不传 ID，由服务端生成；已有条目必须携带属于该节点的 ID。响应增加保存后的公开配置，绝不返回私钥或节点对凭据。
- 采用整表单更新：`dedicated_addresses` 省略时保留已有专线，显式 `[]` 才清空；`public_port` 省略保留、`null` 清空。名称去空白、1–200 字节；地址规范化，沿用 `forwarding.ValidateTarget(host,true)` 的 IPv4/域名语法与 `AllowedIP` 实际地址限制，拒绝 IPv6、URL、内嵌端口、未指定/组播/广播地址。对外端口为空时取本机监听端口，0 不作为外部 API 的空值。
- `tunnel_snapshots.go` 公网端点的 `TunnelEndpoint.Port` 编译为 `public_port ?? listen_port`；`TunnelServer.ListenPort` 始终用本机 `listen_port`。域名不由面板转换为固定 IP，仍用随机内部 `server_name` 和固定证书校验。
- `internal/forwarding/tunnel_snapshot.go` 允许合法域名；`tunnel.go:openSessionEndpoint` 的 `http.Transport.DialContext` 在前一跳通过 `LookupIPv4` 解析并过滤实际 IPv4，再把具体 IP:端口交给 `DialTunnel`，禁止二次解析、IPv6 或 TLS 身份随域名改变。沿用有界拨号/握手预算、取消与故障归类；探测与业务使用相同解析逻辑，新的会话/探测重新解析域名。
- `exit_blocked.go:rotateExitAddress` 与 `rotation.go` 的自动和手动 changed 收尾，只更新公网 `address,address_version`，保留公网对外端口和所有专线行。`trackExitAddresses` 仍只追踪真正使用该公网版本的拨号者；03 会进一步区分专线引用。公网换 IP 不更新出站 IP（04 的字段）。
- `web/src/Endpoints.tsx`、`EndpointData.ts`、`Endpoints.css`：公网地址明确命名；列出专线名称、地址和有效对外端口；编辑可增删专线，设置各地址对外端口，展示本机监听端口。说明「前一跳连接对外端口，由运营商/NAT 映射到本机监听端口」「域名只解析 IPv4」。轮换、证书、被墙设置继续可用。
- 使用 01 门禁：公网仍为 IPv4 且无覆盖端口时保留旧快照形状；域名或显式覆盖端口要求拨号者具备 `ix_address_dialing`，不得用已解析 IP 绕过能力检查。完成拨号、探测、缓存测试后只宣告此能力；03 才宣告 `ix_member_identity`，本票不承诺多地址会话/健康隔离。
- 为域名/映射修改后的健康证据增加配置绑定：在 `TunnelEndpoint` 和 `ExitHealth` 各追加 `string endpoint_config_digest = 281`，摘要覆盖服务器、public 身份、地址、有效拨号端口、传输与 TLS 身份，节点探测回显同一值；保存/恢复时保留。运行 `make proto` 提交生成物。此时只有公网槽，不实现 03 的 address_id 或多地址客户端键。
- 对外端口只属于前驱拨号参数，不登记为本机监听占用；实际本机隧道端口仍与业务/检测监听冲突校验。外部端口与本机业务端口同号且实际隧道监听不同必须可保存。

## 迁移与兼容要求

- 本票独占 47，不能改动或重排 1–46；从真实结构版本 46 升级，原 address（即使管理员曾手填内网地址）原样作为公网槽保留，不推测并搬入专线，以免改变现有行为。专线初始为空，对外端口初始 NULL，原监听、证书、凭据、规则和流量不变。
- 数据库与完整快照通过 `mutateForwarding` 原子提交。后续 03 增加引用后，删除被组成员引用的专线必须 409，不能默默换成公网；本票的数据接口为该约束保留稳定 ID。
- 拨号地址仍用 `TunnelEndpoint.address`、有效端口用 `.port`；不能把对外端口写入 `.listen_port`。仅新增端点配置摘要字段 281，03 复用它再加地址身份 280；proto 交接顺序为 02→03→04→06a。新健康元数据不改变目标授权；保持 ADR 0006 的高水位、重发和同版本离线恢复。

## 测试要求

- 单元：迁移 46→47/最新与新安装，NULL 默认、外键检查、ID 归属、重复 ID、地址规范化、非法端口与 IPv6；部分字段省略不清空数据。编译验证端点拨号端口与服务端监听端口不同；DNS 仅 A、全部候选非法/解析失败及时失败、证书身份稳定。
- 单元/集成：实际隧道监听与本机业务/检测端口冲突拒绝（含中间节点），外部端口与本机业务端口同号接受；地址/端口修改生成新摘要，旧摘要健康不能确认新端点。
- clusterharness 新建 `ix_addresses_test.go`：A→B 公网域名 + 对外端口映射→回显目标，映射使用真实 TCP 代理转送到 B 的真实 WSS 监听；目标禁止由 A 直拨，验证 B 监听没有被改为映射端口。两条专线持久化后分别驱动手动/自动换公网 IP，API 读取专线、对外端口、证书不变；面板重启仍保留。经旧能力声明的 A 不收到新语义且它的普通直连仍工作。
- 与 03 共用「02 版节点 × 03 版面板」兼容基线：保存本票产物/真实能力声明用于后续测试；纯公网域名/映射只要求 `ix_address_dialing`，不能因缺 `ix_member_identity` 整条阻塞，专线/多地址需求则精确拦截。收包断言不向该节点下发 address_id 新语义。
- 运行 `go test ./internal/panel ./internal/forwarding ./internal/node -timeout=10m` 与 `go test ./internal/clusterharness -run 'Test.*(IXAddress|ExitBlocked|TunnelChanges)' -timeout=15m`；前端地址表单数据测试和构建。

## 验收标准

- [ ] 公网与多条专线可保存、回读、编辑并在页面完整显示。
- [ ] 域名在拨号节点解析且只使用 IPv4；WSS 证书仍按内部身份校验。
- [ ] 对外端口覆盖仅影响拨号，未填保持现有监听端口行为。
- [ ] 手动与自动换 IP 均不覆盖专线；旧节点获得逐端口升级提示。
- [ ] 只宣告拨号能力，健康摘要随配置改变；外部端口同号可用、实际监听冲突拒绝。

## 不做的事

不让专线地址脱离服务器独立存在；不实现端点组地址选择（03）、NAT 配置工具、监听绑定 IP、IPv6、UDP、管理连接经中转；不提交构建产物。
