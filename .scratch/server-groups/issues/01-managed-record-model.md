# 01: 受管记录多条化底座

**What to build:** 把"一个入口组一条受管记录、动作针对整组"的模型改成"受管记录有三种归属、动作针对一条或多条记录"，为后续票提供公共底座，本票不新增用户可见功能，主备组行为保持不变。

- 受管记录归属三选一：主备入口组、全部发布组的成员、入口节点；同一条远端记录以服务商、规范化域名和记录 ID 标识，全局只能有一个归属，账号只是访问凭据，账号或凭据别名不能绕过唯一性（沿用 v1 `UNIQUE(provider,domain,record_id)`）。
- 入口动作逐条持久化：记录身份、绑定代次、旧值和新值、旧新启用状态、每条的确认结果；组级（以及节点级）期望版本保留，过时的自动动作按版本失效。部分成功逐条记录，未确认的那条先读回核对，不把部分成功标成整体完成。迟到结果按动作 ID、记录身份和绑定代次核对，不会写到新的绑定上。
- DNSPod 接口新增暂停、启用；改值显式带上记录原来的状态（`Record.Modify` 省略 `status` 时默认启用）；绑定和读取允许暂停的记录（主备组的受管记录仍要求启用）。服务商明确拒绝（例如 `500025` A 记录负载均衡超限）与结果未知分开：明确拒绝记为失败并通知，结果未知先核对。
- 解绑、更换记录：旧记录保持原状并提示；该记录有未完成动作时拒绝。
- 节点的上报地址与面板确认的地址分开保存；面板确认的地址只由受控换 IP 成功和手动同步改变。迁移时以现有公网 IP 初始化。
- 备份恢复的核对与放行改为逐条记录；旧版本迁移保留已有记录、期望版本、活动入口和未确认动作。
- 假 DNSPod 同步支持暂停、启用、省略状态默认启用、启用超限拒绝、超时。

**Blocked by:** 无

**Status:** ready-for-human

**Refs:** 规格「Implementation Decisions · 受管记录模型」、用户故事 19；ADR 0005、0007；`internal/panel/managed_dns.go`、`managed_record.go`、`entry_coordinator.go`、`recovery.go`、`recovery_api.go`、`internal/dnsprovider/`

- [x] 迁移后主备组的发布、切换、回切、被墙策略、跨组换 IP、偏差写回、备份恢复全部回归通过，假 DNSPod 写入次数与值不变（面板 HTTP API 测试台）
- [x] 两个本地账号使用相同凭据时，重复绑定同一远端记录被拒绝（面板 HTTP API）
- [x] 动作在途时解绑或更换该记录被拒绝（面板 HTTP API）
- [x] 底座边界测试：一个动作改两条记录、第一条成功第二条超时，重启后第一条保持已确认，第二条先读回核对，不重发第一条
- [x] 底座边界测试：改一条暂停记录的值后它仍是暂停；假 DNSPod 在省略状态时会启用，用来证明面板显式传了状态
- [x] 底座边界测试：启用被 `500025` 拒绝时记为明确失败，不当成结果未知重试
- [x] 底座边界测试：迟到结果按动作 ID、记录身份和绑定代次核对，不会写到新绑定
- [x] 迁移测试：已有记录、期望版本、活动入口、未确认动作完整保留；旧备份恢复后逐条核对，历史动作不重放
- [x] 新增模式的多记录、暂停记录与部分成功的 HTTP 集成验收由票 03、04 完成，本票不为验收新增产品 API

## Comments

2026-11-10 实施记录（分支 `sg/01`；仅在指定工作树实施，未创建或切换分支、未创建工作树、未 push；未修改规格、术语、ADR、其他票及票 02 的规则入口领域）：

- **归属与绑定**：`managed_records` 新增稳定 ID、`group/member/node` 三选一归属、绑定代次和启用状态，保留 `UNIQUE(provider,domain,record_id)`；共用绑定事务先规范化域名，再按远端身份判断唯一性，账号只作为凭据。换绑和解绑不调用 DNSPod，旧记录保持原状并返回提示；逐记录未完成动作通过事务检查及数据库触发器保护，组和节点删除同样受保护。主备组绑定仍要求启用。
- **逐条动作与执行**：新增 `entry_action_records`，保存身份、代次、旧新值、TTL、旧新启用状态、写入与启用步骤、逐条确认及通知标记；保留原组级期望版本，新增 `node_publications` 节点级期望版本。原主备提交入口适配为单条明细，保持原写入次数和值。协调器先启用目标、确认成功后才允许暂停最后的原记录；未知启用不计入保底，先读回再续跑。部分成功持久化，重启跳过已确认记录；明确拒绝终结对应明细，不自动重试，事件和 Telegram 通知独立续投。迟到结果需匹配动作 ID/版本、远端身份、绑定代次和未终结状态。
- **DNSPod 与替身**：服务商接口新增 `SetRecordEnabled`，使用 `Record.Status`；改值时 `Record.Modify` 显式传入读回的启用状态，暂停记录改值仍保持暂停。有效失败码归类为 `RejectionError`，网络超时和损坏响应仍为结果未知，错误不带服务商原始消息。假 DNSPod 移入可供包内边界测试复用的 `internal/clusterharness/fakedns`，原测试台类型保留兼容别名；补齐状态控制、`500025` 拒绝、响应丢失/超时及 HTTP 适配，并模拟省略 `status` 默认启用。
- **节点地址与恢复**：上报地址继续写 `public_ipv4`，面板确认地址独立保存为 `confirmed_ipv4`，普通上报只触发偏差通知，不改确认地址或 DNS；受控换 IP 首次成功更新确认地址，手动同步事务接缝留给后续票。恢复导入只采纳最新可信成功换 IP，不能以历史动作覆盖后来的手动确认。`recovery_records` 按记录身份、代次、地址和启用状态核对；组放行在最后复核事务内完成，记录缺行、代次变化或节点重新失效均不能沿用旧放行。恢复旧动作逐条封存，不重放 DNS。主备沿用实际 DNS 唯一对应已核对成员上报地址的规则；成员/节点记录使用面板确认地址。
- **验收第 1–3 项**：全仓测试台回归发布、切换、回切、被墙策略、跨组换 IP、偏差写回及备份恢复，未放宽原假 DNSPod 写入次数和值断言。`TestManagedDNSBindingReadsValueWarnsAndOwnsOnlyOneRecord` 验证相同凭据别名及 `EXAMPLE.COM.` 规范化后的重复绑定拒绝；`TestManagedDNSPendingActionProtectsBindingAndMembership` 通过既有删除组接口验收解绑保护，并验证在途更换记录返回 409、原绑定及写入次数不变。新增 `TestManagedDNSRejectedSwitchKeepsActualEntryMemberProtected`、`TestManagedDNSAuditRejectsPausedPrimaryStandbyRecord`、`TestManagedDNSRebindAfterRestoreCanBeReconciledAndPublished`，补强明确拒绝后的真实旧入口保护、主备暂停记录核对及恢复后换绑再发布。以上业务验收均走面板 HTTP API 与假 DNSPod。
- **验收第 4–7 项**：包内底座测试 `TestManagedRecordActionRestartKeepsConfirmedRowsAndReadsUnknownRow` 验证两条动作第二条超时后重启仅多一次读回，写入总数仍为两次；`TestManagedRecordPausedValueUpdateStaysPausedThroughRealDNSPodAdapter` 经过真实 DNSPod HTTP 适配器证明暂停改值不误启用，配合 `TestFakeDNSPodModifyWithoutStatusEnablesAndStatusRejectionDoesNotChangeRecord` 证明替身默认行为。`TestManagedRecordEnableRejectionKeepsPartialSuccessAndNotifies` 验证 `500025` 明确失败、部分成功、通知及不重试；`TestDNSPodStatusRejectionIsDefinitiveAndTimeoutIsUnknown` 验证明确拒绝与超时分类；`TestManagedRecordLateResultCannotUpdateNewBindingOrDifferentAction` 验证迟到保护。另以 `TestManagedRecordLastEnabledRecordWaitsForConfirmedEnable`、`TestManagedRecordUnknownEnableIsReadBackBeforePausingLastOriginal` 验证先启用后暂停及最后记录保护，以 `TestManagedRecordNodePublicationHasOwnVersionAndExplicitEnableStep` 验证节点独立版本与首次发布的明确启用步骤。
- **验收第 8–9 项及地址边界**：`TestManagedRecordMigrationPreservesBindingsPublicationsAndPendingActions` 验证 35→37 后原绑定、组期望版本、活动入口、未确认动作及写入标记、回切通知标记和暂停状态完整保留，并以原公网 IP 初始化确认地址。`TestManagedRecordRestoreArchivesRecordRowsWithoutReplayingOldAction`、`TestManagedRecordRestoreChecksEachAddressAndStatusBeforeReleasingGroup`、`TestManagedRecordRecoveryRechecksRecordsEvenWhenGroupFlagIsStale` 覆盖旧备份逐条核对、封存、不重放，以及旧组标记已放行但记录缺行/未确认/代次变化的边界。`TestManagedRecordReportedAddressDoesNotReplaceConfirmedAddress`、`TestManagedRecordRecoveryConfirmsLatestControlledAddressWithoutRollingBackManualSync` 和增强的 HTTP 用例 `TestManualRotationObservesAddressAndRegionThroughAPI` 验证地址分存与受控更新。本票未新增产品端点；新模式的多记录、暂停、部分成功 HTTP 集成验收留给票 03/04。
- **TDD 与独立审查**：先观察暂停改值、重启核对、明确拒绝、最后记录保护、恢复放行等语义 RED，再实现至 GREEN。全仓验证暴露的传播窗口旧 TTL 读取和恢复后新建组继承暂停回归已分别复现并修复：传播使用写前实际旧 TTL，恢复后的全新手动绑定采用本次读回证据。使用 `requesting-code-review` 进行只读独立审查，多轮复核后无剩余明确的重要问题；未生成额外审查文件。
- **歧义决定**：无新增产品解绑或手动同步端点；主备解绑保护使用既有组删除接口验收，成员/节点页面后续调用共用解绑事务并展示返回提示。保留主备恢复及启用要求；允许暂停绑定的是新成员/节点归属。旧恢复清单内换绑须按新代次重新核对，全新手动绑定已有新读回证据，不继承旧备份暂停。迁移用已有公网 IP 初始化确认地址，新节点在首次受控确认或后续手动同步之前保持空确认地址，不以普通活性上报暗中确认。无其他规格行为偏离。
- **迁移与协议**：在 `internal/panel/migrations.go` 的 `crossGroupRotationMigration` 后追加 **迁移 37 `managedRecordModelMigration`**（原为 36，合并时排在票 02 的第 36 版之后），SQL 定义于 `internal/panel/managed_record_model_migration.go`；SQL 不依赖迁移编号，可在合并票 02 时平移。未修改 protobuf 或 `api/`，未运行 `make proto`；既有节点、动作、发布及恢复 JSON 读取增加确认地址或逐条状态字段，没有新增用户可见功能端点。
- **最终验证**：`env -u FORCE_COLOR GOMAXPROCS=2 GOFLAGS=-p=1 make test` 完整通过，包含前端构建、节点内嵌包重建、`go vet` 与全仓 Go 测试；最终测试台 299.910 秒，面板 37.436 秒。`env GOMAXPROCS=4 go test -race -p 1 ./internal/panel/... ./internal/dnsprovider/... -count=1 -timeout=20m` 完整通过，面板 268.197 秒、DNS provider 2.223 秒，无数据竞争报告；`gofmt`、`git diff --check` 通过。验证过程中既有 `TestProbeMinuteAverageMaximumMissingFieldsAndPersistence` 曾出现探针新鲜度等待超时，单独执行 `env GOMAXPROCS=2 go test -p 1 ./internal/clusterharness -run '^TestProbeMinuteAverageMaximumMissingFieldsAndPersistence$' -count=3 -timeout=3m` 连续三轮通过（1.301 秒），未修改探针/节点生产逻辑；最终完整测试亦通过。未出现需重跑的 `TestRotation*` 失败。Vite 仅有既有大体积分块提示。
- **产物与提交**：提交前已执行 `git checkout -- web/dist internal/nodebundle`，并删除本轮新增的未跟踪前端生成文件；生成产物不纳入提交，`web/src` 的旧记录保持原状提示正常提交。代码提交为 `fb5d288`（扩展 DNSPod 状态控制并补齐测试替身）和 `a3e51ca`（建立受管记录逐条动作与恢复核对底座）；本票实施记录与人工清单统一一次中文文档提交。无已知阻塞交付的开放问题，Linux 人工验收待执行；合并后由集成人员统一 `make build`。
- **票 03/04 接缝**：`bindRecordTx` / `unbindRecordTx` 管理三种归属、代次、唯一性及旧记录提示；`ManagedRecordChange` 携带记录 ID、绑定代次、目标值、启用状态及 `ExplicitEnable`（空值表示保留当前地址）；`RecordActionRequest` 选择组或节点归属，携带 `ExpectedVersion`、逐条变更及 `MinimumEnabled`，通过 `submitRecordActionTx` 同事务校验版本并落盘。调用方负责策略和授权，沿用现有锁顺序 `entryMu → controlMu`，不得持 `controlMu` 反向获取 `entryMu`；事务提交成功后调用 `p.coordinator.wake(actionWorkerKey(a))`，节点 worker 键为 `node:<id>`。组动作至少保留一条确认启用记录，全部发布应在同一动作列入先启用后暂停的变更。`confirmNodeAddressTx` 供手动同步与相应逐记录动作同事务提交。`loadActionRecords`、`recovery_records` 及既有 JSON 读取供后续页面显示逐条结果；集合传播及新模式产品策略留给票 03/04。

Linux 人工验收清单（以下**尚未执行**，自动化与替身通过不能代替真实环境验收）：

1. 合并票 02 并协调迁移编号后统一 `make build`，在 Linux 上先备份第 35 版数据库，再升级；核对原受管记录、期望版本、活动入口、在途动作与节点确认地址初始化结果。按既有流程回归主备首次发布、手动/故障切换、回切、被墙策略、跨组换 IP、偏差写回，核对真实 DNSPod 写入记录和值。
2. 使用隔离的真实 DNSPod 测试记录核对暂停、启用、暂停时改值和线路保持；在有条件的账号下触发 `500025`，核对明确失败、部分成功及通知，不反复重试。阻断服务商响应制造超时，确认先读回核对；新模式操作页面与多记录 HTTP 验收须待票 03/04 接入后补做。
3. 动作在途时尝试换绑或删除组，确认拒绝；动作结束后换绑或解绑，核对旧远端记录保持原状且有提示。重启面板以及恢复含未确认动作的旧备份，确认历史动作不重放，逐条核对完成前不能自动写，失败原因可在恢复状态中读取。
4. 从节点外部改变公网地址，确认面板上报地址更新、确认地址保持原值、只提示偏差；执行受控换 IP，确认成功后更新确认地址。待票 03/04 接入手动同步后，补验手动同步与逐条动作同事务提交，以及旧备份中的历史换 IP 不覆盖后来的人工确认。
5. 检查事件与 Telegram 通知中的记录、动作和失败码；断开通知通道后恢复，确认通知续投且不会重发已明确失败的 DNS 写入。核对合并构建后的前端资源与节点内嵌包版本。
