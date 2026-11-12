# 06a 链式规则协议、持久化与按变更范围校验

Status: ready-for-agent
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

- [ ] 迁移 51、API/导入与相邻边编译完整，协议/指纹/错误/总预算接缝明确且未宣告 forward_chains。
- [ ] 历史重复节点只在被变更影响时校验，无关保存/重连/确认不阻塞。
- [ ] 所有可能末跳参与目标回环检测，监听占用按实际监听者，对外端口不占本机端口。
- [ ] A 的格式 2 完整缓存契约及真实旧版拒绝公共矩阵已定义，可由 06b 落地，不改 01 的读写范围。

## 不做的事

不实现 relay 数据面、真实 grant 确认/健康/事故投影或前端；不宣告多跳能力，不改变 A 或另升缓存格式，不提交构建产物或改变已定决策。
