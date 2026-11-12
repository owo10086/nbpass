# 01 节点能力门禁与逐端口升级提示

Status: ready-for-agent
Blocked by: 无（spec 已定决策 7：按 A 实施）
Migration: 无（复用现有 `node_compatibility`，不占用 47）
Parallel: 先完成本票；02–06c 消费本票接缝，不与本票同时修改能力协商和配置编译。

## 目标

为 IX 后续切片提供明确的能力判断、逐规则过滤和「需升级服务器」状态。旧节点继续接收能理解的完整快照；一条规则被拦截不能阻止该节点其他规则同步。依据 `../spec.md`、ADR 0009 和 ADR 0006，不通过软件版本字符串猜能力。

## 具体改动点

- 读取并复用 `internal/compatibility/compatibility.go` 的范围声明、`Compatibility.capabilities`、`internal/panel/version_compatibility.go` 的 `nodeCompatibility` 与 `node_compatibility(node_id,report)`。分别预留 `ix_address_dialing`（02 域名/对外端口映射拨号）、`ix_member_identity`（03 成员地址身份及多地址会话/健康隔离）、`outbound_ipv4`（04）、`forward_chains`（06c）；不再使用笼统的 `ix_addresses`。对应票完成实际支持和验收后才声明；06a/06b 不提前宣告多跳能力。
- 新建 `internal/panel/ix_capabilities.go`：从当前获认证会话对应的已保存声明读取有效能力，提供「节点 ID + 必需能力集合 → 缺失能力」接缝，供 02/03/04/06a/06c 的编译器和查询视图共同使用。无报告、未知能力、范围不兼容均不能证明支持。能力必须来自当前会话准入，探针、流量、事件及已退休实例不得改变它。
- 修正 `resolveCompatibility` 中 `legacy || ...` 的继承范围：无 Compatibility 的协议 1 节点只能通过既有显式 flag 获得旧能力，不能在将来自动继承 IX 能力。保留旧的 `tunnel_authorization_version`、`exit_failover_version`、`tunnel_rotation_version` 约束。
- `internal/panel/control.go:admitControl` 当前先执行授权准入/编译、后保存兼容报告。调整事务内顺序：先保存本次协商信息，再根据能力变化清除相关确认、重编译；提交后通知受影响节点。升级、降级、同进程重新建立会话都不能使用旧报告生成新快照。
- 在 `config_snapshots.go` / `tunnel_snapshots.go` 接入逐规则能力过滤接缝；后续票提供各功能的具体需求。过滤不仅涉及入口 `Rules`，也涉及相关 `ExitGroups`、中间/末跳 grants，不能留下失去规则归属的秘密。现有授权确认门禁继续在能力门禁之后执行，缺能力时绝不退回直连或旧语义。
- `config_sync.go` 的规则视图新增 `configuration_blockers: [{node_id,node_name,address_id,capability,reason}]`，由同一需求计算接缝派生，不建重复状态表。能力不足导致没有完整可用路径时 `sync_status` / 对应入口成员状态为「整条阻塞（需升级服务器）」；已有完整可用路径时为「可运行但备用成员能力不足」，保留具体 blocker 与候选原顺序，不能把备用排除误报为整条阻塞。`aggregateSync` 同时保留可运行状态与备用缺失；离线待确认不能覆盖能力原因。停用规则仍按撤销确认判断。
- `web/src/ConfigSync.tsx`、`PortSyncDetails.tsx`、`RulesData.ts` 只增加 blocker 展示与升级提示；现有 `SyncTag`、端口展开行能显示具体缺能力服务器，不用服务器汇总错误代替逐端口状态。

## 迁移与兼容要求

- 不新增数据库迁移、NodeReport 布尔开关或软件版本下限；复用已持久化的 Compatibility。保持旧协议 1 与当前协议 2 的普通规则兼容；按已定决策 7（A）将 `StateCurrent=2`、`Current().StateMin=1,StateMax=2,StateWrite=2`，替换兼容包中要求保持写格式 1 的旧注释。版本号属于缓存格式，不是迁移号或软件版本；本次只分配 2，04/06a–06c 不再另升。
- 能力按实际执行语义判断：域名/对外端口拨号要求拨号者的 `ix_address_dialing`；专线成员身份、多地址会话/健康隔离要求消费该语义节点的 `ix_member_identity`，同时用到域名/映射则两者都要；仅公网域名/映射不要求后者。出站 IP 由最终目标拨号者消费；链语义由入口和每跳消费。仅登记未选用的专线不影响普通规则。能力不足候选不获新授权，备用升级后清除旧确认并持久应用、重新确认，再按原顺序加入候选。
- 旧节点离线缓存遵循 ADR 0006；面板过滤表示新期望撤销，不能声称离线撤销已经完成。降级清除授权确认，重新升级须重新确认，不复用旧代次的通过结果。
- `internal/node/config_store.go` 新版读取器显式分派格式 1/2、拒绝未知格式并保留身份校验；格式 1 缺新字段按旧语义转换，保留高水位/实际结果/撤销检查点。`node.go` 的 Persist 对所有状态和撤销检查点写外层整数 `version: 2`；沿用同路径 `config.enc`、AEAD 附加数据 `nbpass-node-config-v1` 和原子写，不双写/回退格式 1。真实旧代码严格比较 `Version != StateCurrent(1)`，因此 2 在 Restore 前确定报错，未知 JSON 字段与 Restore 重算摘要不再构成绕过。该判断必须由真实旧读取代码验证，不能用新读取器改声明模拟。
- 旧 `node.go:242` 返回缓存错误并关闭 engine，控制会话尚未开始，不能承诺自动重连。按 spec 的人工回滚流程停节点/更新器、原样隔离不兼容缓存，保留凭据/密钥/撤销证据，以空状态重启；断网不转发，重连只收当前能力内快照。不把普通缓存损坏/写盘失败自动当空缓存，不修改 version 或恢复更早缓存。旧节点上报格式 1 不应被误作控制会话不兼容而拒绝全部普通配置。
- `CompatibleState` 与旧独立更新器的候选检查仍拒绝跨格式在线切换；新面板不能声称旧格式节点可直接在线升级到 2。不放宽检查伪造双向可读窗口，09 通过现有同身份重新安装流程替换节点与更新器，完整 IX 一次发布；中间票构建不生产发布。新版读 1、写 2 与旧版读 2 拒绝是单向升级合约。

## 测试要求

- 单元：`version_compatibility_test.go` 覆盖协议 1 无声明不继承新能力、显式缺失、未知名称、同名软件版本不同能力、声明变化后先保存再编译；`config_sync` 测试逐规则 blocker、停用/删除撤销和汇总优先级。
- 公共兼容验收合约：用真实 02 版节点二进制（或由该版真实源码构建的节点）验证「02 版节点 × 03 版面板」，仅改新版声明不能代替此版本组合：仅声明拨号能力时纯公网域名/映射仍可用，专线成员/多地址隔离要求成员身份能力；升级后须重新确认才能成为候选。分别断言整条阻塞与仅备用不足，不能只断言最终流量失败。
- 公共缓存验收合约（04、06a/06b 落地场景）：记录实际生产回滚目标的旧源码提交/二进制 SHA256，用其真实读取代码/旧二进制读取新版实际写出的格式 2 缓存；断开控制，覆盖 relay-only、直连/末跳源 IP、撤销检查点。断言明确拒绝且无业务监听/下一跳/目标连接；人工隔离后断网空启动仍不转发，重连只收能力内当前配置，无越跳、无丢源绑定后转发、无授权复活。新版读 1/2、未知格式拒绝、撤销检查点写 2 与原子写失败保守停止也须通过；实际旧二进制无拒绝保证即阻断发布。
- 本票先验证当前真实旧 `config_store.go` 对格式 2 的拒绝、新读写范围与旧 updater 跨格式拒绝；04 扩源 IP，06b 扩 relay/撤销，06c 回归全链，09 验同身份跨格式重新安装与人工回滚。现有 `TestConfigCacheRejectsUnknownFormatAndDifferentIdentity` 已有旧版拒绝 version 2 用例，须保留真实旧读取基线；更新新版测试不能丢此证据。
- clusterharness：沿用 `version_compatibility_test.go`、`TransformControlReports`（真实节点运行时，仅改线上声明），验证原有逐流授权/多成员门禁仍生效、同一旧节点的普通直连不受被拦隧道影响、声明在重连/面板重启后正确更新。使用现有 `BeforeControlSend` 只读观察真实出站 PanelMessage，后续 02/03/04/06c 在此接缝断言旧节点未收到新字段、专线组或新 grants；不能只以未转发成功推断未下发。本票不能用虚构 IX 字段代替运行场景。
- 定向运行 `go test ./internal/compatibility ./internal/panel ./internal/node -timeout=10m`、`go test ./internal/clusterharness -run 'Test.*(Compatibility|Capabilities|UnsupportedTunnel)' -timeout=10m`；前端提示相关测试和 `npm --prefix web run build`。

## 验收标准

- [ ] 后续票可以复用单一能力判断和 blocker 形状；旧节点不会凭 legacy 分支获得新能力。
- [ ] 当前会话的能力变化能触发正确重编译/撤销，旧连接补报不能覆盖。
- [ ] 被能力门禁过滤的规则指出需升级的服务器；同节点其他规则仍可正常应用。
- [ ] 本票不提前发布任何尚未实现的 IX 能力。
- [ ] 两种地址能力独立判断；备用不足保留可运行状态与 blocker，备用升级重新确认。
- [ ] A 的读 1/2、写 2 已落地，真实旧格式 1 读取器在 Restore 前拒绝 2；公共矩阵与人工恢复/重新安装交接明确，能力门禁不被当作离线回滚保护。

## 不做的事

不实现专线地址、出站源绑定或多跳数据面；不改变 A、不放宽跨格式在线更新检查、不改变离线授权期限或控制协议主版本；不构建或提交发行产物，不改已定产品决策。
