# 06c 逐跳授权确认、健康就绪与事故集成

Status: ready-for-agent
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

- [ ] 真实多跳、逐 grant 确认、地址/后续路径就绪及局部失败隔离完整，不把握手当整链就绪。
- [ ] operations/incidents 能按每跳节点关联受影响端口，逐边/grant/blocker 原因和配置历史边界准确。
- [ ] 主备/全部发布入口组矩阵通过；DNS/换 IP 归属正确，外部动作仍由 ADR 0005 协调者执行。
- [ ] 旧节点收包过滤、备用升级重新确认、离线缓存真实旧读取器/回滚矩阵、撤权/轮换/计量回归通过。
- [ ] 06a/06b 与本票验收全部满足后，才统一宣告 forward_chains；API 可交 07 集成及 08 消费。

## 不做的事

不改 07/08 前端，不从观察投影新增动作协调者，不改变 A 或另升缓存格式，不做 UDP/IPv6/智能绕路/管理中转，不提交构建产物。
