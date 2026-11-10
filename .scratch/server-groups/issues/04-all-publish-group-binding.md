# 04: 全部发布组：建组、逐成员绑定记录与首次发布

**What to build:** 入口组建组时选择发布模式（主备或全部发布），创建后只读；被墙处理策略和首选入口回切只对主备组显示。全部发布组的每个成员各绑定一条已有记录（可以是暂停状态），绑定时校验与组域名同名、同线路、类型为 A、未被其他归属占用；成员没绑齐时不能发布，新增成员先绑记录才参与发布。首次发布由管理员手动触发并确认各成员可直接入站：当时在线、就绪、未被墙、不在维护中的成员记录写成其地址并启用；其余成员有面板确认的地址时写成其地址并暂停，没有有效地址时只确认暂停、保留服务商原值并提示"地址待确认"，不写空值或无效地址；没有一个健康成员时拒绝。首次发布前组不做任何自动摘除、加回或启用。首次发布和之后任何多记录调整都先启用、后暂停：确认至少一条目标记录已启用后，才暂停最后一条原有启用记录；未确认的启用不计入保底；目标记录全部启用失败或结果未知时保留最后一条原有启用记录，显示发布失败或部分完成。新增成员绑定记录后进入暂停待加回状态，由票 05 的加回规则启用。成员移出全部发布组时先暂停它的记录、确认后解绑；它是唯一启用记录时拒绝。删除全部发布组时成员记录保持原状并提示。解析监控对全部发布组以已发布地址集合为期望：没有集合外地址即一致；摘除在所有路径都不再返回该地址后算生效，加回在任一路径返回后算生效；被摘除成员的地址仍由其他启用成员发布时，摘除以服务商确认暂停为完成，显示"该地址仍由其他成员发布"，不等待地址消失；记录状态与地址传播状态分别展示。入口组页按成员显示记录地址与启用状态。

**Blocked by:** 01

**Status:** ready-for-human

**Refs:** 规格用户故事 1、2、3、9（显示部分）、18；ADR 0007；GLOSSARY「发布模式」「受管记录」「解析监控」；`internal/dnswatch/model.go`、`entry_domain_watch.go`、`entry_dns_audit.go`

- [x] 建组可选发布模式，建后不能修改；迁移后已有组为主备且行为不变
- [x] 成员逐条绑定记录，校验同名、同线路、A 类型、未被占用；暂停的记录可绑定
- [x] 成员未绑齐时发布被拒绝并指出缺哪个成员
- [x] 首次发布后健康成员记录为其地址且启用，其余为其地址且暂停；没有健康成员时拒绝；面板不新建、不删除记录
- [x] 一个健康成员加一个没有有效地址的成员：后者只确认暂停、保留原值并提示"地址待确认"
- [x] 不健康成员 A 的记录原本启用、健康成员 B 原本暂停：先启用 B，确认后才暂停 A；B 启用失败、超时或中途重启时 A 保持启用，显示发布失败或部分完成
- [x] 两条记录部分成功后重启：只核对未确认的那条，不重发已确认的
- [x] 首次发布前成员异常或恢复都不触发任何 DNS 写入
- [x] 启用某条被 `500025` 拒绝时：该成员显示启用失败并通知，其他成员已成功的部分保留
- [x] 成员移出：记录先暂停再解绑；唯一启用记录不能移出；有动作在途时不能移出
- [x] 解析结果是已发布集合的子集算一致，出现集合外地址按疑似污染/偏差规则处理；摘除与加回的"已生效"按上述规则判定；两个成员地址相同时摘除其一以服务商暂停确认为完成
- [x] 逐记录偏差审计同时核对地址与启用状态，外部误启用或误暂停按现有偏差写回规则处理
- [x] 以上行为由测试台通过面板 HTTP API 与假 DNSPod 验证

## Comments

2026-11-10 实施记录（分支 `sg/04`；在指定工作目录实施，未创建或切换分支、未创建工作树、未 push；未修改规格、术语、ADR 或其他票）：

- **实现范围**：建组选择 `primary_backup` 或 `all`，省略时沿用主备；模式创建后由 API 与数据库共同保护。全部发布按成员绑定已有记录，首次发布、绑定后的暂停、成员移出及偏差写回统一进入逐记录协调器；面板不创建、不删除 DNSPod 记录。主备专属被墙策略、活动入口切换、首选入口回切及原跨组换 IP 查询限于主备模式，原主备业务与假 DNSPod 写入次数断言未放宽。自动摘除、加回、最少保留数策略、手动锁定操作、被墙换 IP 切离屏障及观测模式未在本票实现。
- **迁移与模型**：在 `managedRecordModelMigration` 后追加第 **39** 版 `allPublishMigration`（原为 38，合并时排在票 03 的第 38 版之后），SQL 独立放在 `internal/panel/all_publish_migration.go`，不依赖迁移编号；票 03 合并时由集成者统一编号。既有组默认 `primary_backup`；组增加 `minimum_kept`（默认 1）、`first_published_at`。新增 `entry_member_publications` 保存 `expected_value`、`expected_enabled`、`address_pending`、`reason`、`last_action_error`、`manual_locked`、`removal_action_id`、`propagation_status`；动作明细增加 `propagation_status`、`propagation_note`。未修改 protobuf，未运行 `make proto`。
- **新端点**：`GET /api/entry-groups/{id}/members/{node}/managed-record` 读取成员绑定；`PUT` 同路径读取并绑定或换绑已有记录；`DELETE /api/entry-groups/{id}/members/{node}` 返回 202，暂停确认后本地解绑并移出，可重入且重启后继续收尾。扩展既有建组/编辑、入口组列表、`GET .../publication`、`POST .../publish`，全部发布首发使用 `confirm_inbound` 与 `member_ipv4`（成员 ID 到公网 IPv4 的映射）；全部发布删组返回 200 和旧记录保持原状提示，主备仍返回 204。
- **首次发布与协调器**：成员未绑齐或没有健康有效地址成员时拒绝，不提交 DNS 写入。未知地址使用空目标值表示保留服务商现值和 TTL，只确认暂停并显示“地址待确认”。先处理目标启用再处理暂停；未知启用先读回，未知暂停不再计入下一条暂停的启用保底，防止多条未确认暂停清空集合。部分成功逐条落盘并保留；明确拒绝终结该条、通知且不自动重试，后续只读审计仍可识别服务商人工修复。至少一条目标启用被确认后才保存首次发布标记，重启不丢失。移出动作确认后通过 `commitForwarding` 更新成员与节点快照，保持 `entryMu → controlMu` 锁序。
- **集合监控与审计**：期望取已经读回确认启用的记录地址并去重；解析子集算一致，集合外地址沿用连续异常与旧 TTL 宽限规则，仅解析异常不会写 DNS。逐记录审计同时核对值与启用状态，误暂停、误启用或改值通过统一动作恢复；历史明确失败只读不重试。启用任一路径返回即生效；暂停须所有有效且新鲜的路径均不再返回；同地址仍有其他启用记录时服务商暂停确认即完成，显示“该地址仍由其他成员发布”。记录确认状态与地址传播分别展示。集合采样和节点结果校验支持至 64 个地址，手工监控期望上限保持 32；33 个真实发布节点及真实国内检测点用例覆盖原 32 上限边界。
- **前端**：仅修改 `web/src`，增加全部发布管理界面与入口组展开行逐成员记录表，展示地址、启用/暂停、原因、失败及传播状态，支持绑定/换绑、首次发布和移出。全部发布不显示首选入口及被墙策略；不编写前端自动化测试。

验收项与覆盖测试（业务断言通过面板 HTTP API、假 DNSPod 与所需真实节点；迁移测试仅用旧 SQL 安排输入，输出仍走 HTTP）：

| 验收项 | 覆盖测试与观察结果 |
| --- | --- |
| 1：建组模式、不可修改、旧组为主备 | `TestAllPublishModeAndMemberBinding`、`TestAllPublishMigrationKeepsExistingPrimaryPublication`；旧期望版本、活动入口及未确认逐记录动作保留；既有主备测试全仓回归。 |
| 2：逐成员绑定、同名同线路 A、唯一性、可暂停 | `TestAllPublishModeAndMemberBinding`；错误名称、AAAA、错误线路和重复远端归属均拒绝，暂停记录可绑定，绑定不写 DNS。 |
| 3：缺绑定指出成员 | `TestAllPublishRequiresEveryBindingAndHealthyMember`；响应包含缺记录成员名称，拒绝时无写入。 |
| 4：健康启用、不健康暂停、无健康拒绝、不创建删除 | `TestAllPublishRequiresEveryBindingAndHealthyMember`、`TestAllPublishFirstPublicationPreservesUnknownAddress`、`TestAllPublishUnknownEnableRestartReadsBackBeforePausingOriginal`；核对最终地址、启用状态及严格写入次数。 |
| 5：未知地址保留原值、提示待确认 | `TestAllPublishFirstPublicationPreservesUnknownAddress`；健康成员启用，未知成员原值不变且暂停，API 显示“地址待确认”。 |
| 6：先启用后暂停、失败/未知/重启保底 | `TestAllPublishEnableRejectionKeepsLastOriginalAndNotifies`、`TestAllPublishUnknownEnableRestartReadsBackBeforePausingOriginal`、`TestAllPublishUnknownPauseCannotCountAsLastEnabledGuard`；未确认前原启用记录保留，重启先读回，不将未知暂停算作保底。 |
| 7：部分成功重启只核对未确认条目 | `TestAllPublishTwoRecordRestartOnlyReadsUnconfirmedRecord`、`TestAllPublishPartialSuccessPersistsAndRejectsOnlyFailedEnable`；只增加一次读回，不增加已执行写入，部分成功状态持久化。 |
| 8：首发前异常与恢复不自动写 DNS | `TestAllPublishHealthChangesBeforeFirstPublishNeverWriteDNS`、`TestAllPublishModeAndMemberBinding`；真实节点离线再恢复并推进审计时间后写入仍为零。 |
| 9：`500025` 成员失败、通知、保留成功 | `TestAllPublishEnableRejectionKeepsLastOriginalAndNotifies`、`TestAllPublishPartialSuccessPersistsAndRejectsOnlyFailedEnable`、`TestAllPublishRejectedRecordStillReadsBackManualRepair`；逐条失败码、成员“启用失败”和事件可见；人工修复后重新读回采用且无自动重试。 |
| 10：移出先暂停、唯一启用和在途保护 | `TestAllPublishNewMemberWaitsPausedAndRemovalPausesBeforeUnbinding`、`TestAllPublishUnknownEnableRestartReadsBackBeforePausingOriginal`、`TestAllPublishPausedMemberCanLeaveBeforeFirstPublish`；新成员绑定后暂停待加回；无法从编辑接口绕过；删除组原远端记录和写入次数不变且有提示。 |
| 11：子集一致、集合外异常、启用/暂停传播、同地址 | `TestAllPublishSetWatchAndPerRecordStatusAudit`、`TestAllPublishDuplicateAddressWithdrawalFinishesAtProvider`、`TestAllPublishMoreThan32AddressesCanPublishAndResolve`；任一路径出现、全路径消失及同地址说明分别核对。 |
| 12：逐记录地址与启用偏差 | `TestAllPublishSetWatchAndPerRecordStatusAudit`、`TestAllPublishNewMemberWaitsPausedAndRemovalPausesBeforeUnbinding`；外部改值/误暂停恢复启用，误启用待加回记录恢复暂停；旧远端换绑记录保持原状。 |
| 13：测试台覆盖及补充边界 | 以上 17 项 `TestAllPublish*` 测试台测试及 1 项迁移测试；另以 `TestAllPublishPrimaryOnlyOperationsAreRejected` 验证模式隔离，`TestAllPublishRestoreAdoptsRecordStatesAndAllowsUnknownPausedAddress` 验证恢复采用实际状态、重建集合与版本、未知地址暂停记录合法放行。 |

- **TDD 与审查**：先增加产品边界测试，观察模式、绑定、首发、多记录失败/重启、监控等 RED 后实现至 GREEN。独立只读审查指出的未知暂停保底、超过 32 地址、首发前暂停成员移出、恢复集合与新代次放行、明确失败后人工修复读回等问题均已补回归并修复，最后复核无剩余重要问题；未生成额外审查报告。
- **歧义决定**：首条成员绑定确立该组域名、记录名称和线路，后续绑定必须一致。首发显式地址仅能初始化空的面板确认地址，已有确认地址不同则要求先在节点页同步，避免首发暗中同步该节点其他记录。首次发布标记以至少一条目标启用确认成功为界，允许保留部分成功。已发布成员可换绑，旧远端保持原状并提示，新绑定先暂停待加回；唯一启用成员不能借换绑清空已发布集合。首发前已暂停成员可以移出，不将该动作算作首发。恢复后只对换绑前已完整核对放行的组采用本次新记录读回证据放行新代次；未核对组仍受保护。无其他规格行为偏离。
- **验证中的既有时序问题**：早期全仓运行曾出现 `TestProbeMinuteAverageMaximumMissingFieldsAndPersistence` 的新鲜度等待超时，按要求单独三次通过。另复现 `TestUpgradeMaintenanceSuppressesFailoverAndRestartsOfflineEvidence` 在 `AttachUpdater` 重启后仍读到旧连接“在线”、尚未上报升级能力即请求升级，返回 409；只将该测试前置等待加强为公共 API 的 `connected && upgrade_capable`，未改升级生产逻辑或业务断言，与探针用例合并单独 `-count=3` 通过（20.377 秒）。补强首发前健康变化和外部误启用审计两项用例单独三次通过（6.622 秒）。
- **最终验证**：`env -u FORCE_COLOR GOMAXPROCS=2 GOFLAGS=-p=1 make test` 完整通过，包含前端构建、节点内嵌包重建、`go vet` 和全仓测试，最终测试台 319.768 秒、面板 38.741 秒；`env GOMAXPROCS=4 go test -race -p 1 ./internal/panel/... ./internal/dnswatch/... -count=1 -timeout=20m` 通过，面板 315.049 秒、解析监控 2.309 秒，无数据竞争报告。全部发布定向测试在补强前 16 项一轮通过，补强的两个用例分别连续三次通过后已纳入最终 17 项测试台全仓回归。所有本轮 Go 文件已执行 `gofmt`，`git diff --check` 通过；前端仅有既有 Vite 大分块提示。
- **产物与提交**：提交前已执行 `git checkout -- web/dist internal/nodebundle` 并删除新生成的未跟踪 `web/dist/assets/index-D3fgjg7O.js`，生成产物不提交。代码提交为 `c3d13c8`（成员绑定、首次发布、协调器保护、集合监控与测试）、`0ea45fe`（逐成员前端页面）、`8cb7765`（既有升级测试等待修正）；本票记录与人工清单合并为一次独立中文文档提交。Linux 人工验收尚未执行，无已知阻塞本票交付的代码问题；合并后需统一迁移编号并重新构建产物。

后续票接缝：

- **票 05**：`entry_member_publications` 提供成员期望启用、地址、待确认、原因、失败、手动锁定与传播字段；`entry_groups.minimum_kept` 默认 1，仅预留，不实现自动策略。`submitMemberActionTx(ctx, tx, group, node, kind, expectedVersion, changes)` 供单个或多个成员动作复用，调用者持 `entryMu`、同事务更新成员期望，提交后 `p.coordinator.wake(actionWorkerKey(a))`。当前动作保底固定至少一条确认启用，票 05 应在同事务选择自动摘除成员并应用最少保留数，补齐手动锁定和加回样本规则。
- **票 06**：通过记录 ID、绑定代次、逐条确认状态及首次发布标记接入跨组被墙换 IP 切离屏障；新地址同步仍用 `ManagedRecordChange` 显式保持当前启用/暂停，不把地址更新当作加回。当前主备跨组查询明确过滤模式，全部发布切离由该票扩展。
- **票 07**：自动偏差审计入口为 `auditAllDNS` / `auditAllGroup`，动作提交入口为 `submitMemberActionTx`；需在策略提交及协调器外部写前复核观测模式，首次发布、手动操作和已执行动作收尾按规格保留。
- **首发与集合扩展**：`first_published_at` / API `first_published` 控制首发边界；`refreshAllWatchTx` 从确认启用记录重建已发布集合，`reconcileAllPropagationTx` 分别计算启用、暂停与同地址传播，后续摘除/加回动作可直接沿用。解析证据继续只观察，服务商偏差写回仍经协调器。

Linux 人工验收清单（以下**尚未执行**；步骤与通过条件）：

1. 合并票 03 并由集成者统一迁移编号后 `make build`，在 Linux 备份第 37 版数据库并升级。通过条件：原组均为主备，原记录、活动入口、期望版本和未确认动作完整；主备首次发布、切换、回切及被墙策略行为保持原样，全部发布无主备专属控件。
2. 在真实 DNSPod 隔离域名预建同名同线路 A 记录（至少一条暂停），建全部发布组逐成员绑定，尝试错类型、错名称、错线路、重复归属和修改模式。通过条件：合法绑定成功，错误绑定或改模式拒绝，成员缺绑定不能发布并点名，绑定与首发前节点离线/恢复无自动 DNS 写入。
3. 准备健康、不健康但已确认地址、无有效地址三类成员，确认直接入站后手动首发。通过条件：健康记录地址正确且启用，其余暂停，未知地址原值与 TTL 保留并显示“地址待确认”；无健康成员拒绝；面板只修改已有记录。刷新与重启后状态仍可见。
4. 让不健康成员原记录启用、健康成员原记录暂停，首发时观察真实 API 调用；制造启用拒绝、响应丢失并中途重启。通过条件：先确认目标启用才暂停最后原记录，未知结果先读回；已确认记录不重发，明确失败与部分成功逐成员显示，`500025` 有事件及 Telegram 通知且不自动重试。
5. 首发后新增成员并绑定当前启用的已有记录，再测试换绑、移出、在途操作和删组。通过条件：新绑定暂停待加回；换绑旧远端保持原状并提示，新远端暂停；唯一启用成员不可移出/换绑，其他成员服务商确认暂停后才解绑移出；在途时拒绝；删除无引用组不改成员记录并提示。
6. 用不同解析器分别返回已发布集合的子集，再返回集合外地址；移出不同地址成员和相同地址成员。通过条件：子集一致、集合外连续异常可见；启用任一路径出现即生效，不同地址暂停全路径消失才生效；同地址显示“该地址仍由其他成员发布”，不等待该地址消失；记录确认和传播状态分别显示。
7. 在 DNSPod 外部改值、误暂停已发布记录、误启用待加回记录，并等待审计；人工修复一条明确失败记录。通过条件：地址与状态按各成员期望恢复，其他记录不动；明确失败不重试，人工修复后读回状态、错误与集合更新。
8. 恢复含全部发布记录的备份，逐条核对身份、地址、启用状态后放行；核对未知地址的合法暂停记录和换绑的新代次。通过条件：未放行前无自动写，采用服务商实际状态并重建集合，历史动作不重放，未核对组不能借换绑绕过保护。

2026-11-10 合并记录（集成者）：与票 03 合并时解决四处冲突（动作明细同时读取 `preserve_enabled` 与传播字段；成员移出保护同时考虑地址同步待办与仅主备的失败动作；路由与迁移列表两侧并存）。另把票 03 的节点地址同步中"全部发布成员记录"一支改走 `submitMemberActionTx`，使手动同步与换 IP 后的地址更新同事务刷新集合期望与偏差审计。

- 集成者：合并 main（生产已在第 37 版 `incidentsMigration`）后，服务器组迁移整体后移一位，本票迁移现为第 40 版。
