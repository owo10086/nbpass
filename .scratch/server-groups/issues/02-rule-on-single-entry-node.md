# 02: 规则直接挂单个入口节点

**What to build:** 转发规则的入口改为"入口节点或入口组"二选一，数据库、API、导入都拒绝两者都填或都不填。新增一个"规则的实际入口节点集合"函数，以下所有按入口组成员展开的地方都改用它：配置同步、就绪（按"节点 × 入口"）、隧道授权与凭据轮换（`tunnel_snapshots.go`、`tunnel_rotation.go`）、流量来源登记与实时连接数（`traffic.go`）、随机端口（`random_port.go`）、端口冲突与环路展开（`internal/forwarding/validation.go`）、节点删除引用（`node_references.go`）。规则 JSON 格式升到 2，新增单节点入口字段，导入同时接受格式 1。规则页表单可选入口节点或入口组。挂在单个节点上的规则没有入口切换，客户端连接节点地址。

**Blocked by:** 无

**Status:** ready-for-human

**Refs:** 规格用户故事 10–13；GLOSSARY「转发规则」「就绪」；ADR 0006

- [x] 直连和隧道规则都能挂入口节点，节点应用并监听成功，隧道经出口握手成功
- [x] 同一节点上单节点规则与入口组规则端口冲突时保存被拒绝；节点加入入口组造成冲突时加入被拒绝；随机端口避开两者
- [x] 单节点规则与组规则混合形成环路时被拒绝
- [x] 规则在入口组与入口节点之间改动：旧入口撤销、新入口应用；在线出口撤销旧入口授权并断开其已有流；离线旧入口重新上线后补报的流量不丢
- [x] 隧道凭据轮换覆盖单节点入口
- [x] 单节点规则的字节数与连接数正常累计；组与节点互改后累计连续
- [x] 就绪按节点 × 入口计算，单节点规则出错不影响该节点所在入口组的就绪
- [x] 删除承载单节点规则的节点被拒绝并指出规则
- [x] 格式 1 文件可导入；含单节点规则的导出再导入往返一致；同时指定或都不指定入口的导入整批回滚
- [x] 迁移后已有规则入口全部为原入口组，行为不变
- [x] 以上行为由测试台通过面板 HTTP API 与真实节点验证

## Comments

2026-11-10 实施记录（分支 `sg/02`，基线 `d65096d`；仅在指定工作树内实施，未创建或切换分支、工作树，未 push；未修改总规格、术语、ADR、其他票或票 01 的领域代码）：

- **统一入口模型与接缝**：`forwarding.Rule` 新增 `entry_node_id`，与 `entry_group_id` 二选一。`internal/forwarding/model.go` 的 **`RuleEntryNodes(r Rule, members map[string][]string) []string`** 是规则实际入口节点集合的唯一解析函数：节点入口返回该节点，组入口保留原成员顺序；函数不判断启停，调用者按各自用途过滤启用规则，返回切片只读。`internal/panel/rule_entries.go` 集中读取成员关系、验证入口引用，并为隧道轮换取当前关联规则的实际入口并集。票 03 可直接复用此函数与成员关系读取接缝。
- **展开使用点核对**：配置快照、逐规则同步、隧道授权代次、出口 grants、旧版授权兼容、轮换准入与确认名单、历史流量来源、实时连接数、随机端口、端口冲突和环路、节点删除引用均接入实际入口集合。实时连接数复用已解析的同步成员，每条规则一次集合查询，历史入口仅参与补报。逐一检索 `entry_group_members`、`entry_group_id`：保留的直接组查询属于组成员管理、组发布/切换/恢复、组同步归属和历史迁移，不作为规则节点展开使用；自动换 IP 的单节点用途由票 03 接续。
- **数据库与迁移**：在 `internal/panel/migrations.go` 列表末尾追加 **第 36 版 `singleEntryRuleMigration`**，定义位于 `internal/panel/single_entry_migration.go`。重建 `forward_rules`，两个入口字段均为可空外键，以 CHECK 强制只填一个非空入口；API 中未使用的入口写为 SQL NULL，导出省略未使用的入口字段。旧规则显式拷贝原入口组，新增节点入口为 NULL。重建前暂存六张级联子表的数据，重建后在同一事务回填，保留累计量、补报游标、实时样本、历史来源、授权高水位和确认；子表结构、外键及索引保持原样，规则表保留主键并新增两个入口索引。迁移文本不依赖编号，测试以 `crossGroupRotationMigration` 标识确定旧基线，可在合并时平移。
- **配置、授权与流量连续性**：规则 ID 不变，入口类型或节点集合变化时重新编译旧、新入口完整快照与出口授权；同一节点在组入口和节点入口之间互改也更新授权代次。组入口的授权指纹保持原有序列化形式，新节点字段仅在节点入口时进入指纹。沿用 ADR 0006：离线入口继续缓存转发，在线出口按当前逐规则授权阻止新流并主动断开旧流；重新上线后通过完整快照收敛，历史来源仍能补报，累计字节不重置。凭据与证书轮换覆盖单节点入口，离线单节点仍参加确认，不能提前撤下其旧材料。
- **就绪与歧义决定**：按“节点 × 入口”计算；同一节点承载的全部单节点规则视为同一个节点入口，使用 `node:<节点 ID>` 的独立就绪分桶，与其所属各入口组隔离。停用规则不占监听、不阻塞就绪，但仍阻止删除引用节点，沿用既有语义。规则 API 新增 `entry_states`，暴露该入口的逐节点就绪及原因；读取不会制造候选样本。单节点入口没有 DNS 发布或入口切换，客户端使用节点地址；不实现票 03 的自动换 IP 或票 01 的节点地址分存、受管记录功能。
- **规则 JSON 与页面**：导出统一为 **格式 2**，使用 `entry_node_id` 或 `entry_group_id`；导入接受格式 1、2，格式 1 入口只能为入口组。沿用严格 JSON、引用/冲突/环路整体预检、确认时重新校验、整批回滚和原规则 ID 覆盖语义；节点与组均填或均不填时整批拒绝。`web/src/Rules.tsx` 增加入口类型及节点选择，列表显示入口类型，编辑和启停保留相应入口字段，提交剔除表单专用的入口类型；`RulesTransfer.tsx` 更新格式和引用说明。沿用 DESIGN.md 的组件、表格与表单风格。
- **协议**：未修改 protobuf、生成协议源码、节点控制协议版本、WSS/yamux 或节点执行器。面板在编译时已确定快照所属节点，单节点规则的既有 `ForwardRule.entry_group_id` 留空即可，沿用现有快照校验、缓存、授权代次和撤销机制。HTTP 规则定义新增 `entry_node_id`，规则文件版本升级为 2，旧格式 1 保持可导入。
- **验收与 TDD**：新增九个 HTTP/真实节点测试台场景及一个数据库迁移场景。首轮观察到单节点规则因不识别字段返回 400、格式 2 被拒、迁移缺少新字段的 RED，随后实现至 GREEN；就绪状态的第二轮 RED 为规则 API 缺少单节点就绪输出。所有业务管理与状态断言通过面板 HTTP API，使用真实面板、SQLite、节点、TLS/gRPC、TCP/WSS/yamux；仅随机源、物理监听隔离、目标回显和断网等环境接缝注入。
  - 验收 1、4、6：`TestSingleEntryDirectLifecycleAndTrafficAcrossEntryChanges`、`TestSingleEntryTunnelRotationAndOfflineRevocation`、`TestSingleEntryOfflineTrafficBackfillAcrossEntryTypes`（节点→组与组→节点），覆盖真实字节、连接数、旧监听撤销、新入口应用、在线出口断开旧授权流、离线补报和累计连续。
  - 验收 2、3、8：`TestSingleEntryValidationConflictsLoopsAndReferences`、`TestSingleEntryRandomPortSharesGroupPortSpace`，覆盖共享端口空间、成员加入冲突回滚、两个方向的随机端口避让、混合入口环路和删除引用。
  - 验收 5：`TestSingleEntryTunnelRotationAndOfflineRevocation`、`TestSingleEntryTunnelRotationWaitsForOfflineEntry`，覆盖证书与凭据轮换、离线单节点确认名单、旧材料可用及恢复收敛。
  - 验收 7：`TestSingleEntryReadinessIsolatedFromGroup`、`TestSingleEntryTunnelReadinessRequiresHandshake`，覆盖监听失败隔离、规则 API 就绪原因和真实获授权出口握手。
  - 验收 9：`TestSingleEntryJSONFormatsRoundTripAndAtomicRejection`，覆盖格式 1 导入、格式 2 节点规则往返、非法入口整批回滚且不推进配置版本；同时保留原有 JSON 严格解析、授权门禁与预检回归。
  - 验收 10：`TestSingleEntryMigrationPreservesRulesAndDependentRows`，覆盖原入口组、六张依赖表、累计量与授权高水位、入口 XOR、节点外键、`foreign_key_check` 和删除级联。以上测试台场景对应验收 11。
- **独立审查**：使用 `requesting-code-review` 做只读独立审查，未发现剩余 Critical / Important / Minor；补充复核确认未发现本票改动改变旧纯组入口缺报场景的具体路径。未生成额外审查或交付文件。

- **最终验证**：`env -u FORCE_COLOR GOMAXPROCS=2 GOFLAGS=-p=1 make test` 最终全绿，包含 npm 依赖安装、TypeScript/Vite、Linux 内嵌节点包重建、`go vet ./...` 和全仓测试，测试台 **304.475 秒**；`env GOMAXPROCS=4 go test -race -p 1 ./internal/panel/... ./internal/forwarding/... -count=1 -timeout=20m` 全绿，面板 **237.129 秒**、转发模块 **12.831 秒**，无数据竞争报告。全部改动 Go 文件通过 `gofmt` 检查，`git diff --check` 通过。Vite 仅有既有 AntD 大体积分块提示。
- **验证期间的既有失败**：首轮完整 `make test` 唯一失败为 `TestEntryOfflineMissingCheckerResultCannotConfirmFailure`（`entry_offline_test.go:364`，检测点缺报场景中 DNS 写入次数不等于 1；首轮测试台 309.232 秒）。随后 `env GOMAXPROCS=2 go test -p 1 ./internal/clusterharness -run '^TestEntryOfflineMissingCheckerResultCannotConfirmFailure$' -count=3 -timeout=3m` 连续三轮通过，最终完整 `make test` 重跑通过。未修改该用例、离线检查或入口切换生产逻辑；未能稳定复现，根因尚未确认，不将重跑通过当成已定位根因。两轮全仓检查都未报告本票新增场景失败，也未出现 `TestRotation*` 失败。
- **产物与后续接缝**：提交前已执行 `git checkout -- web/dist internal/nodebundle`，并清除本轮新增的未跟踪前端资源，仅提交源码、测试与本票记录。票 03 复用 `forwarding.RuleEntryNodes` 判断单节点入口用途，可复用规则 API 的 `entry_states` 和面板的 `ruleEntryKey` / `loadEntryGroupMembers`；节点地址与受管记录仍由对应票接续。与票 01 合并时由集成者重排迁移编号，之后统一 `make build` 重建产物，再执行下面的 Linux 与页面人工清单。无本票功能偏离或尚未完成的自动化验收项；真实 Linux 与浏览器人工验收尚未执行。

- **提交**：后端实现与测试 `1526daf`（支持规则绑定单个入口节点并统一实际入口展开）；规则页 `a69e523`（规则页支持选择入口节点或入口组并显示入口类型）。本轮票据文档统一单独一次中文提交，未 push。

Linux 人工验收清单（以下全部尚未执行，不以测试台通过代替真实部署与页面验收）：

1. 合并并重排票 01、02 的迁移编号后统一 `make build`；备份已有 Linux 面板数据库再升级。核对已有规则仍指向原入口组、监听端口/启停/出口不变、累计流量连续，检查 SQLite 外键完整性；新安装与旧库升级均能启动。
2. 接入两个真实 Linux 入口和一个出口；不创建入口组，分别创建单节点直连、隧道规则，通过入口节点地址与监听端口完成真实 TCP 传输，检查同步版本、上/下行累计及当前连接数。单节点规则应没有入口切换或 DNS 发布操作。
3. 将同一入口加入入口组：单节点和组规则使用同端口应拒绝，另一节点加入组后造成冲突也应拒绝并保留原成员；随机端口避开两种规则、检测端口和隧道监听。构造节点/组混合环路，保存应拒绝并给出具体节点、端口与规则。
4. 保持长连接，将规则在入口节点与入口组之间双向修改；确认旧入口撤销、新入口应用且累计连续。断开旧入口的控制连接后修改隧道规则，在线出口应拒绝旧授权新流并断开旧流；恢复旧入口后补报离线字节、收敛撤销，不能重复计账或漏掉合法补报。
5. 对承载单节点规则的出口分别执行证书、凭据及两者轮换；已有流保持，新流可用。单节点入口控制离线时确认名单必须保留它、继续保留旧材料；恢复后轮换完成。占用单节点规则的监听端口或阻断其出口握手，规则就绪应显示对应原因，同节点其他入口组的就绪不受影响。
6. 导入格式 1 的组规则、导出格式 2 的节点规则并覆盖导回；前者可用、后者定义一致。将同批一条规则改为节点与组同时填或都不填，整批应拒绝且规则数、配置版本不变；预检通过后再制造冲突，确认导入时仍应重新拒绝。尝试删除仍承载启用或停用单节点规则的节点，须列出规则引用。
7. 经生产 HTTPS 代理，在桌面和窄屏检查入口类型切换、节点搜索、组选择、编辑/启停、入口类型列表、随机端口、导入错误、同步展开、空列表、长名称及会话失效。切换入口类型后只提交当前选择，不残留另一种入口；节点入口应明确提示客户端连接节点地址。
