# 06b Relay 数据面、逐边重试与缓存恢复

Status: resolved
Blocked by: 06a（缓存按 spec 已定决策 7：A 实施）
Migration: 无（消费 06a 的 51，不另占版本）
Parallel: 06a 后串行接管 forwarding/node 数据面与缓存；可与 07 CRUD 前端并行，不与 06c 同改授权/健康运行文件。proto 新合约由 06a 先落定，不争抢生成物。

## 目标与交接边界

用 06a 合约实现真实入口→第 1 组→…→末跳→目标，仅逐跳转发与末跳拨目标；无入口监听的 relay 能离线重启，保留下一组、grant、源 IP 及撤销限制。完成真实节点组件/数据面测试后交 06c 做面板确认、健康和事故端到端集成；本票不宣告 forward_chains。

## 具体改动点

- `forwarding/tunnel.go` 的逐流请求携带 chain_id/hop_index、规则代次和剩余预算；`tunnel_authorization.go` 验证会话的真实直接前驱、规则 ID/代次、链/跳、授权目标。下一组只能来自本地 grant，不信任入口提交的自由地址链；错链、错跳、旧代次、伪造前驱/下一组/目标一律拒绝。
- `tunnel_limits.go:serveLimitedStream` 分离转交下一组与拨最终目标。中间跳只建下一组连接，末跳才解析最终目标并调用 04 的 `dialTargetWithin` 源绑定。A/B 禁止直拨目标仍须多跳成功；隧道拨号/探测不绑定出站 IP。
- 保留资源限额、有界预读/握手、取消、半关闭、FIN/RST 回收与每秒复查授权。上游断开/撤权取消下游拨号和流；路径修改、停用、删除切断在线节点旧授权流；控制离线本身不设置到期。
- `engine.go/state.go/tunnel_snapshot.go` 保留 relay-only 使用的 ExitGroups、客户端、TunnelServer grants、端点摘要、源 IP 与逐 grant 本地结果。当前 State 只从入口监听规则收集组，必须改为包括 relay 实际所需组；不能在 Restore 后把中间跳 grant 恢复成末跳直拨。空入口 Rules 仍能恢复 relay。
- 缓存顺序保持高水位/摘要、先原子持久化撤销检查点、再应用、再报告；部分失败/写盘失败不得确认，重启不得恢复被撤配置或无绑定旧源配置。04 的源地址枚举在应用和恢复时复查，非本机地址产生可定位的配置错误。

## 下游失败、健康与重试

- 按 06a 结果合约回传失败：本机→下一成员不可达为该实际边失败；接到下游的失败向更上游标为 downstream_next_hop，末跳→目标失败为 exit_target，均保留真实边、地址、跳和 grant/链指纹。资源/配置/授权错误不伪装成链路不可达。
- 建流尚未成功且未转发业务字节时，遇到下一跳或下游/目标不可达，上游按组内原主备顺序尝试同组下一个健康、已确认且获授权的成员；每组每轮最多 3 个不同成员。下游失败不能让本边健康变坏，不把 B1→C 错算成 A→B1；真实本边链路失败才进入本边失败计数。资源/配置/授权错误保留原因并停止本次重试。
- 共用入口计算的 `min(调用方剩余期限,10 秒,入口 DialTimeout)` 总预算；逐跳传剩余毫秒，扣除本地解析/握手/重试耗时，接收端本机限额只可缩短。不得每个 relay 或每次备用尝试重获完整 10 秒。超时取消全部未完成拨号，不回放建流成功后的流量，不跳组、不回退直连。
- `exit_failover.go/exit_health.go` 只维护本机发起的下一边健康，按 03 的地址/摘要隔离；保留约 5 秒探测、连续 3 次链路探测失败、连续成功 60 秒恢复及现有真实链路失败处理。目标/下游/本地/配置失败不污染本边。下游可用性由单独的授权/路径就绪证据表达，不用本边健康代替。
- 探测不拨最终目标；整链建流错误和本边握手健康可同时保留。`traffic.go` 规则累计只收实际入口，relay 不重复计账；每节点资源连接数按本机真实承载统计。

## 缓存回滚 A 的落地与公共验收

复用 01 的 `StateCurrent=2`、`state_min=1,state_max=2,state_write=2`，按 06a 契约完整实现 relay State/Restore，不另升格式。所有状态、撤销检查点统一写现有 `config.enc` 的 JSON 外层整数 `version: 2`，保留同文件原子写与 AEAD 附加数据，不留格式 1 回退副本。真实旧 `config_store.go` 严格比较 `Version == StateCurrent(1)`，必在 Restore 前报错；旧 `node.go` 返回并关闭 engine，尚不能建立控制会话。因此恢复须按 spec 人工停服务、隔离不兼容缓存、空状态启动，成功重连获得能力内配置后才转发，不能假设旧版自动重连。在线门禁、未知字段忽略、Restore 重算摘要均不能代替旧代码的严格拒绝证明。

必须完成：新版真实持久化 relay-only、多跳末跳源 IP 和撤销检查点后，断开面板，用 01 固定来源的真实旧读取代码/旧二进制加载；观测明确拒绝、进程退出、无业务监听及无下一跳/目标连接，无越跳直拨、无源字段丢失后转发、无授权复活。隔离缓存后旧版离线空启动也不转发；重连只接收能力内当前快照。新版读格式 1 时保留旧高水位、实际应用和撤销边界并持久化为 2，同版本离线重启仍按 ADR 0006 转发；写盘失败/普通损坏不得自动删除或隔离缓存绕过。实际回滚二进制没有明确拒绝保证即阻断发布，不改选 B；09 负责完整一次发布的重新安装与人工回滚演练，不引入生产过渡版。

## 测试要求

- 单元/真实节点：三跳及多一跳、无入口监听 relay 的 State/Restore、错链/跳/前驱/目标/代次拒绝、授权复查与撤销取消、资源满/黑洞/RST/半关闭/慢读写、源绑定与流量不重复。
- 非对称可达：A→B1 好、B1→C 坏、B2→C 好，A 在总预算内换 B2，成功传字节；B1 的失败保留为 B1→C，A→B1 健康不判坏。相同用例覆盖末跳→目标失败后同组备用成功，及全部候选失败不跳组。
- 预算：连续多跳黑洞/握手慢/多层备用，测量整体不超过入口剩余预算（允许明确的调度容差），每层预算单调递减、候选不重复且最多 3 个；取消后无残留连接/协程；建流成功后断流不重放。
- 缓存：执行上一节真实旧读取器公共矩阵；relay 控制离线仍转发、离线重启路径完整；末跳非本机源 IP/磁盘失败不得放行，撤销检查点不能恢复旧 grant。
- 数据面可先用真实节点组件注入经校验快照运行 WSS/yamux/TCP；不 mock 转发结果。面板 HTTP + 自动确认的完整 clusterharness 场景由 06c 接通后统一跑，不能将仅模型持久化标为已跑通多跳。
- 运行 `go test ./internal/forwarding ./internal/node -timeout=15m`；relay/撤销/轮换/恢复相关用例运行 `go test -race`。交接可复用的故障/预算用例给 06c，不生成额外报告文件。

## 验收标准

- [x] 真实逐跳转发、末跳独占目标解析/源绑定，无入口监听 relay 可持久恢复，不越跳。
- [x] 非对称下游失败能尝试同组备用，本边健康保持真实归属，共享端到端预算。
- [x] A 的格式 2 持久化/恢复完整，真实旧读取器拒绝/停转发、人工空启动/重连过滤矩阵通过，不靠在线门禁证明离线安全。
- [x] 撤销、资源、流量、半关闭与取消回归通过，未提前宣告 forward_chains，交接 06c 集成。

## 不做的事

不做面板确认/operations/incidents 或 UI，不绕过必经组、不引入全局智能路由，不改变 A 或另升缓存格式，不提交构建产物。

## Comments

### 实施边界与数据面（2026-11-12）

- 基于 `feat/ix` / `27ea261d1a5465ed872c13420f7fd5689f36a3c5`；本票唯一实施者。完整实现后仍保持 `claimed`、无功能提交，等待主编排者独立审查；既有 02/03/04/05/06a/09 六份未提交文档及两份真实 02 二进制未改。未建工作树、切分支、push/deploy/merge、操作浏览器或创建辅助方案／审查报告／日志。
- 接收流通过已认证 WSS 会话的直接前驱、rule/generation/chain/hop/fingerprint/目标匹配本地 grant。下一组和可用成员只取 `NextExitGroupId` / `AuthorizedNextNodeIds`，零候选不会回退直连；新增 grant 校验拒绝缺组、越权成员、错 hop/代次，以及将仍有后续可信边的 relay 裁成末跳。逐流 JSON 对未知字段严格拒绝，不能用自由 `next_exit_group_id` 等字段让接收端静默接受；链式 Check 外层与授权投影须一致。
- 中间跳经本地下一组客户端建立真实 WSS/yamux；最终目标 DNS 与 04 的 `dialTargetWithin` / 源绑定只在末跳执行。非本机源 IP 只令最终目标消费者失败，入口和中间 relay 的传输／健康探测继续走无源绑定的独立拨号路径。预检改传最小 ForwardRule 授权投影，保留旧单跳 Check 兼容，不将完整可信拓扑塞进 4 KiB 逐流帧；超过 4 KiB 的合法可信边夹具已真实传字节。
- 复用逐节点／逐规则／待拨号资源额度、有界预读与握手、FIN/RST 和慢读写回收。已认证会话资源满返回 `local_resource`，不再以无原因 Reset 掩盖资源错误；拒绝不新增排队 goroutine。relay 仅统计本节点承载连接，规则字节只在入口计量。
- 每秒复查 grant 与凭据撤销 epoch；捕获本流实际选中的后继，除目标、代次外还核对本地下一组和该后继许可。同 generation 仅撤 `AuthorizedNextNodeIds` 也取消已建立流及下游拨号，能拦住离线后继；入口对同代次仅撤 `AuthorizedExitIds` 同样复查。路径变化、停删／撤 grant 和普通凭据替换沿既有取消链收敛；控制离线没有新到期时间，显式轮换保留已有流语义。

### 失败归属、重试与预算

- 使用 06a `ChainFailure` 与 **`ValidateChainFailureFromNext`**，验证本轮实际选中的 endpoint、可达后缀、地址摘要、hop、generation、fingerprint；不能用整链成员并集替代。远端不能声明本机的 `entry_exit`；下游传回配置／资源拒绝缺边时，只能由已认证的实际后继身份合成本地归属，不能携带任意节点归属。B1→C 失败上传为 `downstream_next_hop` 并保留真实边，末跳失败保持 `exit_target`。
- 每组按原序选健康且获授权的不同成员，最多三个，建流成功前才能重试；目标／下游失败不污染本边，资源／配置／授权错误保留分类并停止。已交付业务的 RST 不重放，不跳组。原约 5 秒探测、连续三败／60 秒成功恢复逻辑保持，探测仍不拨最终目标。
- 入口和每层接收共用父 context 剩余期限、10 秒和本机 DialTimeout 的最小值；逐流发送时重新计算剩余毫秒，接收扣本地读取／授权耗时，解析／握手／备用共享同一截止点。自查红测发现 `http.Transport` 的拨号 context 会去掉 Deadline：原取消链虽会终止，但 DialTunnel 子 context 显示重新得到 3 秒；已将原握手 Deadline 显式带入，保留取消桥接，实测各层预算单调递减。
- 320 ms 入口限额用例实际 **320.074333 ms**，观察到三层／备用拨号预算 **318.880958 → 206.483251 → 130.916125 ms**；500 ms 的两层主备嵌套用例实际 **180.032709 ms** 成功传字节，最后一层剩余 **364.7055 ms**。预算断言分别允许 230／150 ms 调度容差；每组候选去重及三成员封顶另有真实连接故障用例。取消后同时核对业务连接和资源 pending/connections 归零。

### 缓存与 06c 接缝

- `State.ExitGroups` 现在收集真实 relay grants 所需的下一组，即使 `Rules` 为空也保存完整组、端点材料／摘要、grant、源 IP 和高水位。Restore 校验下一组关系，重建客户端／监听，不将 relay 还原为最终目标直拨；源地址在 Restore 重新枚举。保持原先先持久化撤销检查点、再应用、再持久实际状态／报告的顺序，最终写盘失败不能恢复旧 grant 或无绑定旧源配置。
- `State.TunnelGrantResults` 与 `Engine.TunnelGrantResults()` 提供 `(entry_id, rule_id, generation, applied_revision, apply_error)`。它表示**本地安装／缓存持久化结果**，不等于后续整链就绪。结果以最终持久成功为发布边界；写盘失败全部撤回该次成功，监听失败等逐 grant 错误可跨重启保存；恢复拒绝重复／超高水位／混代结果，链实际 grant 必须有对应缓存结果。getter 返回克隆并加锁，可与 Apply 并发读取；06c 负责接入 NodeReport、当前认证会话及 revision/digest 的完整名单验证／逐层确认，本票没有提前实现面板确认或宣告 `forward_chains`。
- `Engine.ChainFailures()` 返回独立于 `ExitHealth()` 的 `ChainFailureObservation`（RuleID、ChainID、Class、实际 Edge、ObservedAt），按本地规则保留最近建流失败；新 generation/fingerprint 或删除会清理，迟到旧代次不复活。此接口提供历史失败，不把后来的本边握手或另一备用成功伪装为这条失败边恢复；06c 必须结合当前路径／grant／新鲜证据判断就绪和恢复，不能让历史失败永久压住路径，也不能以本边健康覆盖整链错误。
- 可复用真实组件夹具在 `internal/forwarding/chain_relay_test.go`：`newRelayFixture`／`addRelaySibling`，含入口→B→C→目标及再多一跳、relay 离线恢复、非对称与目标失败同组备用、嵌套备用／总预算、同代次撤后继／停删、资源满、RST、不重放、300 KB 慢 IO／FIN、入口计量、源绑定／源失效、真实授权帧拒绝、拓扑大帧投影、故障观察代次和并发 grant 结果。只有物理故障／时间接缝注入，成功路径使用真实 WSS/yamux/TCP 字节和目标观察；HTTP 全链自动确认与 operations/incidents 仍交 06c。

### 真实旧读取器／旧程序公共矩阵

- 保持 `StateCurrent=2`、读 1/2、只写现有同一 `config.enc` 的 JSON 外层 `version: 2`，AEAD 附加数据不变，不双写、不另升缓存或迁移号、不自动删除损坏缓存。
- 扩展 `TestActualV1ReaderRejectsNewlyPersistedCache`：**直连／单跳出口／链末跳源 IP／relay-only × 最终实际缓存／撤销检查点 = 八格**。新版真实 Engine.Apply→Persist→FileStore 加密写盘，再由当前真实 openConfigCache 恢复核对；relay 包含空 Rules、完整下一组和逐 grant 结果。随后固定真实旧源读取器／旧 Run 及完整旧可执行程序均读取这些实际缓存。
- 旧源固定 **`3899928362a75b58ac74ff6844652d319760b656`**；从完整原始源码在测试临时目录构建，实际程序 SHA256 **`32054cfe3cfe3c040ded228e1052c6f145d70eb847714f1d60540e9968c18acd`**。八格旧程序均 **exit status 1 / 节点配置缓存格式或身份不匹配**；原旧读取器在 Restore 前拒绝，旧 Run 在控制拨号前返回错误。无业务／隧道监听，下一跳／目标观察连接 **0**；原样隔离后旧 Run 离线空启动继续无业务，没有 fallback 缓存。
- 复用并扩展 04 的真实旧程序人工隔离／重连测试：控制 TCP 代理离线时空进程不转发，隔离文件逐字节保留；重新连真实面板后，源规则和旧节点处于**链入口／中间／末跳**三位置的规则／grant 都被能力过滤。显式清除源后的当前普通直连可真实交换字节，最新快照无链规则／relay grants／ExitGroups。只证明测试固定历史程序，不冒称 09 的生产实际回滚目标核验或人工发布演练。

### 红绿与验证记录

包耗时为 Go 实际输出；墙钟由 `/usr/bin/time -p` 记录。夹具初稿的不存在 `TunnelGrant.ChainFingerprint` 字段及 `time.Timer.C()` 拼写在有效红测前分别修正，不作为功能红证据。

| 命令 | 退出码；包耗时／墙钟 | 证据 |
|---|---|---|
| `go test ./internal/forwarding -run 'TestChain(RelayReal\|RejectsFree)' -count=1 -timeout=2m`（红） | 1；1.233／1.89 秒 | 链预检因未传预算被拒；未知自由下一组字段被静默接受。 |
| 同命令（基础实现后） | 0；3.268／3.93 秒 | 真实两级／三级 relay 传字节和无入口 Rules 离线恢复、自由下一组拒绝。 |
| `go test ./internal/forwarding -run 'TestChain(EntrySame\|RelayChanged)' -count=1 -timeout=2m`（红） | 1；2.809／3.50 秒 | 入口同代次后继撤权未取消已有流；relay 检查点场景通过。 |
| `go test ./internal/forwarding -run 'TestChain' -count=1 -timeout=2m`（修复后） | 0；2.961／3.65 秒 | 入／中间两侧撤权复查及基础故障路径通过。 |
| `go test ./internal/forwarding -run 'TestChain' -count=1 -timeout=3m`（资源红） | 1；3.859／4.58 秒 | 满载仅 Reset 导致资源拒绝丢成 configuration_authorization。 |
| `go test ./internal/forwarding -run 'TestChainRelayResource' -count=3 -timeout=2m` | 0；0.669／1.39 秒 | 显式资源拒绝保持分类、不重试备用且不污染健康。 |
| `go test -race ./internal/forwarding -run 'TestChain' -count=1 -timeout=3m`（Deadline 红） | 1；4.177／5.40 秒 | 下游观测到 2.999994541／2.999995541 秒，未体现入口 320 ms 剩余期限。 |
| `go test ./internal/forwarding -run TestChainGrantResultsNotConfirmed -count=1 -timeout=2m`（红） | 1；0.626／1.33 秒 | 最终持久化回调内已能读到新 grant 成功。 |
| `go test -race ./internal/forwarding -run 'TestChain' -count=1 -timeout=3m`（两项修复后） | 0；5.026／6.27 秒 | 显式 Deadline、持久后发布 grant 结果及链矩阵通过。 |
| `go test ./internal/forwarding -run TestChainGrantBindFailure -count=1 -timeout=2m`（红） | 1；1.252／1.95 秒 | 无实际监听的失败 grant 结果在 Restore 被丢弃；已保留失败结果并纳入下行 race。 |
| `go test ./internal/node -run TestActualV1Reader -count=1 -timeout=4m -v` | 0；13.265／14.10 秒 | 八格新版实际缓存被完整旧程序／真实旧 Run 拒绝，人工空启动仍无业务；随后增加新版加密 Restore 核对，纳入最终两包与 race。 |
| `go test ./internal/forwarding -run 'TestChainRelay(Nested\|Asymmetric\|TargetFailure\|AtMost\|Budget)' -count=1 -timeout=2m -v` | 0；1.847／2.57 秒 | 非对称、目标备用、每组最多三、递减预算及两层嵌套备用真实传字节。 |
| `go test ./internal/clusterharness -run TestOutboundIPv4ActualV1ManualIsolationAndReconnect -count=1 -timeout=5m -v`（扩展三位置后） | 0；3.404／5.61 秒 | 真实旧进程拒绝／离线空启动／重连三位置链与源门禁，当前兼容直连正常。 |
| `go test ./internal/forwarding ./internal/node -timeout=15m`（最终完整包） | 0；forwarding **14.364**、node **20.085**／**20.83 秒** | 本票全部用例及既有两包回归通过；含最新加密恢复／旧程序八格和失败 grant 持久边界。 |
| `git diff --check` | 0；墙钟 **0.02 秒** | 空白与补丁格式检查通过。 |

- 初轮完整两包还通过过 12.436／14.518 秒（墙钟 15.38），较后完整两包通过 15.048／20.218 秒（墙钟 21.17）；最终以上表为准。没有运行全仓 make test，09 统一最终全量；没有改 proto/生成物、迁移、兼容能力声明或构建产物。

### 冻结待审查

- 最新定向 race：`go test -race ./internal/forwarding ./internal/node -run 'Test(Chain|CachedTunnel|Tunnel.*(Rotation|Authorization|Cancel|Reset|Capacity|HalfClose)|OutboundIPv4.*Restore|ConfigCache|ActualV1Reader)' -count=1 -timeout=5m`，退出 **0**；forwarding **6.116 秒**、node **14.171 秒**，墙钟 **15.78 秒**。覆盖最终嵌套备用、grant 并发读取／持久化边界、relay 恢复、源重核验、旧读取器／完整旧程序八格、撤销／轮换和资源回收。
- 最终 `git diff --check` 退出 **0**；`git status --short web/dist internal/nodebundle bin` 无输出。代码冻结，所有本票自动化验收已执行，仍保持 `claimed` 并保留验收框待独立审查确认；没有提前提交功能或 `.md`。06c 的面板自动确认／整链 HTTP／事故／能力宣告和 09 的真实生产发布演练没有在本票冒称完成。

### 独立审查修复轮 1（1/5，2026-11-12）

- 原审查提出 **Important 2 / Minor 1**，本轮集中处理，未提前提交。代码改动仅 `exit_failover.go`、`tunnel_limits.go`、`chain_relay.go`、`state.go`；测试增补 `chain_relay_test.go` 与既有 `tunnel_rotation_test.go`，证据只写本票。
- **Important 1 — ADDRESSED**：首次 B1→C 失败将本边置为 unhealthy 后，后续建流跳过 C 原先落入默认 configuration_authorization，导致 A 无法再试 B2。现在明确区分没有获授权成员（配置拒绝）和获授权成员因 unhealthy／recovering 被过滤（保持该实际 endpoint 的链路失败与 ChainFailure）；上游继续把它作为 downstream_next_hop 试同组备用，不会污染 A→B1。`TestChainRelayAsymmetricFailover` 扩展为连续三次真实建流：第一次实际拨号失败、第二次健康过滤、第三次已成功探测但尚处恢复等待期，三次均由 B2 传字节并核对真实失败边／入口健康。
- **Important 2 — ADDRESSED**：入口已建立流现在保存本次实际选中的 `tunnelClient` 身份，并复查它是否仍在当前获授权选择中。`applyTunnelClients` 只在相同路线或**每次显式轮换过渡**中保留同一实例，因此最终 RotationId 清空后仍能证明完整轮换连续性；普通凭据／证书／路线替换照旧关闭并替换实例，不能因仅比较节点 ID 而保留旧流。没有放宽 `sameTunnelRoute` 的普通材料变更规则。
- 新增 `TestTunnelRotationFinalSnapshotPreservesRecheckedEntryStream` 覆盖普通单跳与链式入口：真实 WSS/TCP 建流后依次 Apply prepare／switch／最终空 RotationId 快照，核对最终新凭据已安装，再触发入口复查；测试等待复查完成后重新注册的定时器，避免“推进时钟但 goroutine 未跑”的假绿。轮换前旧流及新流都继续传字节；随后仅入口普通凭据替换（下游仍旧状态）必须切断已有载体，保留普通撤权机制。
- **Minor — ADDRESSED**：Restore 对同 `(entry_id, rule_id)` 的**实际 grant**与缓存结果始终要求 generation 精确一致，不论结果成功还是 `AppliedRevision=0` 的失败。只有没有实际 grant 的独立失败结果可保留用于错误显示，不能拿旧代次失败充当当前 grant 的结果后重算成功。新增 `TestChainRestoreRejectsStaleFailedGrantResult`；既有独立监听失败结果恢复仍通过。

| 命令 | 退出码；包耗时／墙钟 | 结果 |
|---|---|---|
| `go test ./internal/forwarding -run 'Test(ChainRelayAsymmetric\|ChainRestoreRejectsStale\|TunnelRotationFinalSnapshot)' -count=1 -timeout=2m`（三项有效红） | **1**；3.296／4.02 秒 | 第二次非对称建流 EOF；过期失败结果被接受；最终轮换复查在单跳、链式两格均误断旧流。 |
| 同命令（修复后） | **0**；0.763／1.49 秒 | 三项集中修复通过；随后普通凭据替换断流断言加入完整回归。 |
| `go test ./internal/forwarding ./internal/node -timeout=15m` | **0**；forwarding **15.304**、node **21.601**／**22.56 秒** | 修复后完整两包通过，含普通凭据替换、独立失败结果、恢复及真实旧版八格。 |
| `go test -race ./internal/forwarding ./internal/node -run 'Test(Chain\|CachedTunnel\|Tunnel.*(Rotation\|Authorization\|Cancel\|Reset\|Capacity\|HalfClose)\|OutboundIPv4.*Restore\|ConfigCache\|ActualV1Reader)' -count=1 -timeout=5m` | **0**；forwarding **6.152**、node **13.505**／**15.17 秒** | relay／轮换／撤销／恢复及旧版矩阵定向 race 通过。 |
| `git diff --check` | **0**；墙钟 **0.02 秒** | 修复轮补丁检查通过；dist/nodebundle/bin 状态仍为空。 |

- 修复轮 1 代码与以上证据已冻结，等待原审查者 scoped 复审；Status 继续 `claimed`，无任何提交。没有改变其他票文档、proto／迁移／能力声明或发行产物，也没有提前推进 06c。


### 独立复审与功能提交

- 原独立审查者对修复轮 1 scoped 复审结论：**Spec 通过 / Quality 通过**，I1、I2、M1 全部 **ADDRESSED**；新问题 **Critical 0 / Important 0 / Minor 0**。主编排者据此明确授权功能提交，本票验收已勾选，Status 标记 **resolved**。
- 实际中文功能提交：**`9963847bbb3ad66dfd55c0c693541883ccb25eff`**「实现链式中继转发与离线缓存安全恢复」，包含 12 个 Go 源码／测试文件，含两个新 relay 文件及既有真实旧程序重连测试扩展。数据面、授权／预算／缓存和跨包回归相互依赖，作为一个完整功能提交；没有暂存任何 `.md`、dist/nodebundle/bin。
- 审查冻结后的代码没有变化，最终验证沿用本轮完整两包退出 0（forwarding **15.304 秒**、node **21.601 秒**，墙钟 **22.56 秒**）和指定 race 退出 0（forwarding **6.152 秒**、node **13.505 秒**，墙钟 **15.17 秒**）；没有为提交无意义重复昂贵测试。提交前工作区及暂存区 `git diff --check` 均退出 0，已逐项核对暂存区仅本票 12 个 Go 文件。
- 提交后暂存区为空，工作区只剩 02/03/04/05/06a/06b/09 七份 `.md`，继续等待本轮最终一次中文文档提交。06c 必要接缝仍为 `Engine.TunnelGrantResults()`（持久成功后发布的实际逐 grant 本地安装结果，非整链就绪）、`State.TunnelGrantResults`（完整恢复边界）、`Engine.ChainFailures()`（按当前规则代次／指纹隔离的最近真实建流失败），以及 `chain_relay_test.go` 中真实节点／故障／多层预算夹具；确认上送和整链当前证据投影由下一票接管。
- 未开始 06c、未宣告 forward_chains，未 push/deploy/merge；生产目标核验／发布与人工回滚演练仍归 09。
