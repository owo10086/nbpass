# 01 节点能力门禁与逐端口升级提示

Status: ready-for-agent
Blocked by: 无
Migration: 无（复用现有 `node_compatibility`，不占用 47）
Parallel: 本票先完成；02–06 消费本票接缝，不与本票同时修改能力协商和配置编译。

## 目标

为 IX 后续切片提供明确的能力判断、逐规则过滤和「需升级服务器」状态。旧节点继续接收能理解的完整快照；一条规则被拦截不能阻止该节点其他规则同步。依据 `../spec.md`、ADR 0009 和 ADR 0006，不通过软件版本字符串猜能力。

## 具体改动点

- 读取并复用 `internal/compatibility/compatibility.go` 的范围声明、`Compatibility.capabilities`、`internal/panel/version_compatibility.go` 的 `nodeCompatibility` 与 `node_compatibility(node_id,report)`。预留能力名 `ix_addresses`、`outbound_ipv4`、`forward_chains`；各功能票完成节点实际支持后才加入 `compatibility.Current()` 的声明，本票不能提前宣告支持未实现能力。
- 新建 `internal/panel/ix_capabilities.go`：从当前获认证会话对应的已保存声明读取有效能力，提供「节点 ID + 必需能力集合 → 缺失能力」接缝，供 02/03/04/06 的编译器和查询视图共同使用。无报告、未知能力、范围不兼容均不能证明支持。能力必须来自当前会话准入，探针、流量、事件及已退休实例不得改变它。
- 修正 `resolveCompatibility` 中 `legacy || ...` 的继承范围：无 Compatibility 的协议 1 节点只能通过既有显式 flag 获得旧能力，不能在将来自动继承 IX 能力。保留旧的 `tunnel_authorization_version`、`exit_failover_version`、`tunnel_rotation_version` 约束。
- `internal/panel/control.go:admitControl` 当前先执行授权准入/编译、后保存兼容报告。调整事务内顺序：先保存本次协商信息，再根据能力变化清除相关确认、重编译；提交后通知受影响节点。升级、降级、同进程重新建立会话都不能使用旧报告生成新快照。
- 在 `config_snapshots.go` / `tunnel_snapshots.go` 接入逐规则能力过滤接缝；后续票提供各功能的具体需求。过滤不仅涉及入口 `Rules`，也涉及相关 `ExitGroups`、中间/末跳 grants，不能留下失去规则归属的秘密。现有授权确认门禁继续在能力门禁之后执行，缺能力时绝不退回直连或旧语义。
- `config_sync.go` 的规则视图新增 `configuration_blockers: [{node_id,node_name,capability,reason}]`，由同一需求计算接缝派生，不建重复状态表。`sync_status` / 相关 `sync_members[].sync_status` 可为 `需升级服务器`，原因指出具体节点与能力；为 `aggregateSync` 增加优先级，不能将被过滤的启用规则误报「正常」。离线待确认与能力不足同时存在时保留结构化 blocker，不能丢原因。停用规则仍按撤销确认判断。
- `web/src/ConfigSync.tsx`、`PortSyncDetails.tsx`、`RulesData.ts` 只增加 blocker 展示与升级提示；现有 `SyncTag`、端口展开行能显示具体缺能力服务器，不用服务器汇总错误代替逐端口状态。

## 迁移与兼容要求

- 不新增数据库迁移、NodeReport 布尔开关或软件版本下限；复用已持久化的 Compatibility。保持旧协议 1 与当前协议 2、原有缓存和已运行端口的兼容行为。
- 能力按实际执行语义判断：地址选择/域名/对外端口由拨下一跳的节点消费；出站 IP 由连接最终目标的节点消费；链语义由入口和每跳节点消费。新字段不得进入不支持它的节点快照，某节点仅拥有未被选用的专线地址不应影响其普通规则。
- 旧节点离线缓存遵循 ADR 0006；面板过滤表示新期望撤销，不能声称离线撤销已经完成。降级清除授权确认，重新升级须重新确认，不复用旧代次的通过结果。

## 测试要求

- 单元：`version_compatibility_test.go` 覆盖协议 1 无声明不继承新能力、显式缺失、未知名称、同名软件版本不同能力、声明变化后先保存再编译；`config_sync` 测试逐规则 blocker、停用/删除撤销和汇总优先级。
- clusterharness：沿用 `version_compatibility_test.go`、`TransformControlReports`（真实节点运行时，仅改线上声明），验证原有逐流授权/多成员门禁仍生效、同一旧节点的普通直连不受被拦隧道影响、声明在重连/面板重启后正确更新。使用现有 `BeforeControlSend` 只读观察真实出站 PanelMessage，后续 02/03/04/06 在此接缝断言旧节点未收到新字段、专线组或新 grants；不能只以未转发成功推断未下发。本票不能用虚构 IX 字段代替运行场景。
- 定向运行 `go test ./internal/compatibility ./internal/panel ./internal/node -timeout=10m`、`go test ./internal/clusterharness -run 'Test.*(Compatibility|Capabilities|UnsupportedTunnel)' -timeout=10m`；前端提示相关测试和 `npm --prefix web run build`。

## 验收标准

- [ ] 后续票可以复用单一能力判断和 blocker 形状；旧节点不会凭 legacy 分支获得新能力。
- [ ] 当前会话的能力变化能触发正确重编译/撤销，旧连接补报不能覆盖。
- [ ] 被能力门禁过滤的规则指出需升级的服务器；同节点其他规则仍可正常应用。
- [ ] 本票不提前发布任何尚未实现的 IX 能力。

## 不做的事

不实现专线地址、出站源绑定或多跳数据面；不改变节点升级流程、离线授权期限、控制协议主版本；不构建或提交发行产物，不改已定产品决策。
