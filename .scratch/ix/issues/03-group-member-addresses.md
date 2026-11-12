# 03 端点组成员选择服务器地址与按地址隔离健康

Status: resolved
Blocked by: 02
Migration: 48（`ixMemberAddressesMigration`）
Parallel: 完成本票后 04 与 05 可分流；本票独占端点组 API、成员 UI、端点客户端键及地址健康协议。

## 目标

端点组成员成为「服务器 + 使用的地址」。同一组同一服务器只出现一次，不同组可分别使用该服务器的公网或不同专线，拨号、健康和地址确认不能串用。

## 具体改动点

- 新建 `internal/panel/ix_member_addresses_migration.go`；迁移 48 给 `exit_group_members` 增加 `address_id TEXT NOT NULL DEFAULT 'public'`。保留现有 `(group_id,node_id)` 主键、`(group_id,position)` 唯一及原顺序；用校验/触发器约束非 public 地址属于同一 node_id，禁止删除被引用专线。不能用地址 ID 替换 node_id 导致同服务器重复入组。
- `tunnel_api.go:exitGroupView/loadExitGroups/saveExitGroup`：新增 `member_addresses: [{node_id,address_id}]` 有序数组。为现有客户端保留 `members: string[]` 投影；只传旧 members 表示这些成员全部使用公网，两个数组同时提供须长度、节点及顺序一致，否则 400。GET 两者始终一致；新 UI 使用 member_addresses 作为事实来源。保留 PrimaryBackup，仅按数组顺序选择。
- `tunnel_snapshots.go` 按成员选中的地址生成端点，`address_id='public'` 读取公网，其余读取对应专线；有效端口遵循 02，凭据和证书仍按服务器节点对共享。修改专线/监听端口/组成员时重编译所有受影响规则；引用不存在或不属于服务器的地址必须整笔回滚。
- 在 `api/control/v1/control.proto` 给 `TunnelEndpoint` 增加 `string address_id = 280`，给 `ExitHealth` 增加同名字段 280；复用 02 的 `endpoint_config_digest = 281` 并把 address_id 纳入摘要，旧字段号不改。旧公网配置可省略 address_id，节点统一把空值解释为 public。运行 `make proto`，提交 `internal/controlpb/control.pb.go`（若生成器改变 gRPC 文件也一并核对），不手改生成文件。
- `internal/forwarding/engine.go:tunnelEndpoints/applyTunnelClients/selectTunnel`、`tunnel_snapshot.go`、`exit_failover.go`、`exit_health.go`、`tunnel_rotation.go`、`readiness.go`：客户端/会话/健康键改为 `(node_id,address_id)`；同一地址重复出现必须配置一致，不同地址允许并存。组内仍按 node_id 去重；`AuthorizedExitIds` 继续表示节点授权，不把地址 ID 混入认证身份。轮换时更新同节点的全部地址客户端，旧业务载体的退役、资源预算和恢复观察不能丢失。
- `internal/panel/exit_health.go` 的去重与合法性检查按 `(exit_id,address_id)`，除节点对凭据外核对当前下发的引用与 `endpoint_config_digest`；摘要覆盖身份、地址、有效拨号端口、传输与 TLS 身份，地址或端口修改即失效旧证据。同 address_id 的迟到旧样本丢弃，不能按接收时间刷新为有效；不能凭历史 tunnel_pairs 接受证据。API 输出 address_id/摘要。公网换 IP 进度 `exit_blocked.go`、`internal/node/exit_address.go` 按地址标识匹配，专线不携带公网 address_version，不等待专线拨号者确认。
- `internal/panel/entry_readiness.go:309/319` 的 health 键改为 `(exit_id,address_id,endpoint_config_digest)`，tunnelReady 按该规则选中的组端点和已确认授权匹配，不能把同服务器公网成功用于专线规则。保留现有进程/会话/接收新鲜度及探测成功时效门槛，两地址健康互不替代。06c 在此基础上接入后续路径就绪。
- `web/src/EndpointData.ts`、`EndpointMembers.tsx`、`ExitGroups.tsx`、`Endpoints.tsx`、`EndpointHealth.tsx`：选项显示「服务器 · 公网/专线名称」和有效地址端口；选中同服务器另一地址时要求替换现有选择，不添加第二个服务器成员。保持排序与编辑回填；归属搜索不再假定所有成员只有节点 ID。健康表区分地址并保留 10 秒新鲜度语义。
- 完成后新增宣告 `ix_member_identity`：专线成员或同服务器多地址会话/健康隔离要求消费节点支持此能力；域名/映射另按 02 检查 `ix_address_dialing`。纯公网域名/映射可继续给仅有 02 拨号能力的节点，省略 address_id、使用 public 与 02 摘要形状；不能因 03 面板上线就要求它具备成员身份能力。完整地址 ID 不发给旧节点让其忽略；专线备用不足保留 blocker，有其他可用路径时不整条阻塞。

## 迁移与兼容要求

- 本票独占 48，只能注册在 47 后。旧组成员全部迁移 public，成员及主备顺序不变；绝不能把原手填地址挪走或自动纠正。
- 被组引用的专线即使关联规则停用也不能删除，409 指明组；更换组地址与删除专线分两次合法操作，不做自动替换。API 的旧 members 输入保持可用且语义固定为公网。
- 新增字段进入确定性摘要、节点 State 与缓存恢复。保留节点对凭据、授权代次和逐规则撤销，地址变化不重新生成服务器证书。

## 测试要求

- 单元：迁移 47→48，旧成员顺序/默认公网、跨节点地址 ID、双数组不一致、同节点两地址入同组、被引用删除、事务失败不推进 revision；同节点两地址跨组快照通过、同一地址冲突拒绝、健康/轮换/Restore 不串用。
- 单元/clusterharness：双地址一好一坏时，引用坏地址的规则不因同节点好地址而就绪；同 address_id 修改地址或端口，注入旧摘要迟到成功后仍待新证据，收到新摘要才就绪。分别验证面板入口就绪、健康 API、换 IP 确认均不串用。
- 兼容：真实 02 版节点二进制（或由该版真实源码构建的节点）× 03 版面板；只修改新版节点声明不足以代替版本组合验收，纯公网 IPv4/域名/映射可运行；专线/多地址隔离被门禁，收包无新身份字段；主成员支持、备用不足时仍可运行并保留具体 blocker，备用升级须新应用/授权确认后才加入候选，不能用旧样本补齐。
- clusterharness 新建 `ix_member_addresses_test.go`：A 的两个端口分别经两组连 B 的公网和专线（含域名 + 真实端口映射），记录实际拨号地址并传真实业务字节；阻断公网只影响公网路径，专线保持可用。B 换公网 IP 后专线规则地址、会话和健康不被替换；公网确认进度只列公网消费者；面板/节点重启恢复地址选择。旧 A 上的专线规则被拦，纯公网规则正常。
- 回归 `tunnel_rotation_test.go`、`exit_health_test.go`、`exit_blocked_test.go`；运行 `go test ./internal/panel ./internal/forwarding ./internal/node -timeout=10m` 和 IX/隧道轮换相关 clusterharness 定向测试。前端执行 `npm --prefix web test -- src/EndpointData.test.mjs`、新增成员数据测试与构建。

## 验收标准

- [x] 组内同服务器唯一，跨组可选不同地址，UI 与 API 往返保留地址和顺序。
- [x] 两地址的连接、健康、失败和恢复相互隔离，证书/凭据仍属于同一服务器身份。
- [x] 专线引用有删除保护，公网换 IP 不覆盖或等待专线路径。
- [x] 旧库、旧 members 客户端、旧公网节点保持兼容。
- [x] 面板入口就绪按规则地址和配置摘要匹配；两地址一好一坏、迟到旧样本与 02×03 兼容验收通过。

## 不做的事

不实现链或组间 fallback、最小连接数策略、自动选地址；不把一台服务器的多个地址当作同组多个主备成员；不提交发行产物。

## Comments

### 交付内容与兼容取舍（2026-11-12）

- 只追加迁移 48，保留原 `(group_id,node_id)` 主键、成员顺序与主备策略。47 旧成员默认 `public`，不搬动原手填公网槽；触发器检查专线属于成员服务器，保护被组引用的专线身份及删除。API 返回有序 `member_addresses` 和一致的旧 `members` 投影；只传旧数组固定表示公网，双数组的长度、节点或顺序不一致返回 400，失败不推进 revision。
- 专线表单更新保留原行及稳定 ID，先临时移开位置再 upsert，不删除后重建引用行。即使关联规则停用，被组引用的专线仍返回 409 并指出组名；整笔事务回滚。须先修改组地址，再单独删除专线，不自动替换为公网。
- 快照按成员地址读取公网或专线及其有效对外端口；服务器仍监听本机端口。证书、随机 TLS 内部名称、节点对凭据及逐规则节点授权保持服务器身份语义，地址 ID 不混入认证身份。专线不携带公网 `address_version`；公网换 IP 不改写专线配置、不替换已有专线业务载体，也不等待只使用专线的入口确认。
- `TunnelEndpoint.address_id` 与 `ExitHealth.address_id` 均使用字段 280，复用 281 摘要。`make proto` 生成并提交 `control.pb.go`，gRPC 生成文件无变化。客户端、会话和健康按服务器及归一后的地址 ID 隔离，空 ID 统一解释为 `public`；同地址跨组配置须一致，同服务器不同地址可跨组并存，组内仍按服务器唯一。轮换更新该服务器全部地址客户端，保留旧会话退役、预算、恢复观察与 State/缓存恢复策略。
- 摘要覆盖服务器、地址身份、拨号地址、有效端口、传输与 TLS 名称；面板健康保存/读取按当前下发引用、服务器、地址及摘要核对，去重按服务器加地址。入口就绪逐规则核对所选组端点和已确认 `AuthorizedExitIds`，健康键包含地址与摘要；保留既有进程、会话、接收和探测时效门槛。旧摘要即使接收时间很新也不能刷新当前证据，健康 API 将旧空身份规范为 `public` 输出。
- 新节点宣告 `ix_member_identity`。专线消费要求此能力，域名/显式对外端口另要求 `ix_address_dialing`；未选用专线不影响公网规则。真实 02 节点的 IPv4、域名与映射公网路径均省略 280、携带 02 形状的 public 摘要 281，继续可运行；不能把专线身份下发给旧节点让其忽略。真正 pre-02 的传统 IPv4 路径仍保留原空摘要形状和既有证据约束，不能获得新版成员身份语义。
- 前端以 `member_addresses` 为事实来源，选项及组列表显示「服务器 · 公网/专线名称」与有效地址端口；同服务器换地址须确认后原位替换，批量勾选同服务器多个地址明确拒绝。保留主备排序、编辑回填及归属搜索；健康表使用服务器加地址的行键，两地址分行，保留 10 秒新鲜度。

### 自动化验证与失败修复

- 先按接缝运行失败测试，再实现迁移/API、字段 280、身份摘要、跨组双地址、健康/就绪隔离、引用删除回滚和成员数据；没有删除失败用例。首轮集成测试误用进度字段 `entry_id`，按实际 API 改为 `node_id`；离线重启先等 B 的真实隧道监听恢复，再恢复 A，保留真实业务字节断言。02 遗留测试中的“当前新版节点不带 280”改为当前 03 节点应带 `public`，真实旧节点无 280 的断言另由真实版本矩阵验证。
- `go test ./internal/panel ./internal/forwarding ./internal/node -timeout=10m` 退出码 0：panel **46.972 秒**、forwarding **12.004 秒**、node **11.481 秒**。
- `go test ./internal/clusterharness -run 'Test.*(IXAddress|IXMember|ExitBlocked|TunnelChanges|TunnelRotation)' -count=1 -timeout=15m` 退出码 0，包内 **31.698 秒**。新增集成通过 A 的两个真实端口分别经公网与专线连接 B；专线含域名解析与真实 TCP 端口映射，A 不能直拨目标，记录实际隧道拨号地址并传真实业务字节。验证公网阻断不影响专线、公网换址不等待专线消费者且已有专线流继续、旧摘要迟到仍不就绪、新摘要才恢复，以及节点/面板重启后保留地址选择。
- `go test ./internal/clusterharness -run TestIXMemberReal02 -count=1 -timeout=3m -v` 退出码 0，包内 **2.630 秒**；真实 02 外部进程的 IPv4、localhost 域名、真实映射三个子场景均通过。专线精确门禁，主公网可运行而备用专线不足时保留准确 blocker；停旧进程、同凭据启动 03 节点后，拦住备用新 grant 下发，确认前不授权、确认后才加入候选。
- `npm --prefix web test -- src/EndpointData.test.mjs` 退出码 0，**10/10**；`node --experimental-strip-types --test web/route.test.mjs web/server-groups.test.mjs web/src/*.test.mjs` 退出码 0，**30/30**，duration **105.86425 ms**。`npm --prefix web run build` 退出码 0；保留既有 Vite 大 chunk 和 Node 颜色环境警告。
- 只读审查发现真实 02 普通 IPv4 路径仍可能空摘要，地址/监听修改后可误接旧成功。先补失败测试，再让已具备 02 拨号能力的全部公网路径发送 281，继续省略 280；复测真实矩阵通过，原审查员确认该问题已修复。
- 首轮 `GOFLAGS=-timeout=30m make test` 退出码 **2**，墙钟 **400.10 秒**，clusterharness **388.806 秒**、panel **59.985 秒**。既有 `TestRestoreDiscardsHistoricalBlockedDecision` 在替换 `panel.db` 时遇到文件已存在，其他包及本票场景未报失败；孤立该原测试连续 10 次及 50 次均通过（包内分别 **6.362 秒**、**27.321 秒**）。定位到测试台 gRPC `Stop()` 默认不等处理器退出就关闭并替换数据库的生命周期缺口；新加真实阻塞 RPC 回归先失败，证明旧处理器尚未退出恢复已完成，再以 `WaitForHandlers(true)` 让测试台等待取消的处理器结束。回归及原失败测试各连续 10 次通过，包内 **7.997 秒**。该回归精确复现了关闭时序缺口，初次文件重建的具体交错未单独重现；未改变生产恢复实现或备份禁止覆盖的保护。
- 测试台关闭边界补审无 Critical/Important；Minor 指出失败清理可能与恢复协程并发，已增加独立完成信号，释放屏障后等待恢复协程退出。两项定向测试再次通过，包内 **2.315 秒**、退出码 0；`git diff --check` 通过。
- 最终 `GOFLAGS=-timeout=30m make test` 完整重跑退出码 **0**，墙钟 **392.90 秒**、clusterharness **380.995 秒**，其余已验证且未变更的包命中有效缓存；包含前端构建、Linux 节点临时构建、全仓 `go vet ./...` 和 `go test ./...`，未跳过测试。测试失败清理补强后的源码另由上述 **2.315 秒**定向复测验证。
- 已恢复本轮构建修改的已跟踪 `web/dist`、`internal/nodebundle/nbpass-node-linux-amd64.gz`，仅移除本轮新生成的两个 dist 文件，不提交发行产物或 `bin/`。本轮始终在指定既有目录及 `feat/ix` 分支工作，没有创建工作树、切分支、推送或部署。代码及自动化验证完成后将本票标记 `resolved`，浏览器验收保留为下节交接。
- 代码提交：`118891e` 测试台恢复边界与回归；`2ce724d` 迁移、协议、面板/节点隔离及真实兼容测试；`4374eea` 成员地址选择与分地址健康界面。全部本轮文档只随本票记录整合为一次中文文档提交。

### 真实 02 基线与浏览器交接

- 真实源码固定为 `946783879ab679baba2299704e75fcf398a86d7e`。保留 `bin/nbpass-node-ix02`（SHA256 `bed243861b9d6a25657cbf1a016a7c85ed29cc334491464c5f76cacc1cb03058`）和 `bin/nbpass-node-ix02-linux-amd64`（SHA256 `ef3ad98e58b6ed50b048f5604e7ad351e8b6f2725e4e1babfa4f41f353551b81`），本轮未覆盖；测试优先核验并运行该真实产物，无产物时从固定提交导出源码到测试临时目录构建，不使用删新版声明冒充旧节点。
- 浏览器验收交主编排者，本轮未执行或声称通过：核对公网/专线选项名称与有效端口；同服务器替换确认及取消；主备排序、保存后编辑回填；按服务器、专线名称及地址搜索；双地址健康分行及 10 秒过期；被引用专线删除的 409 反馈。1366–1440px 检查全部列及长名称/域名，窄屏检查横向滚动和弹窗。
- 本轮只完成 03，不运行 04/05 或后续票。后续继续使用成员地址模型、能力门禁及字段 280/281，保留 01 的真实旧缓存回滚策略；06c 的路径就绪在当前逐规则地址/摘要证据上接入。文档只更新本票，最终单独一次中文文档提交。
