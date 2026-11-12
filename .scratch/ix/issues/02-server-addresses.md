# 02 服务器专线地址、对外端口与公网换 IP 隔离

Status: resolved
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

- [x] 公网与多条专线可保存、回读、编辑并在页面完整显示。
- [x] 域名在拨号节点解析且只使用 IPv4；WSS 证书仍按内部身份校验。
- [x] 对外端口覆盖仅影响拨号，未填保持现有监听端口行为。
- [x] 手动与自动换 IP 均不覆盖专线；旧节点获得逐端口升级提示。
- [x] 只宣告拨号能力，健康摘要随配置改变；外部端口同号可用、实际监听冲突拒绝。

## 不做的事

不让专线地址脱离服务器独立存在；不实现端点组地址选择（03）、NAT 配置工具、监听绑定 IP、IPv6、UDP、管理连接经中转；不提交构建产物。

## Comments

### 主编排者已有局部页面核验（续接 1076）

- 主编排者在 `91ead18` 临时副本的 818px 视口核验了端点编辑：数据回填；清空公网对外端口后列表使用本机监听端口；删除首条专线后剩余值正确；清空全部专线后保存成功；SNI 保持、PEM 可展开。临时 QA 服务已停止、标签页已关闭，未操作生产。
- 这不是完整人工验收：1366–1440px、更窄屏、切换服务器、全部证书/轮换等尚未检查，仍按本票原清单交主编排者验收。

### 交付内容与兼容取舍（2026-11-12）

- 迁移 47 仅追加在 `recordTypeMigration`（46）之后。`tunnel_exits.public_port` 初始 NULL；新增 `tunnel_addresses`，以服务器内稳定随机 ID 和顺序保存多条专线，数据库约束保留 `public`、外键、ID/位置唯一性和端口范围。真实 46 结构升级保留原公网槽（含原手填内网地址）、监听、身份、私钥与地址版本；新安装从同一迁移链建表。
- 地址 API 支持规范化 IPv4/域名、公网/专线对外端口、专线稳定 ID 归属检查及整表单保存。省略 `public_port`/`dedicated_addresses` 保留，`null` 清除公网端口，`[]` 清空专线；`dedicated_addresses:null` 保守拒绝，避免误清空。PUT 保留旧 `configured:true`，平铺返回保存后的公开配置，不返回私钥或节点对凭据。数据库与完整快照沿用 `mutateForwarding` 原子提交。
- 前驱按 `LookupIPv4` 解析和过滤实际 IPv4，再将具体 IP:对外端口交给 `DialTunnel`；业务与探测共用逻辑，新会话/新探测重新解析。只尝试最多三个不同合法 IPv4，单候选最多 3 秒，沿用已有建流/握手总预算、取消和故障分类。不进行面板侧固定解析；TLS 继续使用随机内部 `server_name` 和固定证书校验。服务端始终监听本机 `listen_port`。
- 复用票 01 的单一能力计划：域名或显式公网对外端口（即使等于监听端口）只要求前驱 `ix_address_dialing`。过滤同时覆盖入口规则/候选和远端 grants/peers；旧声明同节点的普通直连仍正常。只登记未选用的专线不触发门禁；本票只新增宣告拨号能力。
- 按票据「普通 IPv4 且无覆盖端口保留旧快照形状」保留该路径空摘要；新域名/映射语义携带 281 摘要。摘要覆盖服务器、`public` 身份、地址、有效端口、传输和稳定 TLS 名称；证书材料/凭据轮换仍由原独立确认流程管理，不改变稳定身份。节点回显同一摘要，实际缓存/恢复保留它；面板在健康保存、地址握手确认、入口就绪及健康读取时核对当前期望，拒绝旧/空摘要冒认新的域名或映射配置。未改变授权代次、高水位或离线授权期限。
- 手动与自动 changed 本来就汇入 `rotateExitAddress`；核对后保留其只更新 `address,address_version` 的 SQL，无需另改轮换协调流程。真实集成分别验证两种换公网 IP 后，两个专线及稳定 ID、全部对外端口、本机监听与证书身份均不变，面板重启后仍保留。`trackExitAddresses` 只登记实际下发公网版本的拨号者；无出站 IP 字段改动。
- 对外端口未加入本机监听占用。API 集成验证外部端口与业务端口同号可保存，实际隧道监听与业务/检测端口冲突拒绝且回滚。中间节点/多跳尚未进入本票模型，其实际监听场景交 06c，现有所有服务器的监听冲突校验保留。
- 前端按 DESIGN 使用现有表格/Modal/FormSection：明确公网地址、列出专线名称和有效对外端口、本机监听，支持增删及稳定 ID 回填；保留证书、轮换、被墙与健康入口，说明 NAT 映射和仅 IPv4。未修改 spec/ADR 已定决策。

### 已执行的测试与审查

- 按接缝执行红→绿：46 升级、API 新字段/省略保留、能力过滤/拨号与监听分离、域名真实业务/探测/缓存、解析失败和取消、旧摘要不能确认/显示新配置、表单数据。`make proto` 已执行，提交字段 281 的生成物；本票没有字段 280。
- `go test ./internal/panel ./internal/forwarding ./internal/node -timeout=10m` 最终通过，墙钟 **53.51 秒**（panel 52.502 秒，另两包命中有效缓存）。
- `go test ./internal/clusterharness -run 'Test.*(IXAddress|ExitBlocked|TunnelChanges)' -timeout=15m` 通过，墙钟 **40.20 秒**，包内 **38.383 秒**。新场景用真实 TCP 代理把公网对外端口转到 B 的真实 WSS 监听，A 禁止直拨隧道目标；同版本离线重启后仍转发。旧声明重连后收包断言不含新端点/孤立授权，普通直连仍传输。
- `npm --prefix web run build` 最终通过，墙钟 **2.67 秒**；显式前端测试命令 `node --experimental-strip-types --test web/route.test.mjs web/server-groups.test.mjs web/src/*.test.mjs` **28/28**，墙钟 **0.11 秒**。保留已有 Vite 大 chunk 和 Node 颜色环境警告。
- requesting-code-review 只读独立审查未发现 Critical/Important/Minor 问题；`git diff --check` 通过。
- 首轮全量 `GOFLAGS=-timeout=30m make test` 墙钟 **436.14 秒**、clusterharness **412.837 秒**，两项失败：新增映射场景在离线恢复/重连后过早读取监听或收包（已按真实异步启动/控制重连等待修正，安全断言未削弱）；既有 `TestAllPublishSetWatchAndPerRecordStatusAudit` 报 DNS 写回结果未确认，原测试单独复测通过（包内 2.187 秒、墙钟 4.10 秒），未修改该无关功能。最终代码完整重跑退出码 **0**：墙钟 **392.46 秒**，clusterharness **379.855 秒**，panel **52.249 秒**；包含前端构建、节点临时构建、全仓库 `go vet` 与 `go test ./...`，没有跳过测试。

### 后续票与浏览器交接

- 03 继续实现端点组地址选择、成员地址身份以及同服务器多地址会话/健康隔离，并为被引用专线删除返回 409。本票专线接口预留稳定 ID，但没有提前实现专线拨号或宣告 `ix_member_identity`。
- 03 必须运行真实「02 节点 × 03 面板」矩阵：纯公网域名/映射只需拨号能力；专线/多地址精确要求成员身份能力；备用升级重新持久应用确认再纳入。不得用新版节点仅删声明代替真实版本组合。02 固定源码/产物和声明见下节；01 的真实旧缓存回滚基线仍原样交接给 04/06/09。
- 浏览器验收交编排者：在 1366–1440px 检查主列表全部列和长域名/专线名称换行，窄屏横向滚动/弹窗；新增、回读、编辑两条专线、删除中间项后保留剩余 ID/端口、清空全部再保存；公网/专线空端口显示本机监听且清空覆盖可回读；切换服务器时表单不残留上一个服务器的专线。核对 NAT/仅 IPv4 文案、证书查看/复制、轮换/被墙操作继续可用，旧能力逐端口升级提示及健康修改后待新证据。


### 真实 02 节点兼容基线

- 本票完整代码基线：`946783879ab679baba2299704e75fcf398a86d7e`（节点实现提交 `398a5eb`，面板/集成实现 `447b718`，界面 `9467838`）。源码从该提交重建，不依赖软件版本字符串判断能力；后续文档提交不改变代码。
- 已保留本地实际节点产物（均在被忽略的 `bin/`，不提交、不发布）：`bin/nbpass-node-ix02-linux-amd64`，SHA256 **`ef3ad98e58b6ed50b048f5604e7ad351e8b6f2725e4e1babfa4f41f353551b81`**；`bin/nbpass-node-ix02`（本机 darwin/arm64 可运行），SHA256 **`bed243861b9d6a25657cbf1a016a7c85ed29cc334491464c5f76cacc1cb03058`**。Linux 构建为 `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -buildvcs=false -ldflags='-s -w'`，工具链 `go1.26.3`；本机使用相同构建 flags。二进制关闭 VCS 注入，以上源码提交与哈希共同定位来源；03 勿覆盖这两个带 `ix02` 名称的产物。
- 实际执行 `bin/nbpass-node-ix02 --update-check` 返回（真实程序输出，非手改声明）：

```json
{"compatibility":{"control_min":1,"control_max":2,"tunnel_min":1,"tunnel_max":1,"state_min":1,"state_max":2,"state_write":2,"capabilities":["upgrade","tunnel_authorization","exit_failover","tunnel_rotation","rotation_recheck","dns_watch","ix_address_dialing"]},"version":"0.1.0","platform":"darwin/arm64"}
```

- 03 的真实版本矩阵使用上述二进制，或使用固定提交真实源码构建对应平台节点。纯公网域名/映射无需 `ix_member_identity`，专线/多地址须拦截并给出准确 blocker；收包断言不能给 02 节点增加地址身份字段 280 的新语义。当前 clusterharness 的删声明场景只证明本票旧能力门禁，不声称已经完成尚未存在的 03 面板矩阵。
- 已还原 `web/dist` 与 `internal/nodebundle/nbpass-node-linux-amd64.gz` 并移除本轮额外的 dist 文件；proto 生成物随代码提交。只在指定目录与 `feat/ix` 操作，未切分支、未建工作树、未推送。票据按要求标记 `resolved`；剩余浏览器验收及 03/06c/09 交接见上文。

### 主编排者最终补验（2026-11-12）

- 使用本次正式 make build 的本机面板、独立临时测试库、两个未接入虚拟服务器与独立无头 Chrome profile；没有操作生产数据。1440×1000 列表实际检查长公网域名、长专线名称/域名及两条专线换行，无列重叠；390×844 编辑窗能纵向滚动、保留两条专线 ID/端口并实际保存原配置，完整回读仍为两条。
- 「添加转发端点」中选已有两专线服务器再切无专线服务器，真实 UI 从专线行数2变0、公网地址回填第二服务器、空覆盖端口保持为空，无上一服务器残留。
- 实际点击证书身份复制，浏览器剪贴板内容与该端点稳定 *.nbpass.internal SNI 一致；实际展开 PEM 内容。轮换入口显示真实「尚未轮换」/相关入口0/0；被墙入口显示真实配置和地址同步进度。未发起完整轮换或换IP，不声称完成其业务闭环或 PEM 剪贴板逐字节核对。
- 实际撤销会话（logout204），点击端点刷新，真实401返回登录页。临时服务和 Chrome 已停止。真实双地址健康/新鲜证据及完整轮换操作仍以自动化证据和原未验边界为准。
