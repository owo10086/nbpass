# 06c 逐跳授权确认、健康就绪与事故集成

Status: resolved
Blocked by: 06b（缓存按 spec 已定决策 7：A 实施）
Migration: 无（使用 06a 的 51 与既有 operations/incidents 表）
Parallel: 在 06b 后串行完成后端集成；可与 07 CRUD 前端并行，07 的多跳集成验收等待本票；08 在本票及 07 全部完成后联调。本票最后统一宣告 forward_chains。

## 目标与完成边界

接通面板完整路径编译、逐 grant 持久应用确认、逐边/后续路径健康、服务器组入口与 operations/incidents。经真实 HTTP + 节点端到端测试后，最后在 `compatibility.Current()` 宣告 forward_chains；只有 API/relay 或局部握手成功都不算本票完成。沿用 06a 错误协议、指纹与 06b 数据面，不重复定义链模型。

## 授权确认与能力门禁

- `config_results.go` / 新 grant 报告处理、`control.go/config_sync.go`、`node.go/config_report.go` 使用 06a 的逐 grant 报告与持久表；校验当前会话、完整名单、重复项、伪造/旧代次、attempted_revision/digest、撤销检查点，旧连接/旧报告不得覆盖当前结果。relay 不伪造入口监听规则状态，局部端口错误不永久卡住其他端口。
- 从末跳向前逐层确认：本地 grant 已持久应用且相应下一组有获授权的已确认候选才向前就绪；中间 grants 可先安装，未就绪不放行上游。最终至少一条完整逐边确认路径才下发入口监听。内容相同重编译/确认不能递归推进 revision 形成循环。
- 入口和所有执行跳检查 forward_chains 及授权/主备基础能力；地址语义按 01 分别检查拨号/成员身份，源绑定检查末跳。能力不足候选不获链配置或 relay grant，原组顺序与 blocker 保留，不跳过一组。
- 能力不足导致无完整路径时显示「整条阻塞（需升级服务器）」；存在完整路径但备用缺能力时为「可运行但备用成员能力不足」，具体 blocker 保留 node/address/capability/reason。备用升级先清旧确认、下发并持久应用、重新确认，再按原顺序加入候选，不能复用旧代次/健康证明。

## 健康、入口就绪与轮换

- `panel/exit_health.go`、`entry_readiness.go`、`readiness.go/upgrade_check.go` 以及链 GET 聚合真实逐边观察；按 `(from_node_id,to_node_id,address_id,endpoint_config_digest)` 匹配规则实际选中的端点，再结合对应 rule/grant 代次和完整后续路径就绪。03 已修正服务器 ID 合并，本票不能回退该键。没有新鲜证据为未知/历史，A→B 握手成功不等于整链就绪；探测不拨目标。
- 06a 的失败类别/实际失败边、06b 的同组重试与总预算在同步与观测中保持一致：B1→C 失败不能让 A→B1 健康变坏，目标错误不伪装为入口链路问题；配置/授权/资源错误与网络错误分别保留。
- `exit_blocked.go` 公网换 IP 确认覆盖所有实际公网直接前驱（含中间节点/离线前驱），专线不冒充公网消费者；`tunnel_rotation.go` 证书/凭据轮换覆盖直接前驱及同节点各地址客户端，旧材料保留至当前参与者确认。控制离线不设缓存授权到期，在线另一端撤销仍阻断离线上游的新流并结束旧流。

## operations/incidents（本票负责，非 09 补功能）

- `operations_snapshot.go/operations_evaluate.go/operations_model.go` 增加链路径节点与逐边/grant/blocker 投影；入口 Members 的决策语义保持，另以 path_nodes 展开入口及各跳全部候选，供每个中间/末跳节点反查受影响端口。共享链影响多规则要全部关联，其他规则不误关联。
- operationRule/operationReason 的公开证据保留 chain_id、chain_fingerprint、from/to、address_id、端点摘要、hop_index、rule/grant generation、期望/应用 revision、失败类别/原因及观测时间；无对应新鲜样本显示未知或历史，不伪装节点入口监听结果，不保存秘密或第三方响应。
- `incidents_rules.go/incidents_projector.go/incidents_store.go` 使用规则配置指纹作为历史边界：指纹包括 chain_id、有序链内容、全部成员地址配置摘要、最终目标及相关规则语义；不能只用 chain_id/入口 revision。复用 `operationRule.Configuration` 与 `source_key`、refs/reasons JSON、timeline 持久化，无新增迁移。节点/链/规则删除后保留历史引用快照。
- 链 ID 不变但改内容/成员/地址，完整读取当前配置后旧记录记「新版本替代」，新失败进入新指纹；停用/删除记「配置变更结束」；仅新鲜当前证据解除异常才记「异常解除」，不推断目标应用正常。过期、控制离线、存储/投影读取失败不能结束为恢复；迟到旧配置证据不能重开/污染新记录。面板重启保留边界和时间线去重。
- 控制离线与公网被墙作为独立证据：中间节点控制断开、B 公网被墙而专线仍通，都不等同于业务中断，按已有业务/授权/边证据表达可用或未知。operations/incidents 为观察投影，入口切换、DNS、换 IP 等外部动作仍交 ADR 0005 的唯一协调者，不从事故投影另触发动作。

## 服务器组入口集成

- 主备组与全部发布组分别让 A1/A2 接同一条链，按每个实际入口到第一组及完整后续路径判断就绪，不能把 A1 证据套给 A2。
- 仅 A1 专线不可达、A2 可达：主备按既有策略选择就绪成员/协调 DNS，全部发布按既有逐成员摘除/加回与最少保留数屏障执行；逐成员就绪、期望与已确认 DNS 记录按现有发布策略验收，不能把两种策略统一成组内单选。
- 后跳全坏：A1/A2 均失去完整路径就绪，明确无候选；DNS 按既有无就绪策略及全部发布最少保留数屏障执行，不靠盲目切另一入口掩盖故障，更不能因 B→C 失败启动入口公网换 IP。恢复后按既有观察窗/主备恢复/发布策略重新纳入。
- 控制离线但缓存专线业务仍通：控制/就绪证据可未知或历史，不以控制离线单一事实判业务中断或驱动新动作；沿用既有国内检测点/发布政策。确有公网换 IP 证据时仅归实际受影响公网节点及消费者，专线失败不借用公网自测归因。所有动作核对动作 ID/期望版本、串行协调与迟到结果去重。

## 迁移、缓存与发布前置

- 无新迁移；确认表使用 51，operations/incidents 复用既有存储，迁移号 47–51 不变。
- 按 A 消费 01/06b 的读 1/2、写 2，保持 `StateCurrent=2` 与同文件 `config.enc` 外层 `version: 2`（含所有撤销检查点），不另升格式。真实旧格式 1 读取器的严格版本比较必须在 Restore 前拒绝；旧进程因此退出停转发，人工隔离后空启动、重连只收能力内当前配置。本票通过 HTTP + 全链节点回归 06b 的真实旧读取矩阵，涵盖 relay-only、直连/末跳源 IP、撤销检查点，无越跳、无丢绑定后转发、无授权复活；同时验证新版读格式 1、同版本离线恢复。矩阵通过后才宣告能力，不能用在线门禁或新版模拟代替；09 再负责一次完整发布的跨格式重新安装/人工回滚演练，不把 09 的发行工作反设成本票依赖。

## 测试要求

- 单元：逐 grant 完整/重复/旧连接/伪造/代次/磁盘失败、稳定重编译、逐层确认与局部失败隔离；有完整路径/仅备用不足的状态区别及升级重新确认；逐边、地址摘要、历史与过期就绪。
- operations/incidents 单元/持久化：中间/末跳节点关联全部受影响端口、逐边/grant/blocker 原因；同 chain_id 改内容/成员地址产生新边界，旧记录正确结束；停用/删除/恢复/证据过期/读取失败/迟到样本、重启去重；控制离线与公网被墙但专线正常均不误判业务中断。
- clusterharness 新 `forward_chains_test.go/chain_authorization_test.go/chain_failover_test.go`：通过 HTTP 建资源、真实 WSS/yamux/TCP 传字节，A→B→C→目标及再多一跳；A/B 禁止直拨目标，末跳独占解析，含中间/末跳域名专线与真实对外端口映射。
- 各组 B1/B2、C1/C2 独立故障/恢复/全坏，非对称 A→B1 好、B1→C 坏、B2→C 好时换 B2 且 A→B1 仍健康；全链失败在共享总预算内、每组最多 3 次，目标故障不污染前一边。两地址一好一坏及同 address_id 改地址后迟到旧摘要丢弃同时检查入口就绪与 incidents。
- 控制离线/节点重启仍完整转发；在线下游撤权拦离线上游新旧流；链编辑作用于所有引用端口，移入口/切模式/停删旧授权收敛且其他端口不受影响；上游伪造下一组/目标/原始入口被拒。
- 末跳写盘失败不得启用入口；逐跳证书/凭据轮换等待离线前驱；公网换 IP 保留专线；末跳源 IP/非本机错误；旧节点每个位置的收包过滤、仅备用能力不足、备用升级重新确认；导入回滚/引用删除与规则计量不按跳数放大。
- 新 `chain_entry_groups_test.go` 覆盖上节主备组、全部发布组 × 仅 A1 专线不可达/后跳全坏/恢复/控制离线矩阵，断言逐成员就绪、DNS 期望与实际/动作记录、换 IP 归属、协调者去重；不能只断言端口能通。
- 运行 `go test ./internal/panel ./internal/forwarding ./internal/node -timeout=15m`、`go test ./internal/clusterharness -run 'Test.*(ForwardChain|ChainAuthorization|ChainFailover|ChainEntryGroup)' -timeout=20m`，relay/轮换/撤销/确认相关 race；全量归 09。

## 验收标准

- [x] 真实多跳、逐 grant 确认、地址/后续路径就绪及局部失败隔离完整，不把握手当整链就绪。
- [x] operations/incidents 能按每跳节点关联受影响端口，逐边/grant/blocker 原因和配置历史边界准确。
- [x] 主备/全部发布入口组矩阵通过；DNS/换 IP 归属正确，外部动作仍由 ADR 0005 协调者执行。
- [x] 旧节点收包过滤、备用升级重新确认、离线缓存真实旧读取器/回滚矩阵、撤权/轮换/计量回归通过。
- [x] 06a/06b 与本票验收全部满足后，才统一宣告 forward_chains；API 可交 07 集成及 08 消费。

## 不做的事

不改 07/08 前端，不从观察投影新增动作协调者，不改变 A 或另升缓存格式，不做 UDP/IPv6/智能绕路/管理中转，不提交构建产物。

## Comments

### 实施中（2026-11-12）

- 基于 `feat/ix` / `9963847bbb3ad66dfd55c0c693541883ccb25eff`，唯一实施者；完整消费 06a/06b 已审查合约，先逐 grant 确认与末跳前推，再接完整路径观测与事故，最后真实 HTTP 矩阵。已有七份未提交文档不改，不提交功能／文档，冻结后交独立审查。

### 中断接续与逐项验证（2026-11-12）

- 接续核对仍为 `feat/ix` / `9963847bbb3ad66dfd55c0c693541883ccb25eff`，无遗留测试进程；完整保留八份修改中的原票文档及前一实施者代码。不建 worktree、不切分支、不提交，证据只更新本票。测试前读取原 spec、ADR 0006/0009、GLOSSARY/DESIGN/CLAUDE 和 06a/06b Comments。
- 修复后 `go test ./internal/node -run 'Test(Traffic|ConfigReport|CachedTunnel)' -count=1 -timeout=3m` 退出 **0**，node **4.175 秒**，墙钟 **4.87 秒**；普通心跳/流量/探针/轮换帧不夹带逐 grant/revocation 确认，缓存尝试结果保持独立帧。
- 预发布测试仅使用系统临时目录的 Go overlay 为 `Current()` 临时加入 `forward_chains`，保留真实能力交集，不增加生产开关。以下标注 overlay 的命令前缀均为 `go test -overlay /tmp/codeg-acp-501/77041-83fc5633/ix06c-6app_0sk/overlay.json`；overlay 不进仓库，最终需正式能力宣告后无 overlay 验证。
- HTTP 非对称夹具的初始自造专线 ID、错误 `/api/operations` 路由、读取尚未采样的新健康状态三处接线错误已修正（这些不算功能红证据）。专线 ID 取 API 返回值，运维读 `/api/overview`，等待真实探测/采样。`go test`（overlay）`./internal/clusterharness -run TestChainFailover -count=1 -timeout=3m` 退出 **0**，包 **3.022 秒**／墙钟 **4.38 秒**；B1→C 全坏、B2→C 成功，A→B1 本边保持健康；后跳全部坏与恢复均检查完整路径就绪。
- `chainRig` 后续统一采用中间/末跳域名专线与实际 TCP 端口映射代理，所有非末跳禁止拨目标，目标域名仅末跳解析。服务器组矩阵使用真实 HTTP 建资源、真实控制会话和 WSS/yamux/TCP。
- 既有发布政策解释：专线未就绪本身不是 GLOSSARY/ADR 0005 的自动切换/摘除触发。仅 A1 专线坏先验证 A1 未就绪、A2 就绪、无 DNS/换 IP 自动动作，再用已有手动协调者切 A2 并拒绝相同旧期望版本。另有真实入口控制离线与国内检测点不可达触发自动协调：只选完整路径就绪 A2；后跳全坏无候选保持最后解析。全部发布单纯未就绪保留各记录，真实离线确认才摘除，并验证最少保留数及重复证据不重复动作。未把手动切换冒称专线故障自动切换。
- `go test`（overlay）`./internal/clusterharness -run 'TestChain(EntryGroup|Failover)' -count=1 -timeout=5m` 退出 **0**，包 **20.657 秒**／墙钟 **22.02 秒**：主备/全部发布 × 单入口专线故障、后跳全坏、恢复、relay 控制离线但缓存业务继续；逐成员就绪、候选、DNS 期望与服务商实际记录、期望版本拒绝、离线自动候选筛选、无候选与最少保留屏障、无错误公网换 IP、动作去重均断言。

### 真实边事件与轮换修复

- 新红测 `TestChainFlowSuccessCannotEraseDifferentEdgeOrHistoricalFailure` 捕获“同一节点另一条边成功抹掉失败”和“同一实际边成功不能清前驱转述失败”，`TestChainTerminalBusinessSuccessHasTargetEvidence` 捕获末跳没有真实目标建流成功事件。前者命令 `go test ./internal/panel -run 'TestChain(FlowSuccess|GrantHeartbeat)' -count=1 -timeout=2m` 退出 **1**，包 **3.857 秒**／墙钟 **5.42 秒**；末跳定向命令退出 **1**，包 **1.232 秒**／墙钟 **1.95 秒**。
- 观测按实际边、rule、rule/grant generation、fingerprint 归并，只在结果变化后持久入有界事件队列；同组备用成功不清未经过的坏边。末跳仅在真实目标拨号和授权复查成功后记录 target 成功，探测仍不拨目标。投影重新检查当前 process/snapshot/revision/可信后缀；历史/离线证据不会成为恢复。
- 初次修复验证暴露数据库单连接内嵌查询阻塞，主动终止该 panel 测试进程后将验证改为复用完整读取的 runtime snapshot，未留下运行进程。修正后 `go test ./internal/panel ./internal/forwarding -run 'TestChain(FlowSuccess|GrantHeartbeat|TerminalBusiness|Establishment)' -count=1 -timeout=2m` 退出 **0**，panel **4.750 秒**、forwarding **0.931 秒**／墙钟 **6.34 秒**。
- 增加 `TestChainFlowSuccess/expired_after_failure` 有效红：过期成功曾解除尚未消除的失败，定向退出 **1**，panel **0.951 秒**／墙钟 **1.75 秒**。修复后只允许新鲜当前成功解除该边，过期成功保留历史未知，多个边严重度合并不受 map 遍历次序影响。
- HTTP 授权矩阵首轮修正重启监听等待、预留端口被误当真实业务监听、准备阶段等待三处夹具问题后，缓存全链离线新进程恢复、在线下游撤销离线上游新旧流、末跳写盘失败隔离其他端口、同/新进程重连和丢首个服务端响应均通过；仅轮换末尾旧流被误断为有效红（`TestChainAuthorizationRotationWaitsForEveryDirectPredecessor` 两格，包 **7.088 秒**／墙钟 **9.05 秒**）。
- 轮换根因是把逐 grant 确认绑定到整节点 revision；证书/凭据过渡改变节点 revision 时会暂撤入口/relay。现在保留真实 applied_revision，仅在 grant 全部内容不变时允许跨无关快照连续有效；编译事务主动清除改变/删除 grant 的确认，下一组/地址/代次变化仍必须重新确认。不会伪造新的 applied_revision。
- 事件、确认、轮换集中验证：`go test`（overlay）`./internal/panel ./internal/clusterharness -run 'TestChain(Grant|FlowSuccess|AuthorizationRotation)' -count=1 -timeout=3m` 退出 **0**，panel **11.826 秒**、clusterharness **3.929 秒**／墙钟 **14.04 秒**。仍为实施中，不提前勾选全部验收。

### 完整后缀、事件顺序与持久基准收口

- 入口和 relay-only 的 readiness/upgrade 检查现在沿获授权后缀递归，复用既有资源 lease，并共享一次预算、每组最多三个不同候选；只检查授权路径，不解析或拨最终目标。`TestChainReadinessAndUpgradeCheckCompleteSuffixWithoutTarget` 捕获原来 relay-only 只看本边的缺口。原四个预算测试将故障注入放在真实预检完成后，继续验证业务建流的候选上限与总预算，而非把新增预检耗时混入业务计数。
- 首轮完整 HTTP 矩阵暴露两项问题：异步探针尚未采样就读取旧的 historical 状态；固定测试时钟下同一时间戳的旧 success 覆盖了新的 target failure。前者修正夹具的阶段边界，独立 target 阶段先等待所有物理边恢复，后置断言仍要求全部六条当前 node/address/digest、rule/grant generation、fingerprint 对应的新鲜健康样本，同时保留 `exit_target` 分类断言。不是过滤坏边，也不是用目标失败替代本边失败。后者为真实顺序缺口：引擎在锁内给观测递增本进程序号，节点按实际边取最新，面板同时间优先实际边 owner，同 node/process 才比较序号，跨进程保留接收顺序。
- 上述非对称/逐候选与公网换 IP 验证：`go test -overlay /tmp/codeg-acp-501/77041-83fc5633/ix06c-6app_0sk/overlay.json ./internal/panel ./internal/clusterharness -run 'TestChain(PublicAddressRotation|Failover(Asymmetric|EachHop))' -count=2 -timeout=3m` 退出 **0**；panel **2.457 秒**、clusterharness **17.160 秒**，墙钟 **19.01 秒**。公网轮换测试先补真实 rotation_actions 外键夹具，验证末跳只等待实际直接 relay、中间跳等待入口、离线前驱仍未确认，专线不被列作公网消费者且地址不被覆盖。
- 顺序回归采用临时 overlay 去掉对应修复，未改生产文件：面板 `TestChainFlowSuccess/same_timestamp` 在移除 owner/同进程序号判定后有两格有效红（新失败被旧成功擦除、迟到上游转述覆盖 owner 恢复），退出 **1**，panel **2.256 秒**／墙钟 **4.45 秒**；节点 `TestChainEventsSameTimestamp` 去掉同时间戳序号排序后有效红，退出 **1**，node **0.631 秒**／墙钟 **1.35 秒**。跨进程序号重置、不同边互不抹除同时保留单测。修复后的三包完整测试覆盖全部新单测。
- 接收成功时由面板重新计算 AcceptedSuccess/Historical/Retained，测试故意自报三个 true 验证节点不能伪造。只有当前 process/snapshot/revision 与可信实际边匹配且首次收到时未过期的成功可解除失败；接受后的恢复基准跨证据过期、无关快照、进程切换和真实面板重开持续生效，迟到更早失败不能复活，之后新失败可以重开；首次收到已过期成功不能解除。
- 发现并补足全局最近 1000 条事件截断及历史 retention 的持久边界：`go test ./internal/panel -run 'TestChainFlowSuccess/(unrelated_event_volume|retention)' -count=1 -timeout=2m` 原实现退出 **1**，panel **2.871 秒**／墙钟 **4.64 秒**。1001 条无关链事件或 91 天历史清理均曾让未解除失败凭空消失。修复复用 events/message，按实际边及当前规则/代次/指纹保留**一条**面板接收基准；被替代的事件仍按普通历史策略清理，配置替换/删除后的旧基准也按原策略清理，不增加迁移或第二套动作协调。读取错误继续向上返回，不制造恢复。`go test ./internal/panel -run 'Test(ChainFlowSuccess|RetentionStorage)' -count=1 -timeout=2m` 退出 **0**，panel **6.959 秒**／墙钟 **8.42 秒**。
- 开放能力前最新完整 HTTP 与真实旧程序隔离回归：`go test -overlay /tmp/codeg-acp-501/77041-83fc5633/ix06c-6app_0sk/overlay.json ./internal/clusterharness -run 'Test.*(ForwardChain|ChainAuthorization|ChainFailover|ChainEntryGroup)|TestOutboundIPv4ActualV1ManualIsolationAndReconnect' -count=1 -timeout=20m` 退出 **0**，clusterharness **49.641 秒**／墙钟 **51.98 秒**。
- 开放能力前最新完整三包：`go test -overlay /tmp/codeg-acp-501/77041-83fc5633/ix06c-6app_0sk/overlay.json ./internal/panel ./internal/forwarding ./internal/node -timeout=15m` 退出 **0**，panel **70.727 秒**、forwarding **15.868 秒**、node **23.327 秒**／墙钟 **72.72 秒**。这次覆盖最终事件基准、顺序与 retention 修复，替代更早中间测试结果。
- 以上开放前矩阵全部通过后才在 `compatibility.Current()` 正式加入 `ForwardChains`，移除 HTTP/面板夹具的临时能力追加；未改能力交集或保留测试旁路。能力契约测试在声明前有效红（exit **1**，compatibility **0.631 秒**／墙钟 **1.04 秒**）。以下继续记录正式声明后的无 overlay 最终验证，未完成独立审查前本票仍为 claimed，不提交。

### 正式能力声明后的验收映射（待独立审查）

| 验收项 | 已执行的自动化证据 |
| --- | --- |
| 两/三级真实链、目标域名仅末跳解析，中间节点无目标访问 | `TestForwardChainHTTPRealMultiHopAndOffline`，`TestChainFailoverAsymmetricEdgesAndRecovery`；实际 HTTP/控制节点/WSS/yamux/TCP 与域名专线映射端口 |
| 后向逐 grant 持久确认、完整名单/重复/伪造/旧代次/旧 revision/digest/写盘失败、局部失败隔离与稳定 revision | `TestChainGrantConfirmationBeforeEntry`、`TestChainGrantReportsRequireExactCompleteCurrentEvidence`、`TestChainGrantHeartbeatDoesNotConfirmAndPartialStillConfirmsAfterSummary`、`TestChainAuthorizationTerminalDiskFailureIsolatesOtherPorts`，配合既有当前 controlSession 旧连接拦截 |
| 缓存完整结果独立帧、普通首帧不推断确认、同/新进程重连及无 ACK 重发、64KiB/2048事件边界 | `TestChainCachedReportReconnectFramesAndBoundedEvents`、`TestChainControlGRPC64KiBBoundary`、`TestChainAuthorizationReconnectResendsCompleteGrantResults`；80 grant 错误与 16×2048 事件组合仍独立帧，伪造未发送事件 ACK 被拒绝 |
| 单边/完整后缀/地址摘要/未知与历史就绪 | `TestChainHealthRequiresEveryCurrentAuthorizedEdge`、`TestChainReadinessAndUpgradeCheckCompleteSuffixWithoutTarget`、`TestChainFailoverAddressIsolationLateDigestAndIncidentBoundary`；本边成功不能掩盖坏后缀，相同 address ID 新内容不能复用迟到旧摘要 |
| B1→C 坏/B2 好、后跳全坏及恢复、每组各候选独立故障，目标错误不污染前边 | `TestChainFailoverAsymmetricEdgesAndRecovery`、`TestChainFailoverEachHopCandidateRecoveryAndTargetClassification`；保留实际失败边与独立 target 类别，探针只表述本物理边 |
| 主备/全部发布入口组、自动 offline 条件、DNS 实际/期望、无候选与最少保留、版本去重 | `TestChainEntryGroupPrivateFailurePublicationMatrix`、`TestChainEntryGroupOfflineAutomaticCandidateAndMinimumKept`；专线失败本身不新增自动动作，按前述既有发布政策区分手动切换与真实 offline 自动协调 |
| operations/incidents 按中间/末跳关联共享多规则，排除无关规则、地址/内容新边界、停删/重启/读取失败/历史引用 | `TestChainIncidentsReferencesAndConfigurationBoundary`、`TestChainIncidentsSharedNodeReferencesReadFailureRestartAndDeletion`、`TestChainFailoverAddressIsolationLateDigestAndIncidentBoundary`；共享链编辑/移入口/切直连/停删另由 `TestChainAuthorizationSharedEditMoveModeAndDeleteIsolation` 覆盖 |
| 真实边恢复、旧过程/快照/过期不恢复、已接受恢复跨重启和保留期、顺序及无关大批事件 | `TestChainFlowSuccessCannotEraseDifferentEdgeOrHistoricalFailure`、`TestChainEventsSameTimestampKeepsNewestRealOutcomePerEdge`、`TestChainTerminalBusinessSuccessHasTargetEvidence`、`TestRetentionStorageBatchBoundariesAndRealtimeCursor` |
| 全执行位置旧节点门禁、备用不足仍可用与升级重新下发/持久确认 | `TestChainAuthorizationEveryOldPositionFiltered`、`TestChainAuthorizationBackupUpgradeWaitsForFreshDurableConfirmation`；实际阻塞新版 grant 发送期间不能复用旧确认，放行后再让主用故障证明备用真实可用 |
| 控制全离线新进程缓存恢复、在线下游撤销离线上游新旧流、轮换等待直接前驱与旧流连续 | `TestChainAuthorizationOfflineRestartAndDownstreamRevocation`、`TestChainAuthorizationRotationWaitsForEveryDirectPredecessor`；配合 `TestChainPublicAddressRotationIncludesOfflineDirectPredecessorsOnly` 与既有同节点多地址轮换隔离测试 |
| 真实旧读取器拒绝、人工隔离空启动/重连能力过滤、新版读旧缓存与源绑定/检查点 | `TestForwardChainActualOldReaderRejectsHTTPNodeCachesAndCheckpoints` 覆盖 entry/relay-only/terminal-source × 最终缓存/撤销检查点六格；既有 `TestActualV1Reader...` 八格随 node 整包执行；`TestOutboundIPv4ActualV1ManualIsolationAndReconnect` 验证完整旧程序回滚后所有位置过滤与兼容当前直连恢复 |
| 多跳计量不放大、half-close/RST、预算/候选上限/资源隔离/撤销持久化 | `TestChainAuthorizationHTTPRuleTrafficCountedOnce` 真实 HTTP 三跳端口只计 4 字节上传与 10 字节下载；06b `TestChainRelay...` 系列随 forwarding 整包与 race 回归，仍消费既有边界而非重定协议 |

- 最终无 overlay：`go test ./internal/panel ./internal/forwarding ./internal/node -timeout=15m` 退出 **0**，panel **72.475 秒**，forwarding/node 为 Go 缓存命中（与上一轮最终代码 overlay 测试内容一致），墙钟 **74.11 秒**。
- 最终无 overlay：`go test ./internal/clusterharness -run 'Test.*(ForwardChain|ChainAuthorization|ChainFailover|ChainEntryGroup)' -timeout=20m` 退出 **0**，clusterharness **47.608 秒**／墙钟 **49.67 秒**。
- 能力契约：`go test ./internal/compatibility -count=1` 退出 **0**，compatibility **0.690 秒**／墙钟 **1.18 秒**。
- 为显示真实旧程序来源与所有进程退出证据，最终无 overlay 运行 `go test ./internal/clusterharness -run 'TestForwardChainActualOldReaderRejectsHTTPNodeCachesAndCheckpoints|TestOutboundIPv4ActualV1ManualIsolationAndReconnect' -count=1 -v -timeout=5m`，退出 **0**，clusterharness **5.398 秒**／墙钟 **6.03 秒**。完整固定旧源 **3899928362a75b58ac74ff6844652d319760b656** 在系统临时测试目录构建，SHA256 **32054cfe3cfe3c040ded228e1052c6f145d70eb847714f1d60540e9968c18acd** 与 06b 一致；六格旧程序均 exit status 1，在 Restore/监听/控制前拒绝。人工隔离后离线空启动不恢复业务；真实重连过滤 entry/relay/terminal 链配置及源绑定规则，显式清除源绑定后仅当前兼容直连恢复。未覆盖 bin 中两份 02 基线。

### 最终代次隔离与 race

- 最后核查补入「指纹不变但授权 generation 增加」边界。原投影仅筛 fingerprint，旧 generation 失败仍会成为当前历史原因：`go test ./internal/panel -run 'TestChainFlowSuccess/new_generation_same_fingerprint' -count=1 -timeout=2m` 有效红，退出 **1**，panel **1.793 秒**／墙钟 **3.24 秒**。改为 JOIN 当前 rule_authorizations 的 rule/generation/fingerprint 后退出 **0**，panel **1.208 秒**／墙钟 **2.69 秒**；新代次没有当前证据仍保留未知，不由旧失败/成功决定新代次状态。
- 此最后生产修复后的指定无 overlay 三包：`go test ./internal/panel ./internal/forwarding ./internal/node -timeout=15m` 退出 **0**，panel **76.186 秒**、forwarding/node 缓存命中，墙钟 **77.68 秒**。指定无 overlay HTTP：`go test ./internal/clusterharness -run 'Test.*(ForwardChain|ChainAuthorization|ChainFailover|ChainEntryGroup)' -timeout=20m` 退出 **0**，clusterharness **48.629 秒**／墙钟 **51.00 秒**。
- 首轮 `go test -race ./internal/panel ./internal/forwarding ./internal/node ./internal/clusterharness -run 'Test(Chain|ForwardChain|TunnelRotation|TunnelAuthorizationConfirmation|ConfigReportCannot|CachedTunnel|RetentionStorage)' -timeout=20m`：panel **294.090 秒**、forwarding **7.023 秒**、node **2.005 秒**通过，无 race 报告；clusterharness **197.657 秒**失败，整个命令退出 **1**／墙钟 **300.02 秒**。失败只在 `TestChainAuthorizationBackupUpgradeWaitsForFreshDurableConfirmation` relay/terminal 两格的在途控制等待，不掩盖为通过。
- 备用升级定向 race 复现夹具问题：原循环每 20ms 无条件推进 1 秒，在 race 下配置编译/密码计算尚未完成就触发 15 秒控制期限，出现未到达 grant gate 或释放后等待 idle 超时；定向复现退出 **1**，clusterharness **17.196 秒**／墙钟 **22.58 秒**。现在先恢复网络再启动新进程，直接等待实际发送 gate 信号，不在受阻配置发送期间快进控制时钟。确认前不入候选、释放后新持久确认、主用坏后备用真实接管的所有断言保留。`go test -race ./internal/clusterharness -run TestChainAuthorizationBackupUpgradeWaitsForFreshDurableConfirmation -count=1 -v -timeout=3m` 退出 **0**，两格通过，clusterharness **12.312 秒**／墙钟 **15.08 秒**。随后重跑整个原 cluster race 集合。
- 最后代次过滤的独立 race：`go test -race ./internal/panel -run 'TestChainFlowSuccess/new_generation_same_fingerprint' -count=1 -timeout=3m` 退出 **0**，panel **8.562 秒**／墙钟 **11.90 秒**。
- cluster race 集合第二轮退出 **1**，clusterharness **183.278 秒**／墙钟 **183.99 秒**；备用升级已通过，只剩 `TestChainEntryGroupOfflineAutomaticCandidateAndMinimumKept/all/suffix_true` 在假时钟 105 秒后即读取尚未完成的异步 DNS 摘除。改为等待具体成员真实离线确认、协调 tick、期望/已确认/服务商三方结果，并要求恰好一次写入。
- 加强终态等待又揭示原最少保留断言不充分：默认 `startChecker` 只接受 8.8.8.8 的任务，对第二入口 1.1.1.1 返回执行器错误，结果 unknown；旧测试仅见 DNS 保留，不能据此断言触发了最少保留屏障。强化后的定向 race 有效暴露两格（all/suffix_true、all/suffix_false），退出 **1**，clusterharness **50.193 秒**／墙钟 **53.03 秒**。改用既有 `allOfflineChecker(nil)` 为两个入口产生真实 TCP 不可达，必须先看到第二入口 `unreachable` 和 `entry_member_kept` 事件才验证最后记录保留；保留动作/版本/重复写入断言，不修改生产发布策略。此前最少保留结论以这组强化后的最终结果为准。
- 强化后的 `go test -race ./internal/clusterharness -run TestChainEntryGroupOfflineAutomaticCandidateAndMinimumKept -count=1 -v -timeout=3m` 退出 **0**，主备/全部发布 × 后缀通/坏四格全通过，clusterharness **41.539 秒**／墙钟 **44.33 秒**；第一成员真实确认后摘除，第二成员真实确认后触发最少保留事件，外部写入不重复。最后再运行完整原 cluster race 集合。
- 最终完整 cluster race：`go test -race ./internal/clusterharness -run 'Test(Chain|ForwardChain|TunnelRotation|TunnelAuthorizationConfirmation|ConfigReportCannot|CachedTunnel|RetentionStorage)' -timeout=20m` 退出 **0**，clusterharness **190.675 秒**／墙钟 **191.84 秒**，无 race 报告。与此前已通过的 panel/forwarding/node race、新代次过滤定向 race 共同构成本票最终 race 证据。最后两轮修复只调整测试夹具，未变更生产代码；没有重跑无变化旧程序矩阵或整仓测试，整仓归 09。
- 冻结检查：`git diff --check` 通过，44 份修改/新增 Go 文件 `gofmt -l` 无输出；当前仍为 **feat/ix / 9963847bbb3ad66dfd55c0c693541883ccb25eff**。八份既有修改 md 保留，本轮新证据仅追加本票 Comments；无额外方案/审查/总结/日志文件，无新迁移、worktree、切分支、提交、push、部署、浏览器操作或产物写入。bin/dist/internal/nodebundle 无新增/修改列入工作区差异。
- 两份 02 保留产物仅只读核对：`bin/nbpass-node-ix02` SHA256 **bed243861b9d6a25657cbf1a016a7c85ed29cc334491464c5f76cacc1cb03058**；`bin/nbpass-node-ix02-linux-amd64` SHA256 **ef3ad98e58b6ed50b048f5604e7ad351e8b6f2725e4e1babfa4f41f353551b81**，均与 02 Comments 一致，没有重建/覆盖。
- **实施已冻结，等待独立审查，不提交**。保留 Status: claimed 及未勾选审查验收项。审查重点：逐 grant 内容相同才跨无关 revision 延续的事务边界；完整后缀预检的共享预算/资源 lease；事件 owner/同进程序号/当前代次隔离与面板重算的单边持久基准；主备/全部发布仍遵循既有协调者和自动触发政策。07/08 可按现有公开 API 集成，正式完成与进入下一票由独立审查结论确认。

### 独立审查修复轮 1（进行中，2026-11-12）

- 初次冻结的独立审查结果为 **Spec ❌ / Quality Needs fixes，Critical 0 / Important 3 / Minor 0**，已按授权继续由同一实施者修复，不提交。三项为：I1 同 Class+Edge 的新真实成功被永久去重，过期/旧快照恢复未接受或上下游心跳错位后无法再次恢复；I2 中间节点空 To 的 local_resource 没有同身份真实恢复结果，额度释放后事故永久历史未知；I3 NodeID:Code 时间线键会吞掉多个前驱到同一后继的独立边事实。
- I1 红测：真实 HTTP 生成失败、恢复和再次成功，仅在真实控制报文边界把首条成功设为过期/旧 snapshot；旧实现再次真实成功仍保留失败。新增节点持久队列测试证实 99 次新观测合并后仍被压成首条一条；心跳间终端真实失败/恢复而上游独立转述的回归也暴露 owner 第二条成功被压制。夹具中的网络失败冷却窗和异步报文尚未落库，分别改为终端目标暂时失败及等待实际事件后再进入下一阶段，不算功能红证据；事故解除用单入口节点规则，避免把未配置 DNS 发布状态的 unknown 混入业务恢复。
- I1 修复只按每个心跳合并的最新真实 Observation（原 Sequence/ObservedAt/实际边）与当前 snapshot 做去重；同一观测/同一快照不重复写，不改观测时间、不由探针或心跳生成业务成功，仍使用现有有界持久事件队列。`go test ./internal/node ./internal/clusterharness -run 'TestChain(EventsRefresh|Failover(NewRealSuccess|RecoveryBetween))' -count=1 -timeout=3m` 最终退出 **0**，node **0.471 秒**、clusterharness **6.403 秒**／墙钟 **7.85 秒**。
- I2 有效红：`go test ./internal/forwarding ./internal/clusterharness -run 'TestChain(RelayLocalResource|FailoverRelayQuota)' -count=1 -timeout=3m` 退出 **1**，forwarding **1.201 秒**、clusterharness **3.741 秒**／墙钟 **5.18 秒**；实际中间 quota 拒绝有 From=B/To 空，但业务再次可通后没有对应恢复，事故仍为历史未知。owner 验证单测 `go test ./internal/panel -run TestChainLocalEstablishment -count=1 -timeout=2m` 退出 **1**，panel **0.645 秒**／墙钟 **2.11 秒**。
- I2 增加明确 `local_established`：只在 Check=false、逐规则资源分配、当前授权复查和完整后缀/目标建流均成功后由该节点生成；面板严格校验 owner、rule/gen/fingerprint/hop，转述恢复不接受。local_resource/local_established 使用独立资源 scope，不能清网络/target 失败；quota 拒绝处实际 owner 也记录真实失败序号，避免同时间戳的旧 owner 成功覆盖上游新失败。首次恢复验证 `go test ./internal/forwarding ./internal/panel ./internal/clusterharness -run 'TestChain(RelayLocalResource|LocalEstablishment|TerminalBusiness|FailoverRelayQuota)' -count=1 -timeout=3m` 退出 **0**，forwarding **0.653 秒**、panel **0.688 秒**、clusterharness **2.958 秒**／墙钟 **5.52 秒**。正在强化恢复后同时间戳再次耗尽/二次恢复序列。
- I3 有效红：`go test ./internal/panel -run TestChainIncidentsTimeline -count=1 -timeout=2m` 退出 **1**，panel **1.360 秒**／墙钟 **2.83 秒**；B1→C、B2→C 两条探针事实及由同 A 转述的两条建流失败，四条只持久化两条。修复按完整实际边/授权/结果生成 fact hash，探针采样时间不进入稳定键，真实失败保留 Sequence/ObservedAt 以区分后续真实转换；测试核对消息里的实际 from、地址摘要变更与类别转换，并反复投影/重开数据库验证去重。后续继续记录绿色及最终受影响验证，尚未再次冻结。
- I1 最终夹具的反向验证仅用系统临时 overlay 恢复旧 Class+Edge 去重，不修改生产文件：`go test -overlay /tmp/codeg-acp-501/77041-83fc5633/ix06c-r1-old-dedup-4awy41g_/overlay.json ./internal/node ./internal/clusterharness -run 'TestChain(EventsRefresh|Failover(NewRealSuccess|RecoveryBetween))' -count=1 -timeout=3m` 退出 **1**，node **0.725 秒**、clusterharness **8.732 秒**／墙钟 **11.00 秒**；节点新真实结果刷新、HTTP expired、old_snapshot、心跳间 owner 失败/恢复三种场景均有效红。当前实现没有该 overlay。
- I2 强化序列 `go test ./internal/clusterharness -run TestChainFailoverRelayQuota -count=1 -timeout=3m` 退出 **0**，clusterharness **2.444 秒**／墙钟 **4.86 秒**；先耗尽→真实恢复并结束原事故，再于同一假时钟 tick 产生新成功和新额度拒绝，实际 owner 的失败序号保持异常并产生新事故，二次真实建流再解除。`go test ./internal/panel -run 'TestChain(Local|IncidentsTimeline)' -count=1 -timeout=2m` 退出 **0**，panel **1.220 秒**／墙钟 **2.69 秒**，包含同节点空 To 的 local_resource 与 exit_target 同时存在时，local_established 只清资源失败。
- I3 修复后 `go test ./internal/panel -run 'TestChainIncidents' -count=1 -timeout=2m` 退出 **0**，panel **4.395 秒**／墙钟 **5.87 秒**；共享后继的两条探针事实与同源转述的两条建流失败均独立落库；地址摘要和结果类别变化产生新事实，采样时间刷新、重复投影及数据库重开均不重复。
- 本轮复审范围仅以下 12 个 Go 文件及本票 Comments：`internal/node/chain_events.go`、`chain_events_test.go`；`internal/forwarding/chain_observation.go`、`chain_relay.go`、`chain_relay_test.go`、`tunnel_limits.go`；`internal/panel/chain_events.go`、`chain_events_test.go`、`events.go`、`incidents_rules.go`、`chain_incidents_test.go`；新增 `internal/clusterharness/chain_events_test.go`。未修改兼容性声明、发布政策、迁移、缓存格式或旧程序矩阵。以下补记最后受影响验证后再冻结。
- 修复轮 1 最终无 overlay 三包：`go test ./internal/panel ./internal/forwarding ./internal/node -timeout=15m` 退出 **0**，panel **86.591 秒**、forwarding **15.525 秒**、node **32.697 秒**／墙钟 **87.37 秒**（三包均实际执行）。
- 修复轮 1 最终无 overlay HTTP：`go test ./internal/clusterharness -run 'Test.*(ForwardChain|ChainAuthorization|ChainFailover|ChainEntryGroup)' -timeout=20m` 退出 **0**，clusterharness **66.041 秒**／墙钟 **67.27 秒**。
- 修复轮 1 最终精准 race：`go test -race ./internal/node ./internal/forwarding ./internal/panel ./internal/clusterharness -run 'TestChain(Events|RelayLocalResource|TerminalBusiness|Local|FlowSuccess|Incidents|Failover(NewRealSuccess|RecoveryBetween|RelayQuota))' -timeout=10m` 退出 **0**；node **4.138 秒**、forwarding **2.862 秒**、panel **115.264 秒**、clusterharness **31.614 秒**／墙钟 **122.44 秒**，无 race 报告。
- **修复轮 1 已完成并再次冻结，等待原审查者 scoped 复审，不提交。** 三项 Important 均已有有效红、实际修复和对应自动化；I1 真实 HTTP 证明拒收首条恢复后再次真实成功可解除，I2 真实额度耗尽/恢复/同 tick 再耗尽/再次恢复及资源 scope 隔离均通过，I3 多前驱共享后继的全部事实和重开去重均通过。保持 `Status: claimed`、原验收项不勾选；最终是否通过由独立复审决定。
- 最终只读核对：本轮 12 个 Go 文件 `gofmt -l` 无输出，`git diff --check` 通过；仍为 `feat/ix` / `9963847bbb3ad66dfd55c0c693541883ccb25eff`。本轮仅修改上列文件与本票 Comments，未创建其他文档/日志、worktree、分支或提交，未改构建产物、缓存版本/迁移、兼容性声明或对外动作政策。

### 修复轮 2：总额度拒绝的实际 owner 顺序（进行中）

- 原三项 Important 经独立复审均为 ADDRESSED；本轮仅处理新发现的总 `MaxConnections/MaxPendingDials` 门禁拒绝。门禁位于读取 rule 之前，只返回无 Edge 的 local_resource，实际 B 未留下顺序；上游按授权合成 B→空后，面板在同 timestamp 下错误保留 B 旧 local_established。
- 已批准取舍：仍在认证 carrier 后、解析请求前执行总资源门禁，拒绝不读取 rule、不新增 worker/额度槽/拨号；仅附实际拒绝 owner 的进程实例（最多128字节）和共用 engine 观测序号。实例+序号是有界顺序元数据，不构成授权，也不为所有 grant 捏造失败。上游仍按当前真实所选后继和已有可信后缀验证失败 scope，逐跳保留 origin，不能用转述序号替代；面板核对 owner 当前实例，同实例按 origin 序号排序，不跨实例比较数值。没有新 proto、迁移或自由授权协议；代价是在现有4096字节响应和2048字节事件预算内增加少量字段。
- 初始 HTTP 红 `go test ./internal/clusterharness -run TestChainFailoverNodeResourceOrderAndRecovery -count=1 -timeout=3m` 退出 **1**，clusterharness **5.302秒**／墙钟 **7.47秒**：connections 有效复现，pending 因等待35秒假时钟产生额外超时，不作为有效功能红。夹具修正为第9秒真实成功/拒绝、仅推进1秒报送，修正后同命令退出 **1**，clusterharness **7.160秒**／墙钟 **9.19秒**，两格均在无超时污染下缺失 B→空拒绝。
- 实现首次编译发现 queryer 仅提供 QueryContext，已改用关闭 rows 后返回的 operationRows 查询 owner 实例，保持单连接事务无嵌套查询。首轮绿色同 HTTP 命令退出 **0**，clusterharness **3.799秒**／墙钟 **6.61秒**；两格均真实耗尽→面板异常→释放→真实建流→事故解除。继续补顺序、实例、可信 scope 及资源预算边界，尚未冻结。
- 边界用例已补：`TestChainResourceOriginOrderPreservedAcrossRelays` 真实两跳转述末跳 node/rule/pending 三类额度失败，验证 owner 序号不被独立 relay 序号替代、不增加目标解析；`TestTunnelNodeResourceRefusalDoesNotReadRequest` 在不发送任何 request 时接收拒绝，验证无 rule 事实且额度计数不增；未认证 carrier 不生成序号。面板覆盖空/超长实例、零序号、错误 class/scope/owner/代次、本地 owner 与实例/序号不符，以及同 timestamp 成功→新拒绝→恢复→较晚时间戳的迟到旧拒绝、跨实例重置、旧实例历史、旧无元数据 owner 基准兼容。
- 边界首次运行中面板夹具缺少后向授权确认而索引空 rules；补与原事件夹具相同的确认步骤后，`go test ./internal/forwarding ./internal/panel -run 'Test(ChainResource|TunnelNodeResource)' -count=1 -timeout=3m` 退出 **0**，forwarding **0.494秒**、panel **1.189秒**／墙钟 **2.66秒**。这一夹具错误不计功能红。
- 受影响集合 `go test ./internal/forwarding ./internal/panel ./internal/node ./internal/clusterharness -run 'Test(Chain(Resource|Events|Local|FlowSuccess|Failover(NodeResource|NewRealSuccess|RecoveryBetween|RelayQuota)|Relay|TerminalBusiness)|Tunnel(NodeResource|Readiness|Unfinished)|.*Resource.*)' -count=1 -timeout=10m` 退出 **0**，forwarding **3.891秒**、panel **9.420秒**、node **1.734秒**、clusterharness **15.542秒**／墙钟 **18.08秒**。
- 新元数据的必要并发边界：同一心跳前，owner 序号5的拒绝先到、序号2的旧拒绝后到，转述侧 map 不能按本地到达顺序丢掉5。`go test ./internal/forwarding -run TestChainResourceOriginCoalescing -count=1 -timeout=1m` 有效红，退出 **1**，forwarding **0.647秒**／墙钟 **1.85秒**；已改为相同 origin 实例保留较大 owner 序号，响应仍原样转述其实际拒绝序号。继续最终精准 race 与指定完整三包/链 HTTP，旧程序独立矩阵沿用修复轮1证据。
- 最终精准 race（同上述受影响 regex，增加 `-race`）退出 **0**：forwarding **4.801秒**、panel **102.066秒**、node **1.745秒**、clusterharness **49.205秒**／墙钟 **108.24秒**，包含新增 owner 乱序合并红后绿，无 race 报告。
- 最终无 overlay 完整三包 `go test ./internal/panel ./internal/forwarding ./internal/node -timeout=15m` 退出 **0**：panel **84.446秒**、forwarding **15.227秒**、node **27.940秒**／墙钟 **86.61秒**，均实际执行，覆盖普通隧道公共路径。
- 最终指定链 HTTP 初次 `go test ./internal/clusterharness -run 'Test.*(ForwardChain|ChainAuthorization|ChainFailover|ChainEntryGroup)' -timeout=20m` 退出 **1**：clusterharness **69.243秒**／墙钟 **71.96秒**，唯一失败为已有 `TestChainFailoverAddressIsolationLateDigestAndIncidentBoundary:96` 在推进75秒时等待控制消息空闲超5秒，不是业务断言失败。未修改代码/夹具或放宽超时，单独 `go test ./internal/clusterharness -run TestChainFailoverAddressIsolationLateDigestAndIncidentBoundary -count=3 -timeout=3m` 退出 **0**：clusterharness **9.444秒**／墙钟 **10.59秒**，三次未重现。继续无并行三包负载的指定整组复验；不能仅以单例通过覆盖首次集合失败。
- 最终指定链 HTTP 整组复验 `go test ./internal/clusterharness -run 'Test.*(ForwardChain|ChainAuthorization|ChainFailover|ChainEntryGroup)' -count=1 -timeout=20m` 退出 **0**：clusterharness **59.017秒**／墙钟 **60.13秒**。没有测试放宽或生产改动；首次控制空闲等待超时已保留记录，单例三次与整组复验均未重现。
- 本轮仅修改 12 个 Go 文件及本票 Comments：`internal/forwarding/chain_observation.go`、`chain_relay.go`、`chain_relay_test.go`、`engine.go`、`exit_failover.go`、`tunnel.go`、`tunnel_limits.go`、`tunnel_limits_test.go`；`internal/node/node.go`；`internal/panel/chain_events.go`、`chain_events_test.go`；新增 `internal/clusterharness/chain_resources_test.go`。工作区其他既有文档/代码与外部前端改动均原样保护，未暂存/提交。
- **修复轮 2 已完成并冻结，等待原审查者仅针对新增 Important 及修复新增问题的 scoped 复审。** 保持 `Status: claimed`、不勾选审查完成项、不提交；未创建 worktree/分支/辅助文档/日志，未修改 proto、迁移、构建产物、兼容性声明或对外动作政策。最终指定三包、指定链 HTTP 与精准 race 全部通过；本轮12个文件 gofmt无输出、git diff --check通过，仍为 `feat/ix` / `9963847bbb3ad66dfd55c0c693541883ccb25eff`。

### 修复轮 3：跨 origin 实例的上送前覆盖窗口（进行中）

- 独立复审 I1/I2/I3/I4 均 ADDRESSED；新增 I5：同心跳前先收到当前 owner 新实例拒绝，再收到旧实例迟到拒绝，engine 边键及 node 合并键均未区分 origin 实例，旧回复覆盖新失败；Panel虽正确将旧实例判历史，当前失败却已丢失。
- 有效红：`go test ./internal/forwarding ./internal/node -run 'TestChain(ResourceOrigins|EventsDifferentResource)' -count=1 -timeout=2m` 退出 **1**，forwarding **1.264秒**、node **1.942秒**／墙钟 **3.11秒**。新 current→old1→old2→old3 同一心跳的4条不同实例证据，两层最终都只剩1条。
- 已批准的有界交付策略：仅资源 origin 候选按实例独立保留，同实例仍按 owner 序号合并。待持久交付缓存上限与现有 EventQueue 一致为4096条/4MiB（单条事件仍2048字节）；不猜测“最近两个”哪个是当前实例。节点原子持久化整个观测批次与显式 overflow gap 后，才按候选快照序号回收 engine 缓存；写失败保留，交付期间新增更高序号不被旧ack清除。额外内存为有界候选及至多两份常数大小的gap累计，历史实例成功交付后不长期积累。持久队列保持既有count/bytes/发送批次/重启/缺口预算，不新增wire、proto或授权。
- 容量耗尽不能伪造恢复：丢失计数与真实时间范围复用 `event_gap` 持久交付并明确标记链式资源证据缺口。Panel对该reporter当前可信后缀保留unknown/缺口证据，旧成功不能掩盖；每节点只保留最新gap基准，直到后缀各owner均有缺口及其接收之后的真实成功，普通历史清理不制造恢复。尚在补这部分边界与回归，未冻结。
- 首绿 `go test ./internal/forwarding ./internal/node -run 'TestChain(ResourceOrigin|Events)' -count=1 -timeout=2m` 退出 **0**，forwarding **1.924秒**、node **1.379秒**／墙钟 **2.82秒**。当前未完成：容量/写失败/重启精确gap验证、真实持久节点事件批次→Panel当前/历史分离、unknown不结束事故、精准非race/race及范围核对。
- 外部07已明确完成独立前端提交，当前HEAD正常前进到 `3bcd286d373d2330d388cf6adb419b388355a978`（仅外部前端文件）；本轮Go改动仍未提交。继续保护外部07文件，后续集成由root接续。
- 容量/交付边界已补：4096个独立实例候选后新增拒绝只增加明确gap；4MiB字节限制独立生效；写失败保留所有候选并将交付期间新gap精确合并，重试成功才回收，交付期间更高序号留下。node将候选+gap在一次持久提交中发布，磁盘失败不推进去重状态，队列容量丢1条加engine gap2条后重开仍准确为3条、范围覆盖原时间，未ACK批次重开保留相同ID；重复已交付观察不积累。
- `go test ./internal/forwarding ./internal/node ./internal/panel -run 'TestChain(Resource|Events)' -count=1 -timeout=3m` 中 forwarding **1.339秒**、node **1.932秒**通过；panel新夹具最初错误使用非0700的t.TempDir根作为FileStore，按既有夹具改为子目录后 `go test ./internal/panel -run TestChainResourcePersistentBatch -count=1 -timeout=2m` 退出 **0**，panel **2.413秒**／墙钟 **3.81秒**。夹具目录错误不计功能红。
- 面板集成使用真实加密EventQueue写入、重开、Batch与NodeReport接收：正常current先到、3个旧实例后到，当前拒绝为abnormal且旧事件各自historical；Count=1故意溢出丢current时，资源gap令状态unknown并留下事故证据，91天retention和面板重开均不能让旧成功制造恢复。后缀只有B新成功仍unknown，B/C后续真实成功全齐才解除。缺口本身不声称某个owner真的失败，因失去具体候选范围而保守覆盖该reporter获授权的资源后缀。
- 本轮实际Go范围11份：forwarding `engine.go`、`chain_relay.go`、`chain_relay_test.go`、新增 `chain_resource_events.go`；node `chain_events.go`、`chain_events_test.go`、`events.go`；panel `chain_events.go`、`chain_events_test.go`、`retention.go`、`incidents_rules.go`。原公共队列Record/Batch/Ack、64KiB及未发送ACK拒绝、实际旧reader混合overlay将在最终完整三包覆盖；因新增公共持久化变更，完整三包及指定链HTTP必须重新执行，未冻结。
- 最终精准 race：`go test -race ./internal/forwarding ./internal/node ./internal/panel -run 'Test(Chain(Resource|Events|Control)|EventQueue|RetentionStorage)' -count=1 -timeout=10m` 退出 **0**，forwarding **2.813秒**、node **3.720秒**、panel **23.417秒**／墙钟 **27.50秒**。核对测试名后，未发送ACK拒绝另位于CachedReport用例，额外 `go test -race ./internal/node -run TestChainCachedReportReconnectFramesAndBoundedEvents -count=1 -timeout=2m` 退出 **0**，node **1.794秒**／墙钟 **2.84秒**。无race报告。
- 最终无 overlay 完整三包 `go test ./internal/panel ./internal/forwarding ./internal/node -timeout=15m` 退出 **0**，panel **73.497秒**、forwarding **15.287秒**、node **22.936秒**／墙钟 **74.40秒**，均实际执行。覆盖普通事件Record/Pending/Batch/Ack与写失败、64KiB/未发送ACK拒绝、retention/重开、ActualV1Reader实际旧reader混合overlay编译及既有缓存拒收；没有另外重跑独立旧二进制矩阵。
- 指定HTTP首次 `go test ./internal/clusterharness -run 'Test.*(ForwardChain|ChainAuthorization|ChainFailover|ChainEntryGroup)' -count=1 -timeout=20m` 退出 **1**，clusterharness **57.148秒**／墙钟 **59.80秒**。唯一失败为已有 `TestChainAuthorizationOfflineRestartAndDownstreamRevocation/entry:90` 在撤权后推进3秒，旧流仍能转发。未修改授权路径或放宽等待；定向 `go test ./internal/clusterharness -run TestChainAuthorizationOfflineRestartAndDownstreamRevocation -count=3 -timeout=3m` 退出 **0**，clusterharness **7.100秒**／墙钟 **9.12秒**，三次未重现；继续整组复验，首次失败保留。
- 有界gap新引入的实际边界已补：现有队列有overflow/expired两类独立gap，旧expired可在新overflow之后上送；只留reporter最后一条会错误掩盖更晚的overflow。最终策略改为**每节点每类reason各保留最新基准，至多2条**，这是既有两类缺口契约，不是按到达顺序猜当前origin实例。测试追加旧expired晚到、owner仅早于overflow的成功，仍必须unknown，retention/面板重开也不能恢复；只有两类gap均有完整后续成功才解除。`go test ./internal/panel -run TestChainResourcePersistentBatch -count=1 -timeout=2m` 退出 **0**，panel **2.467秒**／墙钟 **4.53秒**；该小改只涉及已有本轮3个panel文件，正在精准race及完整panel复验。
- 两类gap分别保留基准后的最终补验：`go test -race ./internal/panel -run 'TestChainResourcePersistentBatch|TestRetentionStorage' -count=1 -timeout=3m` 退出 **0**，panel **17.451秒**／墙钟 **21.15秒**；`go test ./internal/panel -timeout=15m` 退出 **0**，panel **70.986秒**／墙钟 **71.55秒**。forwarding/node代码自上次完整三包与精准race后未变，沿用该次有效结果。
- 对首次HTTP撤权失败进一步核对：`restore`只检查connected，原用例PUT后快进3秒，未确认在线节点已持久应用撤权，无法区分“应用尚未结束”与“应用后复查失效”，不能以授权代码未改排除间接阻塞。按ADR真实边界强化夹具：记录在线端旧revision，PUT后**不推进业务时钟**等待每个在线端更高的applied=desired及实际发送快照rule/grant撤除；仍用既有5秒真实传输等待，批次写阻塞配置会直接令该屏障失败。应用完成后立刻断言新流拒绝，只推进**1秒**再断言旧流关闭；没有放宽安全时限或改生产授权。新增本轮文件范围 `internal/clusterharness/chain_authorization_test.go`（总计12份Go）。强化后 `go test ./internal/clusterharness -run TestChainAuthorizationOfflineRestartAndDownstreamRevocation -count=3 -timeout=3m` 退出 **0**，clusterharness **8.168秒**／墙钟 **9.55秒**。等待最终指定HTTP整组结果后冻结。
- 最终指定链HTTP `go test ./internal/clusterharness -run 'Test.*(ForwardChain|ChainAuthorization|ChainFailover|ChainEntryGroup)' -count=1 -timeout=20m` 退出 **0**，clusterharness **60.039秒**／墙钟 **60.62秒**，包含强化后的实际应用完成屏障、新流立拒及1秒旧流终止，以及两总额度真实失败/恢复。
- **修复轮3已完成并冻结，等待原reviewer仅针对I5和本轮有界gap新增影响的scoped复审。** 状态保持claimed，未勾选审查完成项。本轮只改上述12份Go及本票Comments；原三包、最后panel补验、资源/事件队列/控制边界精准race、指定链HTTP全部通过，首次HTTP失败和诊断/夹具修正如实保留。12份文件gofmt无输出、git diff --check通过；仍为 `feat/ix` / `3bcd286d373d2330d388cf6adb419b388355a978`。未暂存或提交Go，外部07全部保护；未改公共wire/proto/授权实现、迁移、缓存格式、构建产物或发布政策，未生成辅助报告/日志或worktree。


### 修复轮 4：资源缺口的配置归属边界（进行中）

- 独立 scoped 复审确认 I1–I5 已闭环；本轮仅处理 I6：旧资源 gap 只存 reporter/reason/时间范围，投影读取时套 reporter 当前 snapshot 的全部链规则，retention 又只按节点存在保留，导致旧配置污染新配置及已解除缺口换代复活。
- 有效红：`go test ./internal/panel -run TestChainResourceGapConfigurationBoundary -count=1 -timeout=2m` 退出 **1**，panel **4.997 秒**。真实加密 FileEventQueue 写入资源事件→容量溢出→重开→Batch→Panel 接收后，地址、目标、链顺序、同节点新链规则、离线迟到、真实 B/C 成功解除后换代、相同指纹的新 generation 七格均在 `old gap polluted new authorization` 失败。首次夹具的链位置反转算式触发 CHECK，修为合法位置后重新取得上述全部有效红；夹具错误不计功能红。
- 最小结构修复：在 engine 候选丢失、node 包装超长、持久队列 overflow/expired 的实际丢失处，保留 `(rule_id, generation, fingerprint)`；每个 scope/reason 独立有界 `event_gap`，仅在已有 Event.Message 内编码，沿现有加密 events 文件、16条 Batch 与 ACK 交付。Panel 只用已捕获 scope 匹配当前授权，再用 reporter 当前可信后缀校验归属；不在接收时给旧 gap 补当前配置。恢复仍要求整个可信资源后缀在缺口和接收之后真实成功，retention 同样只保护当前授权的每 reporter/scope/reason 最新基准。
- 有界取舍：候选仍为 **4096条/4MiB**，消息仍 **2048B**；engine 与 queue 的精细 scope 各有 **4096条/2MiB** 独立总预算，按多条消息跨批次交付，不把单条2048B当全部scope容量。预算预留固定计数/时间元数据空间，使满额时已有 scope 仍能推进丢失范围；真正超精细预算的额外丢失合并为既有普通全局缺口，保留总计数/时间但丧失该额外部分的规则归属，不映射任意当前规则。既有精细 scope 不被清掉或以“最新两个”猜测覆盖。代价是额外有界内存/持久摘要预算和极端超预算后的精细归属降级。
- 回收时点：engine 仅在整个批次持久成功后回收交付快照，写失败还原 scope/计数，期间新候选仍保留；queue 的已冻结 scope 只在 ACK 后回收，给后来 scope 释放预算。Panel 旧授权 scope 不参与投影，历史保留期到达后可清理；当前 scope 跨重开和91天 retention 保留。普通全局缺口及无 scope 的旧式摘要只按全局事件展示，不创建当前链规则事故。没有改 wire/proto、迁移、配置缓存格式或授权流程。
- 首轮相关绿：`go test ./internal/forwarding ./internal/node ./internal/panel -run 'TestChain(Resource|Events)' -count=1 -timeout=3m` 退出 **0**，forwarding **1.333秒**、node **2.915秒**、panel **7.247秒**。新增 engine 两配置丢失/失败重试、scope count/bytes预算，node 300个scope跨多批持久重开/相同未ACK ID/ACK回收及超预算全局缺口计数测试；原候选/ACK/失败写盘边界保留。
- 事故补验：`go test ./internal/panel -run 'TestChainResource(PersistentBatch|GapConfiguration)' -count=1 -timeout=3m` 退出 **0**，panel **6.787秒**。地址/目标/链改变旧gap事故为「新版本替代」；已真实解除的旧事故保留「异常解除」且换代/重开不复活；同节点新规则不新建事故且原规则缺口仍在；离线旧gap与普通全局gap不创建新事故。新增重开计数断言最初混入后台正常产生的其他配置证据事故，已限定到本测试gap投影来源后通过，没有修改生产或放宽恢复条件。原当前scope的91天保留、重开、仅B不足/B+C成功才解除、旧expired晚到不掩盖新overflow继续通过。
- 正在最终精准 race、完整指定三包和指定链HTTP；仍 claimed、不提交，完成后冻结交回原 reviewer 仅审 I6 及本轮新增影响。

- 最终精准 race 首次 `go test -race ./internal/forwarding ./internal/node ./internal/panel -run 'Test(Chain(Resource|Events|Control|CachedReport)|EventQueue|RetentionStorage)' -count=1 -timeout=10m`：forwarding **2.855秒**、node **6.237秒**通过，panel **76.371秒**退出 **1**，无 race 报告；唯一失败为新增 `TestChainResourceGapConfigurationBoundary/new_rule` 在 race 下超过周期5秒，后台全系统投影结束了夹具手动创建的隔离来源，后续手动投影遂新建第二条。仅在该新夹具及其重开后停止周期后台并等待退出，保留真实投影、reconcile/save 和存储重开；同 regex 单独 panel race 复验退出 **0**，panel **77.008秒**。没有改生产逻辑或放宽恢复断言。
- 最终 SQL 自查补到普通 JSON 全局gap的 NULL 边界：包含规则字段但缺少精细scope标记时，不应获当前gap保留豁免。`go test ./internal/panel -run TestChainResourceGapConfigurationBoundary/unscoped_json -count=1 -timeout=1m` 有效红，退出 **1**、panel **1.814秒**，91天后仍保留1条；标记比较补 `COALESCE` 后 `go test -race ./internal/panel -run TestChainResourceGapConfigurationBoundary/unscoped_json -count=1 -timeout=2m` 退出 **0**，panel **8.383秒**。该普通全局摘要始终不映射链规则，修复补齐其正常历史清理。

- 最终完整三包 `go test ./internal/panel ./internal/forwarding ./internal/node -count=1 -timeout=15m` 退出 **0**：panel **77.824秒**、forwarding **15.868秒**、node **22.248秒**，均实际执行。覆盖公共事件队列/控制帧/未发送ACK拒绝、普通隧道、已有实际旧reader混合overlay及当前gap保留/恢复；未重跑独立旧二进制矩阵或全仓 make test。
- 最终指定链HTTP `go test ./internal/clusterharness -run 'Test.*(ForwardChain|ChainAuthorization|ChainFailover|ChainEntryGroup)' -count=1 -timeout=20m` 退出 **0**，clusterharness **59.312秒**，包含真实授权撤除应用屏障、立即拒新流/1秒旧流终止、资源拒绝/恢复和历史配置边界。
- 容量口径补充：2MiB 是精细scope消息及预留计数/时间元数据的预算，map键和容器结构开销另受4096条上限约束；普通候选4096条/4MiB、单条2048B、16条发送批次及 events 文件8MiB读上限均未放宽。精细scope可通过多个消息上送，超过预算的额外全局缺口仍有真实计数/时间；只有成功持久交付/ACK才能回收待交付scope，不按到达顺序判断当前配置。
- 本轮实际仅改 **10份 Go**：`internal/forwarding/chain_resource_events.go`、`engine.go`、`chain_relay_test.go`；`internal/node/chain_events.go`、`events.go`、`chain_events_test.go`；`internal/panel/chain_events.go`、`events.go`、`retention.go`、`chain_events_test.go`，以及本票 Comments。前轮 I1–I5 代码与验证保留；外部07前端、其他既有md/构建产物、proto、迁移、配置缓存和兼容性声明均未改。
- **修复轮4已完成并冻结，不提交，交回原reviewer仅复审 I6 及本轮新增影响。** 新旧配置/同节点无关规则/迟到/解除后换代不复活及事故收口有真实持久化回归；当前scope跨91天/重开、B-only不足/B+C恢复、两类reason独立、资源候选/ACK/失败重试与scope预算均通过。保留 claimed、不勾审查完成项；10份Go `gofmt -l` 无输出、`git diff --check` 通过，仍为 `feat/ix` / `3bcd286d373d2330d388cf6adb419b388355a978`。没有暂存/提交、创建工作树/分支/辅助报告/日志或执行对外动作。

### 最终独立复审与功能收口

- 原独立 reviewer 对修复轮4仅审 I6 及新增影响，正式结论：**I6 ADDRESSED；Spec符合；Quality Approved；新增 Critical 0 / Important 0 / Minor 0**。I1–I5 保持通过，未留下待修重要问题。确认真实丢失时配置scope、投影/恢复/retention统一边界、多批持久交付/ACK回收、超预算全局缺口及SQL NULL边界正确；新增夹具隔离后台保留真实投影与持久化，处理合理。
- 根编排者复核最终原票证据、工作区范围与 `git diff --check`（退出0），自动化验收全部满足，标记resolved。按数据面/节点观测与面板/真实集成分功能中文提交；所有.md仍不提交，最终统一文档收口。浏览器、实际专线/NAT及生产部署/回滚保持09人工待验，不混同本票自动化。

- 实际功能提交：**`919fdef`**「完善链式转发资源观测与可靠事件交付」（17文件）；**`ccecd76`**「接通链式授权就绪与逐跳运维事故闭环」（32文件）。两次暂存diffcheck与commit退出0，仅含06c源码/测试；.md和dist/nodebundle/bin均未纳入。提交后仅既有9份md未提交，进入07第二边界。
