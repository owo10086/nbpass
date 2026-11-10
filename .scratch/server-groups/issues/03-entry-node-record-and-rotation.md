# 03: 入口节点受管记录与单节点入口换 IP

**What to build:** 入口节点可选绑定一条受管记录（DNSPod 账号、域名、记录 ID、线路）。记录期望是面板确认的节点地址：首次发布由管理员触发，写入地址后用明确的启用步骤启用记录（绑定的可以是暂停记录），启用失败或结果未知时不标为发布完成；受控换 IP 新地址确认后由协调器改为新地址并保持启用状态；节点上报地址与面板确认的地址不一致（外部换了地址）时只通知，节点页提供"同步地址"手动操作——不以存在节点级记录为前提，同步覆盖节点级记录和该节点在各全部发布组的成员记录，各自保持启用或暂停状态。解析监控对该域名以期望地址为期望。换 IP 的申请用途新增"单节点入口"：节点承载至少一条启用的单节点规则、开了换 IP、被墙。节点页显示绑定的域名、记录状态和期望地址。

**Blocked by:** 01, 02

**Status:** ready-for-human

**Refs:** 规格用户故事 14、15；GLOSSARY「受管记录」「被墙处理策略」「解析监控」；`internal/panel/exit_blocked.go`（`automaticRotationPurpose`）、`rotation.go`、`regions.go`

- [x] 节点可绑定、更换、解绑受管记录；已被其他归属占用的记录绑定被拒绝；解绑后旧记录保持原状
- [x] 绑定暂停记录后首次发布：先写地址再启用；启用超时时不显示发布完成，核对后才完成
- [x] 受控换 IP 新地址确认后记录改为新地址；换 IP 失败或未确认时不改
- [x] 节点上报的地址与面板确认的地址不一致时只通知；手动同步后节点级记录改为新地址；节点没有节点级记录、只有全部发布成员记录时也能同步，且各记录保持原启用状态
- [x] 只承载单节点规则、不在任何入口组、不是出口的节点：开了换 IP 时被墙会自动换 IP；没开时不换
- [x] 删除绑定了记录的节点：记录保持原状并提示；记录有在途动作时拒绝删除
- [x] 解析监控自动以期望地址为期望
- [x] 以上行为由测试台通过面板 HTTP API、假 DNSPod 与假换 IP 接口验证

## Comments

2026-11-10 实施记录（分支 `sg/03`，基线 `23ea7a6`；仅在指定工作树实施，未创建或切换分支、未创建工作树、未 push；未修改规格、术语、ADR 或其他票）：

- **节点记录与首次发布**：复用 `bindRecordTx` / `unbindRecordTx` 的远端身份唯一性、绑定代次、在途保护和旧记录提示；节点可绑定暂停的单线路 A 记录。换绑、解绑只修改本地归属，旧远端记录保持原状。节点发布使用独立的 `node_publications` 版本；换绑和解绑抬高已有版本，旧页面不能发布新绑定。首次发布要求管理员明确确认入站地址及当前版本，先写确认地址、保持当时状态，再单独执行显式启用；仅完整确认后标记首次发布完成，明确拒绝终结、不自动重试，结果未知先读回核对。
- **地址确认与同步**：普通上报只通知偏差，不改变确认地址或 DNS。手动同步要求 `ipv4` 与当前上报一致，并提供 `confirm_inbound:true`；确认地址、监控期望和全部相应逐记录动作同事务提交，在途冲突整体返回 409 并回滚。同步不要求节点级记录存在，覆盖节点级记录及该节点所有已发布成员记录；`PreserveEnabled` 让协调器按执行时从服务商读回的实际状态改值，外部暂停不会被旧缓存误启用。
- **受控换 IP 与并发收尾**：仅在既有受控换 IP 首次确认新地址时提交 `address_update`，失败、观察中或地址来源不一致时不改 DNS。同组其他成员仍有在途动作时，不推进组版本覆盖其期望，而在确认事务内保存逐记录待办；原动作结束后由既有节点策略循环提交并唤醒协调器，正常重启可以续跑。待办按绑定代次和确认地址校验，纳入绑定、解绑、节点删除、组删除和成员移除保护；备份恢复清除旧待办，不重放旧地址收尾。锁顺序保持 `entryMu → controlMu`；受控确认和策略循环仅持 `controlMu`，同步接缝不反向获取 `entryMu`。
- **单节点用途、监控与恢复**：新增 `singleEntryRotationPurpose`，通过启用规则的 `entry_node_id` 判断“单节点入口”用途，并入 `automaticRotationPurpose`；继续复用节点换 IP 开关、被墙、自测、额度、冻结及执行准入。节点监控使用独立 `owner_node_id`，以面板确认地址为期望，不能单独修改或删除；允许节点与主备组使用同名域名且不互相夺取监控。恢复核对实际已启用的未知首发时可完成发布标记，不重复写 DNS；恢复最新可信受控地址时同事务更新监控期望。
- **前端**：节点列表显示绑定域名、最近确认的启用/暂停状态、发布阶段、期望地址、上报与确认不一致及记录等待同步提示。“域名与地址”对话框支持 DNSPod 账号创建、绑定、换绑、解绑、首次发布和同步地址，删除节点确认显示旧记录保持原状提示；解析监控页标明节点自动管理。按要求未新增前端自动化测试。
- **数据库迁移**：在 `internal/panel/migrations.go` 列表末尾追加当前 **第 38 版 `entryNodeRecordMigration`**，SQL 单独定义于 `internal/panel/entry_node_record_migration.go`，不依赖编号。追加 `node_publications.published`、`entry_action_records.preserve_enabled`、`dns_watches.owner_node_id` 及归属索引，新增 `node_address_sync_jobs`；已有字段和历史动作保持不变。票 04 合并时由集成者协调重新编号。未修改 protobuf、生成协议源码或节点生产代码，无需运行 `make proto`。
- **新增端点**：统一沿用面板路由、管理员认证及 `problem` / `respond`。前缀 `/api/nodes/{id}`：`GET /managed-record` 读取绑定、`PUT /managed-record` 绑定或换绑、`DELETE /managed-record` 解绑、`GET /publication` 读取期望版本/地址/动作/首发状态、`POST /publish` 首次发布、`POST /sync-address` 手动同步、`GET /dns-watch` 读取节点监控。现有 `GET /api/nodes` 增加绑定及记录阶段，`GET /api/nodes/{id}/references` 增加旧记录提示。

验收项与测试对应（新增用例均在 `internal/clusterharness/entry_node_record_test.go`；通过面板 HTTP API、真实节点、假 DNSPod 和假 TLS 换 IP 服务断言外部行为）：

1. **绑定、更换、解绑、唯一归属及旧记录保持**：`TestEntryNodeRecordBindingPublishAndPendingProtection`；另以 `TestEntryNodeRebindInvalidatesStalePublicationVersion` 覆盖首次发布前、后换绑使旧版本失效。
2. **暂停记录首次发布、明确启用、未知结果核对**：`TestEntryNodeFirstPublishWritesPausedValueBeforeExplicitEnable`、`TestEntryNodeRecordBindingPublishAndPendingProtection`、`TestEntryNodeRecordEnableRejectionDoesNotCompleteOrRetry`；`TestEntryNodeRestoreReconcilesUnknownFirstPublish` 覆盖备份恢复核对后完成且不重复写入。
3. **受控成功更新、失败或未确认不更新**：`TestEntryNodeRecordControlledRotationOnlyUpdatesAfterConfirmation`、`TestEntryNodeFailedAndUnconfirmedRotationLeaveDNSUntouched`；`TestEntryNodeMemberSyncDoesNotDiscardAnotherMembersPendingAction` 覆盖组内在途动作下排队收尾、面板和真实节点正常重启后继续执行。
4. **外部变化只通知、手动同步全部归属并保持状态**：`TestEntryNodeRecordExternalAddressNeedsManualSyncAndWatch`、`TestEntryNodeSyncWithoutNodeRecordCoversAllPublishedMembers`、`TestEntryNodeSyncPreservesProviderStateAfterExternalPause`、`TestEntryNodeMemberSyncDoesNotDiscardAnotherMembersPendingAction`，覆盖无节点级记录、多组启用/暂停成员、外部暂停、组内冲突拒绝且确认地址回滚。
5. **仅承载单节点规则的自动换 IP 开关**：`TestEntryNodeSingleRuleAutomaticRotationPurpose` 的“开启”“关闭”子场景，节点无入口组、无出口用途。
6. **删除保留远端记录并提示、在途保护**：`TestEntryNodeRecordDeletePreservesRemoteAndWarns`、`TestEntryNodeRecordBindingPublishAndPendingProtection`；`TestEntryNodePendingAddressSyncProtectsGroupAndMembership` 补验仅有地址收尾待办时删除组、移除成员仍被拒绝。
7. **解析监控期望自动维护**：`TestEntryNodeRecordExternalAddressNeedsManualSyncAndWatch`、`TestEntryNodeWatchSameDomainGroupCannotTakeOwnership`、`TestEntryNodeRestoreControlledAddressUpdatesWatchExpectation`，覆盖禁止单独编辑/删除、同名归属隔离及恢复后新地址期望。
8. **外部行为与权限**：上述场景使用完整测试台；`TestEntryNodeRecordAPIsRequireAdminAndExplicitConfirmation` 覆盖七个新增端点的未认证拒绝、显式确认、非法地址及不存在节点。成员绑定 API 属于尚未合入的票 04，因此成员场景以 SQL 预置票 01 模型的已发布备份夹具及在途待办；后续操作与断言均通过 HTTP 和外部服务，不断言数据库结构。票 04 合入后应补做真实成员绑定流程的集成验收。

- **TDD 与审查**：先编写测试台场景，观察新增节点路由返回 404、单节点用途不触发自动换 IP 的 RED，再实现至 GREEN。审查发现的首发写值顺序、服务商实际启用状态、同名监控归属、未知首发恢复、换绑版本以及待办生命周期问题均补充 HTTP 回归，观察 RED 后修复。使用 `requesting-code-review` 做只读独立审查，最终复核未发现剩余 Critical / Important / Minor 或阻塞问题；未生成额外审查文件。
- **歧义决定**：新节点沿用票 01 语义，先手动同步确认地址，再首次发布；同步可以在首发前改值，但保持暂停且不代替显式首发启用。已发布成员由 `owner_kind='member'` 和对应 `entry_publications.desired_version>0` 识别，避免首发前自动写；主备组记录不纳入节点同步。换 IP 后保持服务商当时的启用/暂停状态，异常成员不会因改值提前加回。组内在途动作下，手动同步拒绝，已确认的受控换 IP 则持久化收尾，不丢弃确认结果或其他成员的动作。删除接口继续返回 204，提示由删除确认前的 `/references` 提供。
- **最终验证**：`env -u FORCE_COLOR GOMAXPROCS=2 GOFLAGS=-p=1 make test` 完整通过，包含前端 TypeScript/Vite 构建、Linux 内嵌节点包重建、`go vet ./...` 和全仓 Go 测试，最终测试台 **478.356 秒**；面板本轮复用相同源码上一轮通过结果（48.411 秒）。`env GOMAXPROCS=4 go test -race -p 1 ./internal/panel/... -count=1 -timeout=20m` 通过，面板 **317.345 秒**，未报告数据竞争。新增 `TestEntryNode* -count=3` 通过（17.174 秒）；24 个改动 Go 文件通过 `gofmt` 检查，`git diff --check` 通过。Vite 仅有既有大体积分块提示。未出现需要重跑的 `TestRotation*`、`TestProbeMinute*` 或 `TestEntryOfflineMissingCheckerResultCannotConfirmFailure` 失败。
- **验证期间的偶发失败**：后续收尾改动后的完整回归曾在 `TestUpgradeMaintenanceSuppressesFailoverAndRestartsOfflineEvidence` 返回 409（节点须在线且已安装独立更新服务），该场景单独 `-count=3` 通过（13.918 秒），随后最终完整 `make test` 通过。检查升级接口与测试台重启流程未被本票修改，发现原场景只等待在线、未显式等待更新能力的潜在时序窗口，根因尚未确认，不将重跑通过视为已修复；未修改升级生产代码或该测试。
- **票 05/06/07 接缝**：`syncNodeRecordsTx(ctx,tx,nodeID,address,kind,protectPending)` 统一覆盖节点级和全部已发布成员记录；手动调用传 `sync_address,true`，受控确认传 `address_update,false`，与确认地址同事务，提交成功后按 `actionWorkerKey(a)` 唤醒。`ManagedRecordChange.PreserveEnabled` 保持实际服务商状态，后续摘除/加回仍由各票决定，不能借地址同步启用记录。`node_address_sync_jobs` 与 `reconcileNodeAddressSyncs` 承接组内在途动作后的地址收尾。票 07 可在单节点、入口组、出口等自动用途的申请及外部调用前按节点所属任一组的观测模式统一拦截；手动 `sync_address`、手动操作及已授权换 IP 的 `address_update` 收尾应继续完成，不能被当作新的自动换 IP 申请阻断。
- **产物与交付**：提交前已执行 `git checkout -- web/dist internal/nodebundle`，删除本轮新增的未跟踪 `web/dist/assets/index-BJgSlRzr.js`，确认生成产物无差异且未提交；由集成者统一 `make build`。后端、迁移与测试提交 **`b3ad9be`（支持入口节点受管记录与单节点入口换 IP）**，前端提交 **`925c298`（节点页支持受管域名发布与地址同步）**；本票实施记录、勾选验收项和人工清单统一单独一次中文文档提交。无本票功能偏离或阻塞自动化验收的遗留问题；成员绑定完整流程待合入票 04 后集成验收，Linux、真实 DNSPod 与浏览器人工清单尚未执行，升级场景偶发失败的根因仍待确认。

Linux 人工验收清单（**以下全部尚未执行**，自动化测试不能代替真实部署、DNSPod 与浏览器人工验收）：

1. **升级与构建**：集成者协调票 04 的迁移编号后统一 `make build`；备份已有 Linux 第 37 版数据库，再升级，并验证新安装。通过条件：面板和节点正常启动，原主备组、节点、记录、发布版本及历史动作保留，SQLite 外键检查通过。
2. **暂停记录首发**：接入真实 Linux 入口节点和隔离的 DNSPod 暂停 A 记录，在节点页先同步确认可入站公网 IPv4，再绑定并首次发布。通过条件：记录先改值保持暂停、再明确启用，页面完整确认后才显示首发完成；记录、线路和确认地址正确。
3. **启用异常**：阻断 DNSPod 启用响应并临时阻断读回，再恢复读回；有条件时触发 `500025` 明确拒绝。通过条件：未知期间不显示完成，恢复后先核对且不重复写；明确拒绝显示失败、产生通知、不自动重试。
4. **归属与在途保护**：用相同凭据的两个账号重复绑定同一记录，尝试在途换绑、解绑、删除；动作结束后换绑、解绑和删除节点，并在旧页面提交首发。通过条件：重复归属及在途修改被拒绝，旧版本返回 409，旧远端记录保持原状且删除/解绑前有提示。
5. **外部变化与全归属同步**：从面板之外改变节点公网地址，等待上报；点击同步。票 04 合入后，再让无节点级记录的节点加入多个已发布全部发布组，成员记录分别启用、暂停，并外部暂停一条此前启用的记录后同步。通过条件：同步前只通知且确认地址/DNS 不变；同步后全部相关记录更新，各自保留实际启用状态，监控期望更新。
6. **受控换 IP 与并发收尾**：分别制造失败、查询源不一致、观察中及成功确认；再令同组另一成员 DNS 动作在途，执行换 IP，确认后重启面板与节点并恢复 DNSPod。通过条件：失败或未确认不改记录；成功只同步地址且保持状态，待办重启续跑，其他成员期望不丢失；手动同步遇同组在途返回 409 且不改确认地址。
7. **单节点用途**：不建入口组、不配置出口用途，只创建启用的单节点规则，配置自测与可用换 IP 接口，分别开启和关闭自动换 IP 后制造被墙。通过条件：开启时按既有额度、冻结等约束申请一次换 IP，关闭时不调用接口；停用全部单节点规则后不再具有此用途。
8. **解析监控隔离**：绑定域名后读取监控，尝试单独修改期望或删除；创建使用同名域名的主备组。通过条件：节点期望始终来自确认地址，禁止单独编辑/删除，同名组和节点各自维护期望；DNS 传播由真实解析结果观察。
9. **备份恢复**：恢复含未知首发的备份并读回实际已启用记录；恢复换 IP 前的备份，由节点报告最新成功地址并核对。通过条件：逐条核对前不自动写，历史 DNS 动作和旧收尾不重放，核对可完成首发且监控期望采用最新可信确认地址。
10. **页面与通知**：通过 Linux 部署的 HTTPS 入口，在桌面和窄屏检查域名/状态/地址列、账号创建、绑定/换绑/解绑、首发确认、同步地址、删除提示、长域名及会话失效。通过条件：信息与 DNSPod 一致，失败和偏差有明确提示，事件及 Telegram 通知可核对且恢复通知通道不导致重复 DNS 写入。
