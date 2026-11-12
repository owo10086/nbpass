# 06a 链式规则协议、持久化与按变更范围校验

Status: resolved
Blocked by: 04, 05（缓存按 spec 已定决策 7：A 实施）
Migration: 51（`chainRulesMigration`；原 06 的迁移完整归本票）
Parallel: 可与 07 CRUD 前端并行；本票独占规则后端、proto/生成物、迁移及编译模型；06b 在本票后接数据面，06c 在 06b 后集成并统一宣告能力。不改 07/08 的 web 文件。

## 目标与交接边界

端口通过 API 引用链，定义可持久化且可验证的多跳配置、逐 grant 结果和错误/预算协议。此票完成模型与协议接缝，不代表多跳可运行：06b 负责真实 relay/恢复，06c 负责确认、健康、事故及 `forward_chains` 能力声明。06a/06b 均不得提前宣告该能力；真实节点端到端验收统一在 06c 闭合。

## 规则、数据库及 HTTP

- 新 `chain_rules_migration.go` 定义迁移 51：`forward_rules.chain_id TEXT NULL REFERENCES forward_chains(id)`，默认 NULL；保留 exit_group_id。INSERT/UPDATE 触发器约束直连两引用均空、隧道恰好一项非空。避免重建 forward_rules 级联清除六张流量/授权子表；确须重建时按 `single_entry_migration.go` 回填全部子表并测试。
- `forwarding.Rule` 增加 `ChainID` / `json:"chain_id,omitempty"`；API 仍为 kind=direct|tunnel，以 exit_group_id / chain_id 二选一表达单跳/链。`Rule.Validate/Config`、`forwarding_api.go:ruleColumns/scanRule/saveRule`、`rules_json.go:storeRule/preflightRuleImport`、`rules_batch.go`、随机端口、引用查询全部接入。直连两引用均不带，单跳不带 chain_id，链式不带 exit_group_id。
- 导出格式升 3，导入保留 1/2，旧格式不能夹带链引用。预检、正式导入、普通保存、组/链编辑按同一变更影响范围校验；失败整批回滚。停用规则保留引用；链有任何规则引用时 DELETE 返回 409，不能级联删业务。
- GET rule.path 复用 05 hops，加实际入口、最终目标；链 rule_count 为真实引用数。06c 补 observations。`node_references.go`、`rule_entries.go` 的轮换参与者解析覆盖间接链引用；定义路径与候选不冒充实时选路。

## 静态校验与历史兼容

- 沿用 05 的单跳变更范围校验并扩到链引用：「路径服务器不重复」只在本次被变更影响的规则上执行，覆盖新建/编辑规则、导入项、改链的全部引用、改端点组/服务器组成员的全部直接或间接引用（含停用规则）。展开 `RuleEntryNodes` 的全部入口、每跳主备全集，两两不得重复，400 指明服务器。未引用链按 05 校验结构。入口组加人、规则移入口/切模式、链重排均不得绕过。
- `config_snapshots.go:16` 全量编译不能无条件对历史规则执行这条新增校验：节点重连、授权确认、能力变化或无关保存只重新编译，未受变更影响的历史规则照原语义编译；不能只复用旧快照避开新 revision/撤销收敛，也不能删除/停用历史规则。既有其他配置校验保留。变更影响到历史规则时才要求它满足新规则。
- **替换原 06 的编排者决定（2026-11-12）**：生产 46 库当时 exit_groups/exit_group_members 均为 0 行，不代表其他历史库也没有重复路径；明确采用上述「按变更影响范围校验」。原 `TestCombinedEntryExitCountsBusinessOnce` 中新保存重复路径应改为拒绝；计数回归用合法路径，另保留历史缓存/库编译回归。
- `forwarding/validation.go:90/115` 区分监听者与目标拨号者：业务监听只在实际入口；各跳仅占本机隧道/检测监听，relay 不占规则业务端口；对外端口只用于拨号，不算本机占用。
- 目标回指构图：直连从每个入口解释目标，单跳从所有出口候选、链式从所有可能末跳解释目标；127.0.0.1/localhost 属于实际拨号者，本机地址与可静态证明的域名结果连接到真实监听者，再做环路检测。保留 AllowPrivate、解析失败不伪造静态证明等现有边界，不能把入口的 loopback 当成末跳的 loopback。

## 编译、协议和持久化

- `tunnel_snapshots.go` / 新 `chain_snapshots.go` 展开入口集合×第 1 组、相邻组成员笛卡尔积；仅相邻服务器对生成 pairCredential。中间节点获得本机上游 peers/grants 与下一组，不获得无关跳私钥或任意代拨权。共用 02/03 的地址键与摘要。
- protobuf 追加并保留旧号：`ForwardRule.chain_id=285`、`hop_index=286`；`TunnelGrant.next_exit_group_id=285`、`hop_index=286`、`authorized_next_node_ids=287`；`NodeReport.tunnel_grant_results=285`。`TunnelGrantApplyResult` 定义 `entry_id=1,rule_id=2,generation=3,applied_revision=4,apply_error=5`；结果通过报告的 attempted_revision/digest、revocations_applied 绑定当前快照。运行 `make proto` 并提交生成物，proto 按 02→03→04→06a 交接，后续票消费已定合约。
- 入口 hop_index=0，保存链从 1 起；接收节点 grant.hop_index=i，下一跳 i+1；末跳 next_exit_group_id 空。ForwardRule.exit_group_id 是本节点下一组的运行引用；chain_id/跳数绑定规则代次，不接受客户端自由指定地址链。空 chain_id 继续旧单跳 wire 语义，不把零代次兼容用于新链。
- `tunnel_authorization.go` 指纹覆盖有序链内容、全部成员/地址配置摘要和最终目标，rule_id 代次持久递增。复用 `tunnel_authorization_confirmations(exit_id,entry_id,rule_id,generation)` 表示相邻边，entry_id 为真实直接前驱。
- 迁移 51 同时建 `node_tunnel_grant_results(node_id,entry_id,rule_id,generation,applied_revision,apply_error)`，主键 `(node_id,entry_id,rule_id)`，随节点/规则清理。06c 的确认处理必须按当前尝试 revision/digest 及磁盘撤销检查点验证结果，不让 relay 伪造监听规则报告。事故指纹/原因使用现有投影 source_key、refs/reasons、timeline 保存，不另占迁移。
- 逐流请求扩展链/跳、规则代次和 `remaining_budget_ms`；结果区分 `entry_exit`（本边连接失败）、`downstream_next_hop`（接收者后续边不可达）、`exit_target`（末跳目标不可达）、`local_resource`、`configuration_authorization`。附实际失败边的 from/to、address_id、hop_index、rule/grant 代次与 chain_fingerprint，校验属于当前授权路径；不带私钥或任意响应正文。
- 下游本边失败向上游回传时变为 downstream_next_hop 并保留真实归属；exit_target 逐层透传，不能将下游失败归为当前上游边。建流成功前且未发送业务字节可试同组下个健康已确认成员；资源/配置/授权错误停止本次重试，不重放已建立流。06b 实现，06c 消费同一失败原因。
- 端到端建流预算合约为 `min(调用方剩余期限,10 秒,入口 DialTimeout)`；每跳传递扣本地耗时后的剩余毫秒，以本地单调时钟计时，不依赖跨机绝对时钟同步。解析、握手、目标拨号、逐层同组重试共用预算；本机限额只能缩短，每组每轮最多 3 个不同候选，不能重置预算。
- 同一有序内容的配置指纹必须稳定，chain_id 不变但内容/地址/目标变化须有新指纹，供 06c 授权和事故历史共用。保存超 60 KiB 快照/64 KiB 控制消息整笔拒绝，错误有界。

## 迁移与缓存前置

- 本票独占 51，必须在 50 后追加；旧 chain_id 为 NULL，原 API/业务/流量与授权依赖数据保持。06b/06c 不新增迁移。
- 按 A 复用 01 的 `StateCurrent=2`、读 1/2 写 2；本票不另分配格式号。缓存契约包含 relay-only 下一组、完整 grant/代次、端点摘要、源 IP、高水位及撤销检查点，全部放入同一 `config.enc` 的外层 `version: 2`；06b 完成 State 收集及 Restore，不能只追加字段却仍写 1。真实旧读取器已有严格 `Version == StateCurrent(1)` 校验，在 Restore/重算摘要之前拒绝 2；不得另留旧格式加载路径或用摘要代替此保证。
- 公共验收交接沿用 01 固定的真实旧源码/二进制来源，新版实际写出的格式 2 必须被其明确拒绝，断网无业务监听/拨号；人工隔离后空启动仍停转发，重连只取能力内当前配置。新版仍读格式 1，保留旧高水位/撤销证据；同版本按 ADR 0006 恢复。一次完整 IX 发布，不发布本票中间构建或过渡版；09 交付跨格式重新安装及人工回滚步骤。

## 测试要求

- 单元：51 迁移保留流量/授权六张依赖表与外键，规则引用 XOR、导入 1/2/3、引用删除、变更影响范围、相邻边最小秘密、指纹稳定/变化、grant 结果协议和 60/64 KiB 边界。
- 回归：历史重复路径库中无关规则/链保存、节点重连、授权确认成功编译且历史业务不变；编辑该规则/引用组/入口组才拒绝。导入受影响项失败整批回滚。
- 静态环路正反例：单跳和链式末跳连 127.0.0.1/本机 IP，经末跳本机另一规则回到入口会成环则拒绝；只有某个备用末跳成环也拒绝；同端口仅在入口监听、末跳本机无该监听时不误判自环；非回环本地服务可通过。含 localhost/可确认 A 记录。
- 监听：中间节点隧道与本机业务/检测端口冲突拒绝；外部端口与本机业务端口同号但真实隧道端口不同可接受，relay 不虚构业务监听占用。
- clusterharness 新 `forward_chains_api_test.go` 的引用规则/变更范围扩展：HTTP 创建/互改/导入/引用删除、共享链编辑影响所有引用，规则与授权事务失败回滚，普通业务隔离。未宣告能力前链入口保持门禁，不声称此票已跑通多跳。
- 缓存公共测试定义并交接 06b：真实旧读取器加载 relay grant/源 IP/撤销检查点的格式 2 缓存，断开控制，必须明确拒绝且不转发；再验人工空启动与重连过滤，不得越跳、丢源绑定后转发或复活撤销。04 的源 IP 场景保留；新版读 1/2、未知格式拒绝与撤销检查点写 2 的合约不变。
- 运行 `go test ./internal/panel ./internal/forwarding ./internal/node -timeout=15m` 及 API/导入/迁移定向 clusterharness；完整数据面在 06b/06c 验收。

## 验收标准

- [x] 迁移 51、API/导入与相邻边编译完整，协议/指纹/错误/总预算接缝明确且未宣告 forward_chains。
- [x] 历史重复节点只在被变更影响时校验，无关保存/重连/确认不阻塞。
- [x] 所有可能末跳参与目标回环检测，监听占用按实际监听者，对外端口不占本机端口。
- [x] A 的格式 2 完整缓存契约及真实旧版拒绝公共矩阵已定义，可由 06b 落地，不改 01 的读写范围。

## 不做的事

不实现 relay 数据面、真实 grant 确认/健康/事故投影或前端；不宣告多跳能力，不改变 A 或另升缓存格式，不提交构建产物或改变已定决策。

## Comments

### 实施中（2026-11-12）

- 基于 feat/ix 的 ec59ecbc；唯一实施者按完整 06a 需求实施，保持 claimed 待独立审查；不修改其余票文档、不创建工作树、不提交构建产物。

### 实现与技术裁定

- 迁移 **51** 仅在 50 后追加：`ALTER TABLE forward_rules ADD COLUMN chain_id`，不重建父表；INSERT/UPDATE XOR 触发器与链外键约束同时生效。新增逐 grant 结果表按 `(node_id,entry_id,rule_id)` 主键，节点、前驱或规则删除时清理。50→51 测试逐表比较旧规则与六张流量／授权依赖表的原始行，检查授权高水位、NULL 默认值、外键与级联。
- 普通规则保存、随机端口、批量、导入预检／提交统一接入链引用。导出 3，导入 1/2/3；旧版本即使夹带空 `chain_id` 字段也拒绝。直连、单跳、链互改保留入口二选一约束；停用保留链引用，引用链 DELETE 为 409。链返回真实 rule_count；规则 path 返回实际入口集合、05 hops 及最终目标，observations 保持空数组，不冒充实时选路。节点引用与轮换参与者覆盖链的间接引用，轮换用真实直接前驱集合。
- 受影响集合扩展链、端点组、入口组的全部引用，含停用规则与备用成员。重复路径为 **400 且指明服务器**；原 05/计数回归只精确调整此状态断言，监听／环路仍保持既有 409。全量编译未引入路径唯一性检查；历史库无关保存、重连、授权确认继续真实 WSS 业务。目标回指按每个实际末跳解释 loopback／localhost／已证明 A 记录；本机监听仍只在实际入口，外部映射端口不占本地端口。
- 相邻编译只生成入口集合×第一组和相邻两组的凭据；grant.entry_id 是直接前驱。中间节点无业务监听，grant 中 `NextExitGroupId`、`AuthorizedNextNodeIds` 是唯一下一组拨号许可；末跳两者为空。共享链／单跳组使用同一有序合并，避免生成重复运行组。能力计划采用逆向可达后缀加正向入口可达、精确逐边过滤；某个专线备用缺能力不会阻塞仍完整可用的公网路径，也不会把 B2 的下一边授权交给 B1。
- **协议最终技术裁定（主编排者批准）**：ForwardRule `chain_id=285`、`hop_index=286`、`ChainAuthorization chain_authorization=287`；嵌套 `fingerprint=1,edges=2`，`ChainEdgeAuthorization` 的 `from_node_id/to_node_id/address_id/hop_index/endpoint_config_digest` 为 1–5。原先提出 287 单字符串的草案未发布，最终合并成结构化可信上下文，不新增 288。TunnelGrant 的 285–287、NodeReport 285、TunnelGrantApplyResult 1–5 及既有 280/281/284 原号保留。`make proto` 实际生成，最后重复生成逐字节一致。
- 指纹来自数据库中的有序链／有序成员、每个实际地址的 02/03 EndpointConfigDigest（node/address/地址值/有效端口/transport/TLS server_name）、最终目标、实际入口集合及末跳源 IP；不依赖节点报告或入口自报。所有 grant.Rule 带同一可信指纹及持久递增代次，同内容重编译不递增；主备顺序、备用地址／映射端口、目标、源 IP、链重排均改变指纹。新链不能使用旧零代次兼容，保留旧单跳语义。
- 可信 edges 只是错误证据上下文，无私钥／凭据，不扩大拨号权限。每份 relay grant 只带自身真实入边和从自身可达的后缀，不能验证同组兄弟的伪造失败。`ValidateChainFailureFromNext` 必须传入本轮真正选中的 endpoint（含 EntryId/address/digest），再验证相应可达子图、rule/grant 代次与可信 fingerprint；06b 接收远端响应须用此入口，不能仅用整链成员关系。
- 逐流 JSON 已定义 chain_id/hop_index/chain_fingerprint/remaining_budget_ms，规则授权匹配检查真实前驱、代次、目标、链与接收跳；新链零预算或超过 10000 ms 拒绝。错误对象只含实际边、地址、跳、两种代次、指纹与地址摘要，不含自由响应正文。`entry_exit` 上传改为 `downstream_next_hop` 保留归属，`exit_target` 不改；中间节点本地资源／授权错误可定位自身并停止重试，只有 exit_target 须为末跳。
- `ChainBudget`/`ChainRemainingBudgetMS` 为 06b 提供共享本地单调期限：min(父 context 剩余期限、10 秒、本机限额、上游剩余时间)，毫秒向下取整，零预算不重置；解析／握手／目标拨号／重试共用同一 context，`ChainMaxCandidates=3`。06b 负责真正接入 relay、取消与重试，06c 才按逐 grant 磁盘检查点确认并开放入口。当前真实节点未宣告 forward_chains；编译与协议测试的能力快照是显式测试夹具，不声称多跳数据面已上线。
- 逐 grant 错误纳入既有 NodeReport 有界摘要预算，保留每个结果身份／代次。快照 60 KiB 与控制消息 64 KiB 以真实 protobuf 编码长度测试；超大整批导入预检和提交均回滚，真实 gRPC 服务验证 65536 字节可收、65537 字节 ResourceExhausted。

### 06b 缓存与协议交接

- 不新增缓存格式或加载路径：沿用 01 的读 1/2、只写 2、同一 `config.enc`、相同 AEAD 附加数据和先持久化撤销检查点流程。新 proto 的 relay-only 下一组、完整 grant（直接前驱、下一组许可、hop、代次、可信指纹／边）、端点摘要与源 IP 必须与高水位／撤销检查点一起纳入 State；06b 需要补 State 组收集与 Restore，不能把空入口 Rules 的 relay 还原成末跳直拨。
- 公共验收矩阵交 06b 执行：新版实际写出 relay-only 缓存、末跳源 IP 缓存、撤销检查点三类格式 2；以 01/04 固定真实旧源码 `3899928362a75b58ac74ff6844652d319760b656` 的严格 `Version==1` 读取器和真实旧 Run/二进制读取，断开控制须明确拒绝并退出，业务监听／下一跳／目标连接为零。按 spec 原样隔离后旧版离线空启动仍停转发，重连只取能力内当前快照；不得改 version、加载旧副本或自动跳过普通缓存损坏。
- 新版读格式 1 要保留高水位和撤销证据后写 2；同版本格式 2 按 ADR 0006 离线恢复。保留 04 源 IP 四格及同 revision 重新核验失败的窄准入，不重构该逻辑。本票没有冒称已执行 06b 的 relay State/真实旧版矩阵；06b 完成后交 06c，09 核验实际生产回滚目标与完整一次发行安装步骤。

### 红绿与验证记录

以下记录实际命令及退出码；包耗时为 Go 输出，墙钟为 `/usr/bin/time -p`。未通过的测试均保留原断言意图，修复实现或明确的夹具错误。

| 命令 | 退出码／实际耗时 | 结果 |
|---|---|---|
| `go test ./internal/panel -run TestChainRulesHTTPReferencesAndImport -count=1 -timeout=2m`（红） | 1；panel 1.714 秒，墙钟 3.00 秒 | chain_id 尚未接入，创建 400 而应 201。 |
| 同命令（基础接入后） | 0；panel 1.750 秒，墙钟 3.42 秒 | 引用、互改、导入、path/count/delete 通过。 |
| `go test ./internal/forwarding -run TestChainTargetDialerLoops -count=1 -timeout=2m`（初始） | 1；墙钟 0.23 秒 | 新接缝未实现；初稿另缺 netip import，随后修正。 |
| 同命令（保留旧构图的占位接缝） | 1；forwarding 0.607 秒，墙钟 1.23 秒 | 夹具没有设置保留端口，出现端口 0 冲突；补真实默认端口。 |
| 同命令（有效红） | 1；forwarding 0.592 秒，墙钟 1.23 秒 | 127.0.0.1／localhost／A 记录的备用末跳环路确实未识别。 |
| `go test ./internal/forwarding ./internal/panel -run 'TestChain(TargetDialerLoops|RulesHTTPReferencesAndImport)' -count=1 -timeout=2m` | 0；forwarding 0.597、panel 1.154 秒，墙钟 2.70 秒 | 末跳构图修复与基础 API 通过。 |
| `go test ./internal/panel ./internal/forwarding -run 'TestChain' -count=1 -timeout=2m` | 0；panel 1.700、forwarding 1.749 秒，墙钟 3.36 秒 | 初版协议与模型通过。 |
| `go test ./internal/panel ./internal/forwarding -run 'TestChain' -count=1 -timeout=3m` | 0；panel 3.208、forwarding 1.516 秒，墙钟 4.73 秒 | 51 保全、相邻最小秘密、指纹、协议与预算通过。 |
| `go test ./internal/panel ./internal/forwarding ./internal/node -timeout=15m`（首轮） | 1；panel 51.827、forwarding 11.648、node 14.534 秒，墙钟 52.36 秒 | 唯一失败为 05 固定 409／迁移50的旧断言，按06a合约精确改为400／当前迁移数。 |
| `go test ./internal/panel ./internal/forwarding -run 'Test(Chain|ForwardChains)' -count=1 -timeout=3m` | 1；panel 3.491、forwarding 1.895 秒，墙钟 5.04 秒 | 已收窄 relay 证据为自身后缀；旧测试期望含兄弟的5条，修正为入边+本机两条后边共3条，并增加兄弟伪造拒绝。 |
| `go test ./internal/panel -run TestChainCapabilityPlanKeepsUsablePublicPath -count=1 -timeout=2m`（回置旧并集行为的红） | 1；panel 0.641 秒，墙钟 1.95 秒 | 专线备用缺能力错误阻塞公网路径；恢复逐边可达计划后纳入后续绿测。 |
| `go test ./internal/node -run TestChainGrantReportErrorsStayBounded -count=1 -timeout=2m`（红） | 1；node 0.650 秒，墙钟 1.31 秒 | grant错误未分摊预算导致整份报告超限；扩展统一限额后纳入绿测。 |
| `go test ./internal/panel ./internal/forwarding ./internal/node -run 'Test(Chain|ForwardChains)' -count=1 -timeout=3m` | 0；panel 4.728、forwarding 1.318、node 2.531 秒，墙钟 6.22 秒 | 协议、能力边、边界、旧历史、管理与导入通过。 |
| `go test ./internal/clusterharness -run 'Test(.*ForwardChainsAPI|RulesJSON|TunnelManagementValidationAndGates|CombinedEntryExitCountsBusinessOnce)' -count=1 -timeout=5m`（首轮） | 1；clusterharness 4.351 秒，墙钟 6.25 秒 | 新测试辅助路径误写 importpreflight，补 `/preflight`，未改服务契约。 |
| `go test ./internal/panel ./internal/forwarding ./internal/node -timeout=15m`（第二轮） | 0；panel 52.295、forwarding 11.166、node 14.206 秒，墙钟 52.87 秒 | 三个完整包通过。 |
| `go test ./internal/panel ./internal/node -run 'TestChain' -count=1 -timeout=3m` | 0；panel 3.695、node 0.396 秒，墙钟 5.03 秒 | 增补实际末跳/备用/本机IP/A记录、真实监听/映射及真实gRPC 64KiB通过。 |
| 上述定向 clusterharness 命令（最终该轮） | 0；clusterharness 3.686 秒，墙钟 4.97 秒 | 真实HTTP链引用、共享编辑、导入1/2/3、授权事务故障回滚、门禁与普通业务隔离、历史WSS业务重连确认全部通过。 |
| `make proto` 后对两份生成文件 SHA256 前后比较 | 0；墙钟 3.900 秒 | 真实生成逐字节一致；此前实现时也已运行真实生成。 |
| `go test ./internal/panel -run 'TestChainRules(Migration51|Adjacent)' -count=1 -timeout=3m` | 0；panel 1.829 秒，墙钟 3.15 秒 | 增补INSERT XOR、成员重排、末跳源指纹变化通过。 |
| `go test ./internal/panel -run TestChainRulesNeverUseLegacyZeroGeneration -count=1 -timeout=2m`（红） | 1；panel 0.732 秒，墙钟 2.04 秒 | legacy比较器缺显式chain排除；补空chain约束，保持旧单跳兼容。 |

- 未修改07/08前端、compatibility能力声明或缓存版本。未使用浏览器，未生成额外报告／日志／方案，未覆盖真实02双二进制。其他02/03/04/05/09未提交文档保留；所有.md等本轮最终统一中文文档提交。本票代码保持未提交、Status=claimed，等待独立审查和主编排者批准功能提交。

### 独立审查前最终稳定验证

- `go test ./internal/panel ./internal/forwarding ./internal/node -timeout=15m` 最终退出 **0**：panel **51.321 秒**；forwarding/node 使用有效缓存（最近完整运行分别 **11.166 / 14.206 秒**，之后对应源码未变）；墙钟 **52.69 秒**。包含零代次兼容显式拒绝链的修复。
- `git diff --check` 退出 **0**，墙钟 **0.02 秒**；`git status --short web/dist internal/nodebundle bin` 无输出。原 02/03/04/05/09 五份未提交文档没有被本实施者改动。暂不勾选完成或提交，交独立审查复核本票范围与上述证据。
- 最后一处 legacy 比较器变更后，再运行 `go test ./internal/clusterharness -run 'Test(.*ForwardChainsAPI|RulesJSON|TunnelManagementValidationAndGates|CombinedEntryExitCountsBusinessOnce)' -count=1 -timeout=5m` 退出 **0**，clusterharness **4.371 秒**，墙钟 **6.28 秒**；链／导入／历史真实授权确认及普通业务回归仍通过。

### 独立审查与功能提交

- 主编排者安排的独立只读 `review_06a` 已正式批准并复核全部源码、最终 Comments、legacy 零代次排除和最终验证证据：**Spec 符合本票及最终技术裁定 / Quality 通过；Critical 0 / Important 0 / Minor 0**。本票验收项已勾选，Status 标记 **resolved**。
- 主编排者明确授权后完成中文功能提交：**`27ea261d1a5465ed872c13420f7fd5689f36a3c5`**「支持链式规则持久化与相邻授权编译协议」，含 34 个源码／测试／protobuf 文件。迁移、规则引用与协议编译相互依赖，因此保留一个完整功能提交。提交前工作区与暂存区 `git diff --check` 均退出 0。
- 未暂存任何 `.md`、dist/nodebundle/bin，其他 02/03/04/05/09 未提交文档保持；本票文档同样等待最终统一中文文档提交。未重复已通过且无变更的昂贵测试，未开始 06b，无 push/deploy/merge。
