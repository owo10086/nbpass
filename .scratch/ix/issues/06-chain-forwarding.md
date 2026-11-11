# 06 链式规则、多跳数据面、逐跳授权与健康主备

Status: ready-for-agent
Blocked by: 04, 05
Migration: 51（`chainRulesMigration`）
Parallel: 可与 07 并行，本票只改后端/proto/节点/测试，07 独占链管理前端；不得改 07 的 web 文件。共享端口 UI 留给 08。

## 目标

端口通过 API 引用已保存链，真实路径为入口→第 1 组→第 2 组→…→最终目标；每节点只连接下一跳，逐跳认证与逐规则目标授权，任何一组主备切换均不跳过必经跳。此票把协议、编译器和数据面放在同一垂直切片，不能以仅保存链字段作为完成。

## 具体改动点

### 规则、数据库及 HTTP

- 迁移 51 在新 `chain_rules_migration.go` 定义：`forward_rules.chain_id TEXT NULL REFERENCES forward_chains(id)`，默认 NULL；旧 `exit_group_id` 不变。用 INSERT/UPDATE 约束触发器保证直连两者均空、隧道恰好选一个非空引用。避免重建 forward_rules 触发六张流量/授权子表的级联清除；若确须重建，按 `single_entry_migration.go` 保留并回填全部子表，必须有迁移证据。
- `forwarding.Rule` 增加 `ChainID` / `json:"chain_id,omitempty"`；API 仍用 `kind=direct|tunnel`，隧道通过 exit_group_id 与 chain_id 二选一表达单跳/链式。`Rule.Validate/Config`、`forwarding_api.go:ruleColumns/scanRule/saveRule`、`rules_json.go:storeRule/preflightRuleImport`、`rules_batch.go`、随机端口和引用查询一起接入。直连不可携带任一引用，单跳不带 chain_id，链式不带 exit_group_id。
- 导出规则文件升到格式 3（增加 chain_id），导入保留格式 1/2；旧格式不能夹带链引用。预检、正式导入、普通保存及组/链编辑使用同一全量校验，错误整批回滚；停用规则保留引用并阻止删除链。
- `DELETE /api/forward-chains/{id}` 有任意规则引用时 409；链 PUT 和组成员 PUT 要用所有引用规则的实际入口集合 `RuleEntryNodes` 校验：入口任一成员不能出现在任一跳的任一候选中，各跳候选节点不能重复。入口组加成员、规则移入口/切模式、导入、链重排、端点组改成员都不可绕过。既有端口占用、目标指回规则的静态环路检测继续执行。
- 规则 GET 增加 `path`，复用 05 的 hops 形状，另包含实际入口和最终目标；链 GET 填真实 rule_count 和各跳 observations。路径展示定义路径与全部主备候选，不能把未观测状态当实际选中路径。`node_references.go`、`rule_entries.go` 的出口轮换参与者解析要覆盖间接链引用。

### 编译、协议和确认

- `tunnel_snapshots.go` / 新 `chain_snapshots.go` 从路径展开每条相邻边：入口集合×第一组、前一组全部成员×后一组全部成员。只为相邻服务器对调用现有 `pairCredential`；中间节点得到自身 TunnelServer 的上游 peers/grants 和下一组端点，不得到无关跳私钥、上游入口代拨目标的权力或完整任意路径。
- protobuf 保留原字段号并追加：`ForwardRule.string chain_id=285`、`ForwardRule.uint32 hop_index=286`；`TunnelGrant.string next_exit_group_id=285`、`uint32 hop_index=286`、`repeated string authorized_next_node_ids=287`；`NodeReport.repeated TunnelGrantApplyResult tunnel_grant_results=285`。新消息 `TunnelGrantApplyResult` 字段为 `entry_id=1,rule_id=2,generation=3,applied_revision=4,apply_error=5`。运行 `make proto` 提交生成代码。
- 保存链的跳编号从 1 起，入口本地规则 hop_index=0；接收节点 grant.hop_index=i，下一跳为 i+1，末跳 next_exit_group_id 为空。节点的 ForwardRule.exit_group_id 只作为本节点下一组的运行引用（入口为第 1 组）；链管理 ID 与 hop_index 绑定该规则代次，不能让客户端自由指定下一组。单跳沿用原 wire 路径，空 chain_id 不启用新语义。
- 扩展 `tunnel_authorization.go`：授权指纹覆盖有序路径、每跳全部成员/地址选择与最终目标，不再只排序一个出口集合；代次仍按 rule_id 持久递增。现有 `tunnel_authorization_confirmations(exit_id,entry_id,rule_id,generation)` 可表示每条相邻边，无需把最初入口伪装成所有边的 entry_id。
- 迁移 51 同时增加 `node_tunnel_grant_results(node_id,entry_id,rule_id,generation,applied_revision,apply_error)`，外键及清理随节点/规则；主键 `(node_id,entry_id,rule_id)`。逐 grant 结果绑定当前尝试 revision/digest 和撤销检查点，在 `config_results.go` / 新 grant 报告处理文件验证完整名单、重复/伪造/旧连接/旧代次。与入口 RuleResults 分开存放，不能要求中间节点伪造监听端口规则报告。
- 从末跳向前逐层确认可用候选：只确认已持久应用的本地 grant 与相应下一跳授权；中间节点的 grants 可先安装但其转发状态未就绪时不能放行上游入口。最终至少存在一条逐边已确认路径才下发入口监听。其他端口局部失败不得永久卡死整条链的确认；相同内容重编译必须稳定，避免授权确认递归推进 revision 的循环。
- `control.go`、`config_sync.go`、节点 `node.go/config_report.go` 报送/接收逐 grant 状态与错误，控制消息仍保持 64 KiB、快照 60 KiB 上限，保存超限整笔拒绝；错误有界。01 的 blocker 指出缺能力的具体路径成员，旧节点绝不接收链字段或 relay grants。具备 capability 与授权/主备基础能力后才能执行链；完成实际支持后声明 forward_chains。

### 节点转发、健康与缓存

- `forwarding/tunnel.go` 的逐流请求扩展 chain_id/hop_index，并在 `tunnel_authorization.go` 核对认证会话的真实上一跳、规则 ID、代次、链/跳和授权目标；每次从本地 grant 选择下一组，末跳才调用 `dialTargetWithin`。拒绝跳过跳数、伪造前驱/下一组/目标、错链错代次；不能信任入口提交的一串自由拨号地址。
- `tunnel_limits.go:serveLimitedStream` 分离「转交下一组」和「拨最终目标」；两者均沿用资源限额、有界预读、取消、半关闭、FIN/RST 回收和每秒复查授权。上游断开/撤权须取消下游拨号与流；路径修改、删除、停用切断在线节点的旧授权流，控制离线不设到期。
- `engine.go`、`state.go`、`tunnel_snapshot.go` 使服务器即使没有任何入口监听规则，也能建立/保存下一跳客户端与转发 grants。当前 `State()` 只从监听规则收集 ExitGroups，必须改为保留 relay 所需的实际组；撤销检查点与恢复不能丢失中间跳语义而直接拨最终目标。复核 04 的源 IP，只允许末跳和直连拨目标绑定。
- `exit_failover.go/exit_health.go` 每个发送节点仅探测并选择下一组，复用 03 的地址键；保留现有约 5 秒探测、连续 3 次链路失败、连续成功 60 秒恢复、每次有界候选与总拨号预算。目标失败、下游路径失败、本地资源和配置错误须保留归属，不能把 C→目标失败计成 A→B 的链路故障。全部候选失败就失败，不直连、不跳组、不使用全局智能选择。
- `panel/exit_health.go`、05 的链查询聚合真实逐边观测，附发送者、接收者、地址与新鲜度；链各跳没有新鲜证据时为未知/历史，不能用控制在线替代健康。`exit_blocked.go` 的公网换 IP 确认和 `tunnel_rotation.go` 的轮换名单覆盖所有直接前驱（含中间节点）、同节点全部地址及离线前驱；保留旧材料直到当前参与者确认。
- `readiness.go`、`upgrade_check.go`、面板入口就绪把完整获授权的后续路径纳入判断；只完成 A→B 握手不能宣告链就绪，探测不拨最终目标。`traffic.go` 的规则累计仍只接收原实际入口来源，禁止每经过一跳重复计账；各节点资源连接数按本机真实承载统计。

## 迁移与兼容要求

- 本票独占 51，在 50 后追加；旧链引用均 NULL，原单跳/直连运行与 API 输入保持兼容。已有单跳零代次兼容不得用于新链。
- ADR 0006 全面适用：高水位、摘要、磁盘撤销检查点、旧连接隔离、离线无限期缓存转发、上线完整快照收敛。新配置只发具备能力的执行节点，能力不足的候选不获新授权；保留组的原始顺序并向用户显示阻塞原因，不能通过过滤把下一组直接越过去。
- 当前 `compatibility` 的隧道格式/持久写格式均为 1；任何扩展必须验证旧节点的明确拒绝或门禁行为，以及新节点读取旧缓存。不能只因 protobuf 忽略未知字段就认定业务兼容，不能擅自提高写格式破坏既定回滚窗口；需要新格式时须先满足既有读写范围约束并用测试证明，未满足不能交付。
- 旧代码的 `TestCombinedEntryExitCountsBusinessOnce` 允许同服务器兼任一条路径入口/出口，与本 spec 的完整路径不重复要求相反：将管理 API 用例改为保存拒绝，保留不相关计数回归；不为了保旧测试放宽 spec。只读清点 46 库是否存在此类历史规则；若存在，「旧规则行为不变」与新全路径校验有实际兼容冲突，必须向编排者明确列出，不能自行删除/停用历史规则、绕过校验或改 ADR 决策。

## 测试要求

- 单元：规则引用 XOR、格式 1/2/3、51 迁移保留全部流量/授权依赖数据；入口及备用节点防环、每个变更入口的回滚；相邻边编译、最小秘密、指纹顺序、逐 grant 确认和稳定重编译、旧报告/重复报告拒绝、60/64 KiB 边界。
- 单元数据面：三跳及只有 relay 的节点 State/Restore；错误链/跳/前驱/代次/目标拒绝，授权复查与撤销取消；目标/下一跳故障归属；资源满、黑洞、RST、半关闭、慢读写、源 IP 和流量不重复。
- clusterharness 新建 `forward_chains_test.go`、`chain_authorization_test.go`、`chain_failover_test.go`，通过 HTTP 建资源、真实 WSS/yamux/TCP 传字节：A→B→C→目标，并再加一跳证明没有写死两组。A、B 禁止直拨目标且只能连接各自下一组；末跳独占目标解析，含中间/末跳的域名专线与对外端口映射。
- 场景：第一组 B1/B2 与后组 C1/C2 分别故障、恢复、全坏，正确在本跳切换且不跳组；目标故障不污染前一边健康。中间节点断开控制后仍用缓存，节点重启仍保留完整路径；在线下一跳撤销后拦住离线上一跳的新流并结束旧流；链编辑影响所有引用端口、改直连/单跳/入口后旧授权收敛，保留其他端口。
- 场景：上游伪造目标/下一跳/跳数/原始入口身份被拒；末跳写盘失败不启用入口；逐跳证书/凭据轮换等待离线前驱；B/C 换公网 IP 不改专线；末跳出站 IP 生效和非本机报错；每一种旧节点位置和同节点无关端口隔离；导入整批回滚、被引用删除拒绝、备用成员重复拒绝、规则流量不按跳数放大。
- 运行 `go test ./internal/panel ./internal/forwarding ./internal/node -timeout=15m`、`go test ./internal/clusterharness -run 'Test.*(ForwardChain|ChainAuthorization|ChainFailover)' -timeout=20m`；针对并发 relay/轮换/撤销运行相应 `go test -race`。全量在 09 执行。

## 验收标准

- [ ] 真实多跳路径、逐跳域名解析/地址选择、末跳出站源绑定均可验证；每一组必经。
- [ ] 每条边只接受上一跳并只转下一组或授权最终目标，篡改和旧授权不能放行。
- [ ] 每一跳独立健康与主备恢复；逐 grant 和入口同步状态正确，局部错误不阻断无关端口。
- [ ] 完整路径全候选防环、引用保护、批量导入、旧节点门禁全部覆盖。
- [ ] 离线缓存、重启、撤销、轮换、流量和资源限制均无单跳回归；API 已可被 08 消费。

## 不做的事

不改端口/链管理前端，不做 UDP、IPv6、智能路由、固定前后跳、默认传输、管理中转、自动绕过某跳或无网机器安装；不生成新的 ADR 决策或提交构建产物。
