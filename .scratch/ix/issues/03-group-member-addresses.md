# 03 端点组成员选择服务器地址与按地址隔离健康

Status: ready-for-agent
Blocked by: 02
Migration: 48（`ixMemberAddressesMigration`）
Parallel: 完成本票后 04 与 05 可分流；本票独占端点组 API、成员 UI、端点客户端键及地址健康协议。

## 目标

端点组成员成为「服务器 + 使用的地址」。同一组同一服务器只出现一次，不同组可分别使用该服务器的公网或不同专线，拨号、健康和地址确认不能串用。

## 具体改动点

- 新建 `internal/panel/ix_member_addresses_migration.go`；迁移 48 给 `exit_group_members` 增加 `address_id TEXT NOT NULL DEFAULT 'public'`。保留现有 `(group_id,node_id)` 主键、`(group_id,position)` 唯一及原顺序；用校验/触发器约束非 public 地址属于同一 node_id，禁止删除被引用专线。不能用地址 ID 替换 node_id 导致同服务器重复入组。
- `tunnel_api.go:exitGroupView/loadExitGroups/saveExitGroup`：新增 `member_addresses: [{node_id,address_id}]` 有序数组。为现有客户端保留 `members: string[]` 投影；只传旧 members 表示这些成员全部使用公网，两个数组同时提供须长度、节点及顺序一致，否则 400。GET 两者始终一致；新 UI 使用 member_addresses 作为事实来源。保留 PrimaryBackup，仅按数组顺序选择。
- `tunnel_snapshots.go` 按成员选中的地址生成端点，`address_id='public'` 读取公网，其余读取对应专线；有效端口遵循 02，凭据和证书仍按服务器节点对共享。修改专线/监听端口/组成员时重编译所有受影响规则；引用不存在或不属于服务器的地址必须整笔回滚。
- 在 `api/control/v1/control.proto` 给 `TunnelEndpoint` 增加 `string address_id = 280`，给 `ExitHealth` 增加同名字段 280；旧字段号不改。旧公网配置可省略 address_id，节点统一把空值解释为 public。运行 `make proto`，提交 `internal/controlpb/control.pb.go`（若生成器改变 gRPC 文件也一并核对），不手改生成文件。
- `internal/forwarding/engine.go:tunnelEndpoints/applyTunnelClients/selectTunnel`、`tunnel_snapshot.go`、`exit_failover.go`、`exit_health.go`、`tunnel_rotation.go`、`readiness.go`：客户端/会话/健康键改为 `(node_id,address_id)`；同一地址重复出现必须配置一致，不同地址允许并存。组内仍按 node_id 去重；`AuthorizedExitIds` 继续表示节点授权，不把地址 ID 混入认证身份。轮换时更新同节点的全部地址客户端，旧业务载体的退役、资源预算和恢复观察不能丢失。
- `internal/panel/exit_health.go` 的上报去重与合法性检查按 `(exit_id,address_id)`，除节点对凭据外核对当前下发的地址引用，不能凭历史 tunnel_pairs 接受任意地址证据；API 保留 exit_id 并输出 address_id。公网换 IP 进度 `exit_blocked.go`、`internal/node/exit_address.go` 按地址标识匹配，专线端点不携带公网 address_version，不等待专线拨号者确认公网换 IP。
- `web/src/EndpointData.ts`、`EndpointMembers.tsx`、`ExitGroups.tsx`、`Endpoints.tsx`、`EndpointHealth.tsx`：选项显示「服务器 · 公网/专线名称」和有效地址端口；选中同服务器另一地址时要求替换现有选择，不添加第二个服务器成员。保持排序与编辑回填；归属搜索不再假定所有成员只有节点 ID。健康表区分地址并保留 10 秒新鲜度语义。
- 选用专线地址要求拨号节点的 `ix_addresses`；纯公网、无新语义路径保留旧快照。完整地址 ID 不得发给旧节点让其忽略，纯公网可编译为原形状。

## 迁移与兼容要求

- 本票独占 48，只能注册在 47 后。旧组成员全部迁移 public，成员及主备顺序不变；绝不能把原手填地址挪走或自动纠正。
- 被组引用的专线即使关联规则停用也不能删除，409 指明组；更换组地址与删除专线分两次合法操作，不做自动替换。API 的旧 members 输入保持可用且语义固定为公网。
- 新增字段进入确定性摘要、节点 State 与缓存恢复。保留节点对凭据、授权代次和逐规则撤销，地址变化不重新生成服务器证书。

## 测试要求

- 单元：迁移 47→48，旧成员顺序/默认公网、跨节点地址 ID、双数组不一致、同节点两地址入同组、被引用删除、事务失败不推进 revision；同节点两地址跨组快照通过、同一地址冲突拒绝、健康/轮换/Restore 不串用。
- clusterharness 新建 `ix_member_addresses_test.go`：A 的两个端口分别经两组连 B 的公网和专线（含域名 + 真实端口映射），记录实际拨号地址并传真实业务字节；阻断公网只影响公网路径，专线保持可用。B 换公网 IP 后专线规则地址、会话和健康不被替换；公网确认进度只列公网消费者；面板/节点重启恢复地址选择。旧 A 上的专线规则被拦，纯公网规则正常。
- 回归 `tunnel_rotation_test.go`、`exit_health_test.go`、`exit_blocked_test.go`；运行 `go test ./internal/panel ./internal/forwarding ./internal/node -timeout=10m` 和 IX/隧道轮换相关 clusterharness 定向测试。前端执行 `npm --prefix web test -- src/EndpointData.test.mjs`、新增成员数据测试与构建。

## 验收标准

- [ ] 组内同服务器唯一，跨组可选不同地址，UI 与 API 往返保留地址和顺序。
- [ ] 两地址的连接、健康、失败和恢复相互隔离，证书/凭据仍属于同一服务器身份。
- [ ] 专线引用有删除保护，公网换 IP 不覆盖或等待专线路径。
- [ ] 旧库、旧 members 客户端、旧公网节点保持兼容。

## 不做的事

不实现链或组间 fallback、最小连接数策略、自动选地址；不把一台服务器的多个地址当作同组多个主备成员；不提交发行产物。
