# 05: 全部发布组的自动摘除、加回与最少保留数

**What to build:** 全部发布组的成员离线且国内检测点确认不可达、或被墙时，协调器暂停它的记录（摘除）；未就绪不触发摘除。首次发布后所有处于暂停的成员（被摘除的、首次发布时不健康的、后来新增的，手动锁定的除外）在线、就绪、未被墙、不在维护中、当前地址有有效国内连通证据，且连续 6 次 10 秒候选检查成功后启用记录（加回），复用回切候选检查，不以时间流逝代替样本。加回前记录地址、面板确认的地址和健康证据的地址必须一致，不一致时保持暂停并通知先同步地址；同步后按新地址重新累计。首次发布前不做自动摘除和加回。手动摘除后该成员加上手动锁，不自动加回；手动加回立即启用并解除锁（成员仍异常时界面先警示）。最少保留数按组设置（默认 1，最小 1，超过成员数按成员数算），按启用的成员记录条数计：多个成员同时被判摘除时，在一个事务里决定摘哪些、保留哪些，优先保留仍在记录中的成员，再按组内顺序取；不为凑数加回已摘除成员，只通知。最少保留数不约束手动摘除，但手动摘除不能摘掉最后一条启用记录。每次摘除、加回、因保留数而保留都记事件并发 Telegram；入口组页显示摘除原因、时间和手动锁。

**Blocked by:** 04

**Status:** ready-for-human

**Refs:** 规格用户故事 4–7、9；GLOSSARY「摘除」「最少保留数」；ADR 0005；`entry_failback.go`、`entry_offline.go`、`entry_readiness.go`

- [x] 成员离线且检测点确认不可达时被摘除；检测点无效或不足时不摘除
- [x] 成员被墙时被摘除；仅未就绪不摘除
- [x] 加回需满足全部条件且连续 6 次检查成功；任一次失败、未知、证据过期或进入维护都重新累计
- [x] 首次发布时因不健康而暂停的成员、新增成员，恢复后都能自动加回
- [x] 成员记录为 X 已摘除，节点外部换成 Y 并持续健康：不加回，通知先同步；手动同步后按 Y 重新累计再加回
- [x] 手动摘除后不自动加回；手动加回立即启用并恢复自动规则；不能手动摘掉最后一条启用记录
- [x] 3 成员、最少保留 1，三个同时异常：只保留一个（优先仍在记录中，再按组内顺序），并发通知；不为凑数加回已摘除成员
- [x] 最少保留数大于成员数时按成员数算；设置为 0 或负数被拒绝
- [x] 单成员组：成员异常时因保留数不被摘除，只通知
- [x] 摘除与加回经协调器；超时先核对；迟到结果按动作 ID、记录身份与期望版本核对
- [x] 主备组行为不变
- [x] 以上行为由测试台通过面板 HTTP API、假 DNSPod 与真实节点验证

## Comments

2026-11-10 实施记录（分支 `sg/05`，基于已含票 01–04 的指定工作目录实施；未创建或切换分支、未创建工作树、未 push；未修改规格、术语、ADR 或其他票）：

- **自动摘除**：仅对已经首次发布且恢复核对放行的全部发布组执行。离线须复用国内检测点对照、目标及全体报告的有效确认；仍可达、没有检测点、对照失败、执行错误、缺报告及过期结果均不构成不可达。被墙沿用已有候选输入与事故身份；维护中和仅未就绪不触发摘除。整组在一个事务中按确认启用记录与最少保留数决定保留、摘除及成员期望，统一提交逐记录协调器，保留组内顺序靠前的成员，不启用暂停成员凑数。
- **自动加回**：复用主备回切的连续成功窗口算法，只在十秒候选定时器中累计六次真实成功检查；页面读取和大步时间快进不制造样本。在线、就绪、未被墙、非维护、当前地址证据有效且地址三方一致才累计，失败、未知、证据过期、缺样、地址变化、维护与重启后重新累计。维护在两次采样之间开启再关闭也会通过恢复代次清零；提交期间样本过期则回滚并清除窗口。覆盖被摘除、首发不健康暂停及首发后新绑定成员；手动锁定成员不自动加回。
- **手动操作与页面**：手动摘除持久化手动锁和摘除时间，不受自动最少保留数限制，但不能暂停最后一条确认启用记录。手动加回立即启用并解除锁，不等待健康样本，随后恢复自动规则。入口组逐成员表显示原因、摘除时间、手动锁和失败；管理界面支持保留数设置、手动摘除与加回，异常或健康条件未确认时先显示加回警示。按钮按服务商确认状态、手动锁和错误信息决定是否禁用，明确失败后允许同向重试。仅修改 `web/src`，未新增前端自动化测试。
- **事件与 Telegram**：摘除、加回按逐记录服务商确认产生成功事件及通知；因保留数保留、保留数短缺与地址不一致分别记录并通知。同一持续事故去重，手动恢复后再次短缺及配置 `2→1→2` 会产生新的通知。决策持久化，通知队列故障不阻塞 DNS，恢复后续投。
- **地址同步接缝修复**：节点同步成员地址时同事务更新成员期望地址和待确认标记；协调器完成 `PreserveEnabled` 同步时，成员期望启用状态也采用执行时读回的实际状态，避免后续偏差审计重新启用服务商已暂停的记录。更新仍按当前动作 ID、期望版本、记录身份与绑定代次保护，锁序保持 `entryMu → controlMu`。
- **迁移**：在 `internal/panel/migrations.go` 列表末尾追加第 **40** 版 `allPublishPolicyMigration`，SQL 独立放在 `internal/panel/all_publish_policy_migration.go`。新增 `entry_member_publications.withdrawn_at`、`restore_generation`、`entry_groups.minimum_kept_generation`、相应维护/地址/配置代次触发器，以及持久化通知决策表 `entry_member_decisions`。已有成员期望、手动锁、首发标记和未确认动作保留。未修改 protobuf、节点生产代码或生成协议文件。
- **新端点**：`PUT /api/entry-groups/{id}/minimum-kept`，请求 `{"minimum_kept":N}`，最小 1；`POST /api/entry-groups/{id}/members/{node}/withdraw` 和 `POST .../restore`，请求均须提供当前 `expected_version`，返回 202。端点沿用管理员认证与全部发布模式校验。既有 `GET .../publication` 增加 `minimum_kept`、`effective_minimum_kept`、`member_states` 和成员 `withdrawn_at`，成员、版本与动作通过同一个只读事务读取。

验收项与覆盖测试（新增测试台用例在 `internal/clusterharness/all_publish_policy_test.go`，通过面板 HTTP API、假 DNSPod 和真实节点断言外部行为；迁移测试仅用旧 SQL 安排输入，升级输出经 HTTP 核对）：

| 验收项 | 覆盖测试 |
| --- | --- |
| 1：离线不可达才摘除，无效或不足不摘 | `TestAllPublishOfflineWithdrawalRequiresValidCheckerEvidence` 的确认不可达、仍可达、没有检测点、对照失败、执行失败子场景；`TestAllPublishMissingCheckerReportNeverWithdraws` 覆盖已冻结检测点缺报告。 |
| 2：被墙摘除，仅未就绪不摘 | `TestAllPublishBlockedWithdrawalAndSingleMemberRetention`、`TestAllPublishNotReadyDoesNotWithdraw`。 |
| 3：六次新鲜成功样本，所有中断重计 | `TestAllPublishRestoreRequiresSixFreshChecksAndHonorsManualLock` 的无中断、维护、采样间维护、证据过期、缺少检查、重启、未知地址子场景；`TestAllPublishReadinessFailureResetsRestoreChecks`；`TestAllPublishAutomaticRestoreExpiryDuringSubmissionDoesNotWrite`；被墙恢复由 `TestAllPublishBlockedWithdrawalAndSingleMemberRetention` 覆盖。 |
| 4：首发暂停与新增成员可自动加回 | `TestAllPublishRestoreRequiresSixFreshChecksAndHonorsManualLock`、`TestAllPublishNewBoundMemberCanRestoreAfterSixChecks`。首发前不自动写复用票 04 的 `TestAllPublishHealthChangesBeforeFirstPublishNeverWriteDNS`。 |
| 5：外部 X→Y 后先同步，再按 Y 重计 | `TestAllPublishRestoreAddressMismatchRequiresSyncAndNewSamples`；`TestAllPublishAddressSyncKeepsActualPausedStateInExpectation` 补验外部暂停后同步及后续审计不误启用。 |
| 6：手动锁、立即加回、最后一条保护 | `TestAllPublishManualWithdrawalLockRestoreAndMinimumSetting`、`TestAllPublishRestoreRequiresSixFreshChecksAndHonorsManualLock`；核对手动事件与 Telegram。 |
| 7：三个同时异常、按顺序保留、同事务摘除、不凑数 | `TestAllPublishMinimumKeptChoosesExistingRecordsInMemberOrder`；通过控制协议边界合批真实检测点结果，断言保留首个成员、同一动作摘除另外两个；`TestAllPublishMinimumShortageOnlyNotifiesAndDoesNotRestore`、`TestAllPublishRepeatedShortageNotifiesEachIncident`、`TestAllPublishMinimumSettingStartsNewShortageIncident` 覆盖短缺与新事故通知。 |
| 8：超过成员数截断，0/负数拒绝 | `TestAllPublishMinimumKeptChoosesExistingRecordsInMemberOrder/9`、`TestAllPublishManualWithdrawalLockRestoreAndMinimumSetting`。 |
| 9：单成员异常只通知 | `TestAllPublishBlockedWithdrawalAndSingleMemberRetention/1`；持续异常检查不重复刷通知。 |
| 10：统一协调器、超时核对与迟到保护 | `TestAllPublishAutomaticRestoreTimeoutRestartReadsBeforeRetry`，核对原动作 ID、严格写入次数、在途换绑拒绝及过时期望版本拒绝；复用票 01 的 `TestManagedRecordLateResultCannotUpdateNewBindingOrDifferentAction`、`TestManagedRecordActionRestartKeepsConfirmedRowsAndReadsUnknownRow` 等底座回归。 |
| 11：主备不变 | 既有主备回切、离线、被墙与换 IP 测试全仓回归；`TestEntryBlockedRotationManualSwitchBeforePermitInvalidatesWorkflow` 仅增加公共 API 等待自动策略提交版本 2，原有业务断言和写入次数未修改或放宽。 |
| 12：测试台与补充边界 | 以上 17 个新增测试台函数；`TestAllPublishAutomaticNotificationQueueFailureDoesNotBlockRestore` 验证通知队列中断及续投；`TestAllPublishPolicyMigrationPreservesMemberExpectationAndPendingAction` 验证第 39→40 版迁移保留既有期望、锁、首发及未确认动作。 |

- **TDD 与边界修复**：先写测试台用例观察 RED，再实现至 GREEN。补充回归并修复提交停顿导致样本过期、两次采样间维护、重复短缺事故漏通知、持续被墙保留通知重复，以及地址同步期望与服务商实际状态不一致的问题。使用 `requesting-code-review` 做独立只读审查；最终复核发现服务商明确拒绝后按钮仍因期望状态被禁用，已改为结合实际确认状态和错误信息允许直接重试，经复核无剩余明确的重要问题。不生成额外审查文件。
- **验证中的时序问题**：早期完整回归曾在 `TestEntryBlockedRotationManualSwitchBeforePermitInvalidatesWorkflow` 的自动策略提交前执行固定版本手动请求；只增加 HTTP 可见版本等待，单独三轮通过。后续完整回归曾在新用例 `TestAllPublishMinimumKeptChoosesExistingRecordsInMemberOrder/1` 等待三份不可达确认时失败：同一假时刻并不阻止心跳唤醒策略，首份报告可能先触发摘除、抬高组版本并使其余检测失效。确认属于测试未安排“同时判定”的问题后，在已有控制协议边界合批真实节点三份结果，使面板同一次接收事务完成确认；保留顺序和单动作摘除两成员的断言不变，未为此修改生产策略。修正后定向连续十轮通过（21.036 秒），race 连续三轮通过（27.080 秒）。
- **歧义决定**：保存管理员设置的最少保留数原值，执行与展示有效值时按当前成员数截断；按确认启用且未进入暂停期望的记录保守计算，不将未确认暂停计入保底。新绑定成员没有本地期望地址时，仅当绑定读回地址、面板确认地址与证据地址一致才开始六次检查，否则提示先同步。手动加回不等待健康条件，界面先警示，随后自动规则可再次摘除。持续同一异常只通知一次，新事故或保留数配置代次改变可再次通知。无其他规格行为偏离。
- **最终验证**：`env -u FORCE_COLOR GOMAXPROCS=2 GOFLAGS=-p=1 make test` 完整通过，包含前端 TypeScript/Vite 构建、Linux 内嵌节点包重建、`go vet ./...` 与全仓 Go 测试；最后一次未命中缓存的测试台 **346.148 秒**、面板 **42.550 秒**。最终页面重试按钮修正后再次完整执行同一命令通过，前端与命令包重新构建验证，其余相同 Go 源码复用通过的测试缓存。`env GOMAXPROCS=4 go test -race -p 1 ./internal/panel/... -count=1 -timeout=20m` 通过，面板 **344.016 秒**，无数据竞争报告；全部新增自动策略测试台 race 一轮通过（106.260 秒），最新合批用例 race 连续三轮通过（27.080 秒）。本轮 Go 文件 `gofmt` 检查、`git diff --check` 均通过。最终完整回归无测试失败；Vite 仅有既有大分块提示。独立只读复核未发现剩余重要产品问题或规范违规。
- **产物与提交**：提交前已执行 `git checkout -- web/dist internal/nodebundle`，删除本轮生成的未跟踪 `web/dist/assets/index-U8TgoJFi.js` 与最终重建的 `index-BlcuAobd.js`，确认生成产物无差异且不提交。后端、迁移及测试提交 **`4ef374a`（实现全部发布组摘除加回与最少保留数策略）**；前端提交 **`8acbd9e`（入口组页面支持成员摘除加回与保留数设置）**；既有主备测试等待修正提交 **`94ff3c0`（修正主备被墙测试等待异步策略提交的时序）**。本票实施记录、验收勾选与人工清单统一单独一次中文文档提交。自动化验收已完成，无已知阻塞本票交付的代码遗留；Linux、真实 DNSPod 及浏览器人工清单尚未执行，状态为 `ready-for-human`，部署前由集成者重新构建产物。

后续票接缝：

- **票 06**：`checkAllWithdrawals` / `decideAllWithdrawals` 提供组级选择、保留数、成员期望及通知决策；`submitMemberActionTx` 与逐记录确认状态继续复用。换 IP 前须等待该成员在所有全部发布组中摘除确认；因最少保留数无法摘除时照常换 IP，并在事件记录仍发布地址的影响。新地址通过 `syncNodeRecordsTx` / `submitNodeRecordAddressTx` 更新，`PreserveEnabled` 保持实际启用/暂停状态，成功确认后仍须六次候选检查才能加回。本票未实现换 IP 切离屏障。
- **票 07**：自动摘除与加回唯一提交点为 `submitAutomaticMemberActionTx`，可在同事务提交前复核观测状态；外部调用前复核位于 `entryCoordinator.executeRecord` 的改值与启停分支，在 `markRecordWriting` 及服务商调用边界处理。已发出的结果仍按原动作核对，手动动作和已执行换 IP 的地址收尾须继续完成；观测抑制时应整体回滚/跳过成员期望与动作提交，不改变期望版本。本票未实现观测模式。

Linux 人工验收清单（**以下全部尚未执行**；自动化通过不代替真实 Linux、DNSPod 与浏览器验收）：

1. **升级与数据保留**：备份 Linux 第 39 版数据库，由集成者统一构建面板与节点产物后升级，并验证新安装。通过条件：迁移到第 40 版，已有主备/全部发布组、最少保留数、成员期望、手动锁、首发标记及在途逐记录动作保留，SQLite 外键检查通过，面板正常启动。
2. **首发边界与离线确认**：用三台真实入口和国内检测点绑定隔离 DNSPod 同名记录，首发前制造离线、恢复及被墙；首发后断开一个成员并分别制造国内仍可达、对照失败、缺报告、全部有效不可达。通过条件：首发前无自动摘除/加回；仅有效不可达确认后暂停该成员，缺失/无效证据不摘除，其他记录不受影响。
3. **被墙与未就绪**：首发后让一个成员连续三轮异常确认被墙，再单独制造端口冲突使另一成员仅未就绪；开启维护后断连。通过条件：被墙成员摘除，仅未就绪及维护期间不摘除，页面原因和时间正确，事件与 Telegram 可核对；票 06 的换 IP 切离顺序不在本票验收范围。
4. **最少保留数与并发异常**：默认保留 1 时令三个成员同时达到摘除条件，核对组内顺序；再设置大于成员数、0、负数，并在已有暂停成员时提高保留数。通过条件：按组内顺序保留首个仍启用成员，同一动作决定其余摘除；有效保留数不超过成员数，非法值拒绝；不为凑数加回，只通知；单成员异常不摘除。
5. **六次候选检查与中断**：分别恢复自动摘除成员、首发不健康暂停成员、新增且已绑定成员。采样五次后制造一次未就绪、未知证据、证据过期、维护、采样间维护开启再关闭或面板重启。通过条件：在线、就绪、未被墙、非维护且当前地址国内证据有效后，完整六次十秒检查才加回；任一中断重新累计，页面刷新不能加速。
6. **地址三方一致**：让已摘除成员从 X 在面板外换到 Y 并持续健康，再在节点页确认入站并手动同步；另从 DNSPod 外部暂停一条成员记录后同步。通过条件：同步前保持暂停并通知先同步，确认地址未被普通上报暗改；同步后记录改为 Y、保持实际暂停状态，按 Y 重新完成六次检查才加回，后续审计不误启用。
7. **手动锁和异常加回警示**：手动摘除健康成员，等待超过六个样本；在高保留数下继续手动摘除，最后尝试摘掉唯一启用成员；对异常成员点击手动加回。通过条件：锁定成员始终暂停，允许低于自动保留数但拒绝清空；界面先警示异常风险，确认后立即启用解锁，随后恢复自动规则，原因/时间/锁显示正确。
8. **超时、重启与在途保护**：在自动加回或摘除时丢弃 DNSPod 响应并重启面板，中途尝试换绑和旧版本请求，再恢复读回；另外制造服务商明确拒绝后解决拒绝原因，在页面直接重试同一操作。通过条件：未知结果先核对原动作而非盲目重发，已确认记录不重复写，在途修改和过期版本拒绝，记录身份与绑定代次不串用，组至少保留一条确认启用记录；明确失败有错误提示且按钮允许同向重试，重试成功后显示确认状态。
9. **事件、通知与传播**：核对每次摘除、加回、保留及地址不一致事件；阻断 Telegram 再恢复，并重复制造短缺与修改保留数。通过条件：通知故障不阻塞 DNS、恢复后可续投，同一事故不刷屏，新事故不漏通知；服务商启停确认和真实解析传播分开展示。
10. **主备与界面回归**：在桌面和窄屏检查入口组成员表、保留数、操作确认、长原因与会话失效；运行已有主备离线切换、回切、被墙与跨组换 IP 场景。通过条件：新控件仅用于全部发布，主备行为保持原有规则，页面状态与 DNSPod、事件及通知一致。

### 补充修正：全部发布成员离线证据与组版本解耦（2026-11-10）

- **问题与 RED**：全部发布复用主备的组版本查询和失效触发器，摘除、加回或地址同步提高组期望版本后，其他成员在途或已确认不可达的检测会失效并重新排期，错峰故障可能反复延后。先新增测试台回归，在生产代码修改前观察到 RED：第一名成员摘除后，第二名成员原检测从 `pending` 变为 `invalidated`；检测期间手动摘除或同步另一成员地址也复现同一问题。用例通过面板 HTTP API、假 DNSPod 与真实节点验证，不合并错峰结果。
- **修正**：全部发布按组、节点离线事故、成员受管记录 ID、绑定代次、目标地址、检测端口和节点配置版本选择证据，不再以组期望版本判断有效性。动作提交前后继续复核原证据、截止时间、在线状态、维护、地址和配置；不会因其他成员动作延长有效期或重新检测。组内同批判定仍在同一事务内按最少保留数与成员顺序决定。主备查询与切换逻辑保持原样，既有主备测试及其业务断言均未修改。
- **迁移**：在迁移列表末尾追加第 **41** 版 `allPublishOfflineMigration`，SQL 独立位于 `internal/panel/all_publish_offline_migration.go`。为 `entry_offline_checks` 增加 `managed_record_id`、`binding_generation` 和成员查询索引；组版本失效触发器仅作用于主备，新增换绑及解绑失效触发器。`desired_version` 保留为历史信息，未改为可空。升级时，旧全部发布的 `pending` / `unreachable` 检测因缺少冻结的绑定身份而一次性作废、重新检测，历史行与任务保留；旧主备证据和组期望版本保留。未修改第 1–40 版迁移、protobuf 或节点生产代码。
- **新测试**：`TestAllPublishStaggeredOfflineChecksDoNotRestartAfterWithdrawal` 覆盖三成员相隔一秒断连、分别完成不可达确认，按顺序摘除前两名并保留最后一条，逐次断言仍是原检测 ID、每成员仅一轮，完成时间不超过原首轮截止时间加五秒；`TestAllPublishPendingOfflineCheckSurvivesOtherMemberAction` 覆盖在途检测期间他人成员手动摘除与地址同步；`TestAllPublishPendingOfflineCheckRejectsChangedMember` 覆盖本成员改检测端口、恢复、维护及重新绑定后拒绝迟到证据；`TestAllPublishSameBatchWithdrawalKeepsMemberOrder` 专测同批保留首成员、同一动作摘除另外两条；`TestAllPublishOfflineMigrationPreservesPrimaryEvidenceAndInvalidatesUnboundMemberEvidence` 从第 40 版升级，经 HTTP 核对历史任务、主备证据与版本保留。
- **合批处理**：原 `TestAllPublishMinimumKeptChoosesExistingRecordsInMemberOrder` 去掉协议合批，改名为 `TestAllPublishMinimumKeptWithIndependentCheckerReports`，独立上报后核对实际启用条数、最少保留数及不加回暂停成员凑数。同批顺序和单动作两条记录的原断言移入上述专用同批用例，只有该专用用例使用协议合批；普通离线摘除不再依赖合批通过。
- **验证**：`env GOMAXPROCS=4 go test -p 1 ./internal/clusterharness -run 'TestAllPublish.*(Offline|MinimumKept|SameBatch)|TestEntryOffline|TestEntryDomainWatchOfflineSwitchInvalidatesObservation' -count=5 -timeout=15m` 通过，**251.828 秒**；面板 `Test(AllPublish|EntryOffline)` 相关迁移及旧用例 `-count=5` 通过，**14.181 秒**。最终 `env -u FORCE_COLOR GOMAXPROCS=2 GOFLAGS=-p=1 make test` 完整通过，含前端构建、Linux 节点包重建、`go vet ./...` 和全仓 Go 测试，测试台 **448.675 秒**，面板复用此前同源代码 **69.573 秒**通过的缓存。`env GOMAXPROCS=4 go test -race -p 1 ./internal/panel/... -count=1 -timeout=20m` 通过，**369.826 秒**，无数据竞争。七个修改/新增 Go 文件的 `gofmt` 检查与 `git diff --check` 均通过。Vite 仅有既有大分块提示。
- **验证中发现的测试时序**：较早扩大到所有全部发布用例的五轮回归命中既有 `TestAllPublishAutomaticNotificationQueueFailureDoesNotBlockRestore`，单独十轮复现两次。原等待条件可能读到历史通知成功、队列暂空，却尚未等到本次消息入队；因此把实际收到本次“自动加回”消息也纳入等待，同时保留 `pending=0`、`reachable` 和故障期间 DNS 加回成功等原断言。仅修正测试等待，生产通知逻辑不变；修正后单独十轮通过（**9.910 秒**），最终全仓也通过。本轮未命中已知的 `TestRotation*`、`TestProbeMinute*`、`TestEntryOfflineMissingCheckerResultCannotConfirmFailure`、`TestEntryDomainWatchOfflineSwitchInvalidatesObservation` 偶发失败。独立只读审查及通知等待补审均未发现严重或重要缺陷，未生成额外审查文件。
- **产物、提交与遗留**：提交前已执行 `git checkout -- web/dist internal/nodebundle`，删除本轮未跟踪的 `web/dist/assets/index-BlcuAobd.js`，确认生成产物无差异。代码、迁移与测试提交 **`1060299`（修正全部发布成员离线证据被组版本失效的问题）**；本补充记录单独中文文档提交。持续在 `sg/05` 操作，未建分支、未建工作树、未 push。无已知阻塞本修正交付的代码遗留；既有 Linux、真实 DNSPod 与浏览器人工验收仍未执行，人工升级目标应按第 **41** 版核对，集成部署前重新构建产物。

- 集成者：合并 main（生产已在第 37 版 `incidentsMigration`）后，服务器组迁移整体后移一位，本票迁移现为第 41、42 版。
