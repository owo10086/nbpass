# 09 IX 整体收尾、术语、构建产物与全量验收

Status: resolved（本机可执行交付完成；页面细项及真实网络/生产演练待验，未发布）
Blocked by: 01, 02, 03, 04, 05, 06a, 06b, 06c, 07（两个完成边界）, 08
Migration: 无（核对最终迁移 47–51，禁止另占或重排）
Parallel: 所有功能票完成后串行收尾；本票独占正式术语/设计文档更新和最终发行产物生成。

## 目标

按原 spec 与 ADR 0006/0009 验收 IX 完整功能，统一术语和设计说明，构建并提交面板嵌入前端及 Linux 节点包，完成完整测试和人工验收交接。

## 具体改动点

- 逐条对照 `../spec.md` 核对：服务器公网/专线/对外端口、换 IP 隔离、组成员选地址、三种转发模式、共享链、多跳逐跳主备/认证授权、防环/引用保护、源 IP、本机地址应用错误、旧节点逐端口门禁、离线缓存。发现与已定决策不一致时修实现，不能通过改 spec/ADR 消除不一致。
- 更新 `GLOSSARY.md` 的界面模式和 Forward Rule/Tunnel/Exit Group/健康等相关定义，增加公网地址、专线地址、对外端口、转发链、出站 IP；中间跳不能一概称为最终出口。只描述本次已实现的主备能力，不把既有术语中尚未实现的最小连接数/fallback 当作 IX 交付。
- 更新 `DESIGN.md`：chains 从 PlannedPage 移为实际页面，记录端点地址编辑、组成员选址、三模式、完整路径及逐跳历史健康的真实交互；沿用既有 token 和组件，不另建视觉系统。ADR 0009 决策保持不变，确需澄清实现措辞时不得改动决策。
- 迁移清点：原生产基线 `user_version=46`，47=02 地址、48=03 成员地址、49=04 出站 IP、50=05 链模型、51=06a 链规则/逐 grant 结果；01/06b/06c/07/08/09 无迁移。`migrations.go` 仅在 46 后追加。验证新装和 46 升级到最终版本，原规则/证书/密钥/授权高水位/流量依赖数据及外键完整。
- 核对 protobuf 源码与 `make proto` 生成物一致，字段 280/281/284/285–287 按所属消息唯一，不复用旧号；能力声明分别为 02 的 ix_address_dialing、03 的 ix_member_identity、04 的 outbound_ipv4、06c 的 forward_chains，06a/06b 不提前声明。不用 software_version 代替能力；旧节点兼容必须检查收包，包含 02 版节点 × 03 版面板及备用升级重新确认。
- 在 `docs/deploy-linux.md` 或该文链接的 IX 部署说明补逐边步骤：列出每个前驱能访问的后继 IPv4/域名与对外 TCP 端口，配置「对外端口→后继本机隧道监听端口」映射，检查 NAT/运营商映射、主机/网络防火墙 TCP 放行，再逐边验证真实连接/证书和业务通过。说明域名由前驱仅解析 IPv4、证书按内部身份、目标只由末跳访问/源 IP 需已在末跳本机；面板不代建映射或配置路由。
- 部署人工网络验收交付项：从每个实际前驱验证后继地址/外部端口可达，核对映射落点与本机监听、TCP 放行、各跳主备及全链业务；记录实际已验/待验到现有票，不生成额外日志/报告。明确对外端口不占用本机端口，外部端口与本机业务端口同号可用，实际隧道监听冲突拒绝。保留 `docs/deploy-linux.md` 的 NAT/多 IP 不启用自动换 IP 边界，不宣称 IX 让自动换 IP 支持这些部署。
- 在同一部署说明交付 A 的一次发布/回滚步骤：旧独立更新器 `internal/nodeupdate/install.go` 的 `CompatibleState` 会拒绝写格式 2 候选，不承诺面板在线升级可跨 1→2，也不放宽该保护。先确认无未完成/未恢复更新事务再停两服务，沿用现有同身份重新安装（重置 token 获取新票据）流程，替换并重启节点和独立更新器，保留 `node.key`、同身份缓存/事件等状态，不删除更新事务绕过恢复；验证读取 1 后原子写 2。回滚先停两服务，成套替换为经核验的旧版节点/更新器；旧版读取同一 `config.enc` 的 version 2 必须明确拒绝、退出且无业务连接，人工原样隔离不兼容缓存并保留密钥/凭据/撤销证据，再空状态启动，控制不可达继续停转发，重连按旧能力接收当前配置。禁止改写 version/恢复早期缓存，不把写盘失败或普通损坏当作自动清缓存理由；缓存含秘密，隔离路径仍限权限、不得公开。01–08 中间构建不生产发布，本次无过渡版。
- 最后统一运行 `make build`，提交 `web/dist/` 与 `internal/nodebundle/nbpass-node-linux-amd64.gz`；`bin/` 继续按仓库现有忽略规则处理。Makefile 的 build 会 npm ci/build 并重建嵌入节点包，不能只执行 npm build 后漏掉内置节点。
- 所有功能代码按功能用中文提交；本轮需求/规格/进度/测试/审查等文档变更统一整合为一次中文文档提交，不额外生成执行总结或报告。最终交付信息写聊天或更新现有票 Comments，遵守仓库文档约束。

## 迁移与兼容要求

- 不新增迁移、不改变已有编号；之前并行工作只能按 49→50 的固定顺序进入正式 registry，不能为消除冲突挪动生产版本。
- 保证旧规则运行不变、旧缓存可读取、新必需语义不会被旧节点忽略后降级执行；撤权离线延迟按 ADR 0006 明示，不改成授权到期机制。
- 新保存重复路径的 API 测试按 spec 拒绝，计数用例改合法路径；历史重复路径照原语义编译，核对无关保存/重连/授权确认不受阻，影响该规则的编辑才执行新校验。
- A 已由用户确定，最终交付核对 01/04/06a–06c 的读 1/2、写 2 实现与公共矩阵，再完成上节跨格式重新安装/人工回滚演练。用固定来源的实际生产回滚目标真实旧读取代码/旧二进制加载新版实际缓存（relay-only、直连/末跳源 IP、撤销检查点），离线必须明确拒绝且无业务连接，无越跳/无丢源限制/无授权复活；人工空启动与重连过滤、新版读旧缓存及同版本恢复均通过。不能用新节点自己测试自己；实际回滚目标若没有拒绝保证即阻断发布并报告，不改选 B。

## 测试要求

- 单元/前端：运行 `npm --prefix web run build`；运行 `npm --prefix web test -- src/EndpointData.test.mjs src/ChainsData.test.mjs src/RulesData.test.mjs` 并补上本轮其他新增 `.test.mjs`（package.json 当前默认只测 route.test.mjs，须显式纳入）；`make proto` 后检查生成差异；`git diff --check`。
- 完整运行 **`GOFLAGS=-timeout=30m make test`**，核对退出码与全部包结果，不用定向测试或缩减测试集替代。make test 包含前端构建、节点包、go vet 和 go test，不能因为默认 10 分钟超时而删慢场景。新并发代码的 race 定向验证按 06b/06c 要求完成。
- clusterharness 验收矩阵必须包含：A→B 专线域名/IPv4 + 真实外部端口映射；A→B→C→目标及再多一跳；每跳主备切换/恢复/全坏；换公网 IP 保留专线；直连/末跳源 IP 生效、非本机报错；旧节点在各位置收不到新语义且普通端口不受影响；逐跳撤权、离线重启、轮换和流量不重复。通过面板 HTTP API 及真实节点验证，允许隔离网络/时钟/接口枚举等环境接缝，不 mock 转发结果。
- 新增矩阵验收：非对称 A→B1 好/B1→C 坏/B2→C 好时切 B2 而本边不判坏；全链共享总超时/候选上限；双地址一好一坏与同 address_id 改地址后旧样本丢弃；单跳/链式末跳回环正反例；中间节点监听冲突拒绝、外部端口与本机业务端口同号接受。
- 复核 06c 的 operations/incidents：各跳节点反查受影响端口、逐边/grant/blocker、链 ID 不变但内容改动的历史边界、配置变更/新版本替代/异常解除各结束语义；控制离线或公网被墙但专线可用不误报业务中断。主备组与全部发布组→同链的仅 A1 专线失败/后跳全坏/恢复/控制离线，逐成员就绪、DNS/换 IP 归属及 ADR 0005 协调者动作记录均有断言。后端功能在 06c 完成，本票负责复核交付。
- 编排者按 spec 在本机 HTTP 回环面板逐页验收：转发端点、端点组、链管理、端口、服务器编辑；桌面/窄屏、错误/空态、历史健康、需升级提示。执行者若处于禁止浏览器的会话，只交接清单并保留人工项未完成状态，不能声称已验收，也不能擅自开浏览器。本次拆票不运行此项。

## 验收标准

- [x] 原 spec 每项均有实现与验证证据，已知风险及人工待验项如实列出。（本机实现/自动化及独立审查对照完成；剩余页面细项及生产验收按原票保留待验。）
- [x] 46→51 升级、新安装、旧节点/旧缓存兼容、外键和历史业务数据验证通过。
- [ ] A 的一次完整发布、同身份跨格式重新安装、真实旧版拒绝/停转发及人工空启动/重连过滤已演练，不承诺旧更新器直接跨格式在线升级。（本机真实旧程序和安装脚本接缝已通过；生产目标身份核验及真实Linux/systemd完整发行仍待验。）
- [x] 术语与 DESIGN 更新准确；ADR 决策不变。
- [x] make build 产物已提交，完整 GOFLAGS=-timeout=30m make test、显式前端测试及必要 race 检查通过。（全部构建/自动化及终审已通过；测试提交8d6105a，正式产物提交3e808da。）
- [ ] 编排者页面验收已完成；若未执行，交付明确保持待验，不误标整体验收完成。（已执行事实保留各原票Comments；08运维关联/事故跳转、02完整轮换及03双地址实时健康等未记录页面细项继续待验。）
- [ ] 逐边专线/NAT 部署步骤与人工网络验收已交付，自动换 IP 的 NAT/多 IP 边界保留；未实测网络明确待验。（部署步骤已交付；实际专线/NAT/防火墙/回程与主备切换待生产人工验收。）

## 不做的事

不新增业务能力、不部署/推送、不切换分支/新建工作树、不改已定决策；不把管理中转、Halo IX 安装、UDP、IPv6、智能跳数或监听绑定 IP 混入收尾。

## Comments

### 续接 1076 的实施约束核对（2026-11-12）

- 起点 `feat/ix@1f4b742`，工作区干净，01/02/03 已 resolved；已完整读取 spec、ADR 0006/0009、DESIGN/GLOSSARY/CLAUDE、01–03 证据及04–09全部票。顺序固定为04→05→06a→06b→06c→07（两个边界）→08→09；每票唯一实施者，验证后独立只读审查，重要问题修复后中文功能提交。全部本轮文档最后统一一次中文提交。
- 预检的共享接口与裁定：

| 票/接口 | 生产与消费关系 | 核对结果 |
|---|---|---|
| 04→05→06a 迁移 | 49 源 IP、50 链、51 规则/逐 grant 结果 | 严格追加，保留1–48及流量/授权子表；无占位或重排 |
| 04→06a 协议 | 284 源 IP；285–287 链/报告；沿用280/281 | 串行生成，真实 make proto 产物随功能提交 |
| 05→06a/07 路径 | 固定 hops、members、observations 契约 | 05 空观测、06a 真实引用、06c 真实证据；07 两个边界都通过才解锁08 |
| 06a→06b→06c | 相邻授权/指纹/预算→relay恢复→确认/就绪/事故/入口组 | 06a/06b 不宣告能力，06c 完整闭环后才宣告，不把06c功能推给09 |
| 01/04/06b/06c→09 缓存 | 读1/2、统一写2、真实旧读取器拒绝及人工恢复 | 策略 A 不变；生产回滚目标身份与真实网络/生产演练保留待验，不能混同本机自动化 |
| 07→08→09 前端/产物 | 链页→端口三模式→术语及统一发行构建 | 04–08不提交dist/nodebundle；09统一make build |
| 04、05、06a、06b、06c、07、08、09 各票内部要求 | 目标、测试、验收与不做范围逐票对照 | 未发现相互冲突；既定spec裁定普通实现细节，无需重复确认实施授权 |

- 工作限于指定现有目录/分支；不创建工作树、辅助报告/日志，不推送/部署/合并。记录只进入既有票 Comments，保护真实02二进制基线。浏览器由主编排者后续执行；真实专线/NAT及生产重装/回滚未执行时明确保持待验。


### 09 实施范围与逐 spec 核对（2026-11-12）

- 基线 `feat/ix@bddf428`，唯一实施者；01–08 已完成的实现及审查结论原样消费，不重复实现。原 10 份未提交票据保留，父会话追加的人工事实以原票最新 Comments 为准。全程不创建 worktree/分支/辅助报告/日志/ledger，不操作浏览器、部署、推送、合并或提交；源码/产物冻结后交根整体独立审查。
- 本票没有修改后端或前端业务实现、proto、迁移 registry、ADR 或能力声明。补充现有测试与正式 GLOSSARY/DESIGN/部署说明，最后统一构建。新增验收测试是补足跨阶段证据，没有人为删实现制造红；实际夹具失败及修正见下节。

| 原 spec 项 | 实现与验证落点（最终完整 make test 统一复核） |
|---|---|
| 公网/多专线 IPv4/域名、可空对外端口、前驱 IPv4 解析、监听分离与稳定 TLS 身份 | 02 `ix_addresses`、`tunnel_address_dial`；`TestIXAddressDomainMappedPortAndOldDialerIsolation` 真实 TCP 映射和 WSS/TCP，面板地址 API/前端 EndpointData |
| 换公网 IP 不改专线/映射/身份，不等待专线消费者 | 02/03 `rotateExitAddress`、地址跟踪；`TestIXAddressRotationPreservesDedicatedPortsAndIdentity` 与 IXMember 多地址真实矩阵 |
| 组成员服务器+地址、稳定 ID、组内唯一/被引用删除、两地址健康隔离与旧摘要丢弃 | 03 `ix_member_addresses`、端点摘要与 ExitGroups；真实 `TestIXMemberReal02`，`TestChainFailoverAddressIsolationLateDigestAndIncidentBoundary` |
| 三模式、共享链至少两跳/顺序/引用删除、所有候选防重复 | 05/06a/07/08 链模型/API/Chains/Rules；`TestChainRulesHTTPReferencesAndImport`、`TestChainRulesSharedEditChecksEveryDisabledReference`、`TestPortChainHTTPModesPathTransferAndTraffic` |
| 中间跳 relay-only、直接前驱认证、下一组/末跳最小授权，逐 grant 持久确认 | 06a–06c；`TestChainRulesAdjacentSecretsAndFingerprint`、`TestChainRelayWireAuthorizationRejectsForgery`、`TestChainGrantReportsRequireExactCompleteCurrentEvidence`、HTTP TerminalDisk/BackupUpgrade/Reconnect |
| 逐跳主备、更多一跳、非对称下游失败不污染本边、全坏/恢复 | 06b `TestChainRelayRealAndOfflineRestore`、`TestChainRelayAsymmetricFailover`；06c `TestForwardChainHTTPRealMultiHopAndOffline`、`TestChainFailoverAsymmetricEdgesAndRecovery`、EachHopCandidateRecovery |
| 单次总预算、最多三个不同成员、无业务重放/越跳 | `TestChainRelaySharedBudgetCancellation`、BudgetDecreasesAcrossSlowRetries、NestedBackupBudget、AtMostThreeDifferentCandidates、RSTDoesNotReplay，原06b race有效；未新增生产并发代码 |
| 直连/末跳源 IPv4、本机校验、恢复重新校验，传输不绑定 | 04/06b OutboundIPv4 与 RelayInvalidSourceOnlyBlocksTerminal/FinalSourceRestoreAndCheckpoint；真实默认源拨号、实际目标源观察及当前配置源消失/恢复，08 GrantFailureReason真实 API |
| 防环按真实末跳、本机监听冲突、对外端口同号可用，历史规则只校验受影响变更 | 05/06a `TestChainRulesStaticLoopsAndActualListeners`、`TestChainTargetDialerLoops`、ForwardChains API历史启用自跳/无关保存重连确认；原分段测试保留 |
| 旧节点逐端口门禁、可用主路径与备用升级后重新确认，不用版本字符串 | 四独立能力计划；01/03/06c真实收包和 `TestIXMemberReal02`、EveryOldPositionFiltered、BackupUpgradeWaitsForFreshDurableConfirmation；普通端口仍传业务 |
| 离线恢复/撤权延迟/在线端即时拒新流与1秒复查、轮换连续、流量仅入口计量 | ADR0006不变；HTTP OfflineRestartAndDownstreamRevocation、RotationWaitsForEveryDirectPredecessor、HTTPRuleTrafficCountedOnce，06b HalfCloseSlowIOAndTraffic/同代次撤授权 |
| 逐成员入口就绪、主备/全部发布、DNS/换 IP归属与ADR0005协调 | `TestChainEntryGroupPrivateFailurePublicationMatrix`、`TestChainEntryGroupOfflineAutomaticCandidateAndMinimumKept`；单纯专线未就绪不新增自动切换，后跳坏/控制离线不盲目换公网IP |
| operations/incidents中间/末跳反查、逐边/grant/blocker、同链内容换代、结束语义/新鲜恢复 | 06c ChainIncidents/FlowSuccess/ResourceGap配置归属及I1–I6最终审查闭环；08 Overview/PortSyncDetails/IncidentDetails及公开合约测试 |
| A读1/2写2、同文件/AAD、真实旧版拒绝/停转发、手工隔离空启动/重连过滤 | 01/04/06b/06c真实旧源码/完整旧二进制矩阵；09新增真实同身份重置票据/跨格式离线恢复与安装脚本双程序替换，见后续命令证据；生产身份/systemd仍待验 |
| 迁移严格原1–48+49源IP+50链+51链规则/逐grant，新装与整条升级保全 | `migrations.go` 对 `1f4b742` 仅末尾追加三项；新增 `TestIXFinalMigration46To51AndFreshInstallation` 一次46→51及空库0→51，20张旧表全部原列/密钥及FK/子表级联 |
| 界面/术语/逐边部署与发行产物 | 更新既有 GLOSSARY（三模式/地址/中间跳/健康/仅主备）、DESIGN真实交互、deploy-linux第7/8节映射/证书/源IP/一次发行与人工回滚；最终make build/完整make test及显式前端见下文 |
| 范围外能力不混入 | 管理连接仍直接到面板；不做Halo安装、UDP、IPv6、智能跳数、服务器级默认传输、监听绑定IP；NAT/多IP仍不启自动换IP |

### 新增整体保全与同身份演练

- `TestIXFinalMigration46To51AndFreshInstallation` 在真实 SQLite 执行原1–46、种入旧直连/单跳与原依赖后一次执行47–51，另从空库执行0–51。逐表比较**20张表全部原列**（含NULL、sort_order、ciphertext、证书、节点对凭据、快照、逐规则结果、退休进程、节点revision/恢复高水位、授权代次/确认及全部4张流量表），重开数据库与panel.key逐字节核对；新列默认值、公网成员迁移、新地址/链/逐grant可用、引用拒绝、FK检查及规则删除后七张子表级联均断言。保留已有46地址、50→51分段回归。
- `TestIXSameIdentityActualV1ToV2Reinstallation` 从固定旧提交真实构建并运行旧程序，真实TLS兑换/控制同步、两条直连业务后删除其一，确认撤销完成再停旧进程。**实际旧程序自己写格式1，绝不改写version或合成State**。原节点token reset得到新票据，真实当前可执行程序enroll-only；六份实际文件node.key/config/events/traffic/rotation/self-check逐字节保留，身份/控制URL不变、token改变，票据重用被拒。控制TCP代理真实拒连后启动新版，等待本地初始化完成的首次控制拨号；实际加密文件转为2、高水位/摘要/实际规则保留、有效规则传真实字节、撤销端口无监听、事件保留。停新程序后实际旧程序拒2退出，两个端口均不恢复，无临时文件或格式1副本。
- 安装脚本原隔离测试扩展成套替换：确认无pending，按人工前置步骤停止两服务，用另一完整候选程序执行真实安装脚本，核对节点/更新器两位置都被替换、更新器先于节点重启、node.key/cache/events/traffic/rotation/progress及drop-in保留。Linux账户/文件所有权/systemd由既有物理接缝替代；**这不是实际Linux systemd服务演练**。原FixedInstaller未恢复事务/恢复失败可重试测试保留，无删除事务或放宽CompatibleState。
- 首次迁移定向退出0，panel **1.580s**／墙钟 **3.12s**；迁移+安装脚本增强组合退出0，panel **5.213s**／墙钟 **6.69s**。重装初轮退出1（clusterharness **10.448s**／**11.95s**），新增夹具错误使用harness预留监听器给外部旧进程，旧进程真实bind冲突；改为外部进程可绑定空闲端口。第二轮退出1（**4.820s**／**6.25s**），缓存已成功转换但夹具在rotation初始化写盘中就发停止信号，context取消被报告为启动错误；补首次被拒控制拨号作为初始化完成屏障，不改生产实现或放宽退出断言。最终原场景退出 **0**，clusterharness **4.594s**／墙钟 **6.05s**，旧程序SHA再次与既有基线一致。

### 协议、基线与公共缓存复核

- 真实 `/usr/bin/time -p make proto` 退出 **0**，墙钟 **4.59s**；随后 `git diff --exit-code -- internal/controlpb` 退出0，两生成文件SHA前后完全一致：control.pb.go `369fa25be52aa32212db7a685ef28642b7168f7275be74b3f64db2c880d32ea6`；control_grpc.pb.go `e54a186e7b2639ac832ef4f31e3db037b548ee60a9193e70b7015486551e537a`。按所属消息核对280/281地址身份/摘要、ConfigSnapshot284源IP、ForwardRule285–287链/跳/可信上下文、TunnelGrant285–287下一组/跳/后继许可、NodeReport285逐grant报告，无旧号复用。
- `internal/nodeupdate/install.go` 对实际旧提交diff为空且SHA仍 `7c19dcb4931d333b25f188006cf8f9f207b9edc88cf1b6fcc28bd2cabcf11def`；01三份真实旧源和02两份保留binary SHA逐项等于原票，未覆盖。固定旧源码 `3899928362a75b58ac74ff6844652d319760b656`、当前本机重建实际旧binary SHA **`32054cfe3cfe3c040ded228e1052c6f145d70eb847714f1d60540e9968c18acd`**。
- `/usr/bin/time -p go test ./internal/node -run TestActualV1ReaderRejectsNewlyPersistedCache -count=1 -timeout=5m -v` 退出 **0**，node **12.871s**／墙钟 **13.37s**：真实旧程序及真实旧源码Run八格（直连/单跳出口/链末跳源IP/relay-only×实际缓存/撤销检查点）全部明确格式拒绝、无业务监听/下一跳/目标连接；人工原样隔离后离线空Run不转发。末尾实际旧FixedInstaller配旧兼容包的写2候选拒绝也通过，旧程序未被替换。

- `/usr/bin/time -p go test -race ./internal/clusterharness -run 'TestIXSameIdentityActualV1ToV2Reinstallation|TestOutboundIPv4ActualV1ManualIsolationAndReconnect|TestForwardChainActualOldReaderRejectsHTTPNodeCachesAndCheckpoints|TestIXMemberReal02' -count=1 -timeout=10m -v` 退出 **0**，clusterharness **22.294s**／墙钟 **27.33s**，无race报告。覆盖新增重装代理/进程生命周期、真实HTTP节点缓存六格、旧程序拒绝→原样隔离→离线空启动→重连所有链位置/源限制过滤及兼容普通规则恢复，真实02节点IPv4/域名/映射三格与备用升级确认。此前06b/06c已通过的庞大race集合不重复，生产并发代码未变。

### 最终构建与首轮全量验证记录

- `/usr/bin/time -p make build` 退出 **0**，墙钟 **11.82s**：npm ci、完整TypeScript/Vite（3163模块、623ms）、Linux amd64节点、gzip嵌入bundle、本机面板及节点均完成。产物保留在web/dist与internal/nodebundle，bin仍按忽略规则；两个ix02固定产物没有覆盖。
- 随后串行 `/usr/bin/time -p node --experimental-strip-types --test web/route.test.mjs web/server-groups.test.mjs web/src/*.test.mjs` 退出 **0**，**56/56**、Node **87.872459ms**／墙钟 **0.11s**；无skip/cancel/todo。未与npm ci并发，明确纳入所有新增数据测试。
- 已启动完整 `/usr/bin/time -p env GOFLAGS=-timeout=30m make test`，按原Makefile执行web ci/build、bundle、go vet ./...和go test ./...，等待全部包和最终退出码后记录，不以定向结果替代。

### 人工验收与发布边界（以父会话原票最新记录为准）

- 本实施会话禁止浏览器，未执行任何浏览器或生产操作。02已有818px端点编辑/清空与稳定地址事实；03已有818×1175地址替换确认/取消、回填、所选地址搜索及引用删除409回滚事实；04已有同屏宽名称/出站独立保存、IPv6拒绝和清空事实。02/03/04剩余屏宽、双地址实时健康/排序、完整轮换证书和会话边界仍按各原票未验清单交接，不能把自动化替代人工。
- 07父会话最新补验已完成真实5节点非对称状态、恢复、控制离线历史、阻断GET后的本地老化，加上CRUD、401/428及390/1440静态布局；不再把这些列待验。带暂停真实单例包338.057s退出0，正式自动化集合仍用原15.063s证据。
- 08父会话最新补验已完成三模式切换/回填/隐藏引用、共享链跳转/计数、1440完整路径和逐边确认/非对称/控制离线、备用不足/整条阻塞、390×844表单、6500ms慢响应刷新及慢401返回登录；带人工暂停真实单例包812.967s退出0，正式HTTP45.528s与56项前端证据不替换。08未明确记录的运维关联/事故跳转、其他错误/会话428等交根按原票继续核对，不擅自标为人工通过。
- **仍待生产人工验收**：实际每个前驱到后继的专线/运营商NAT映射、IPv4解析与TCP放行、真实证书部署/回程路由/源IP及逐跳主备/全坏/恢复；实际生产回滚目标与已测固定源码/对应平台二进制的身份核验；一次完整发行、真实Linux/systemd两服务无pending/recovery绕过的同身份重装，以及实际目标旧包拒2/停转发/人工隔离空启动/重连过滤。已交付deploy-linux第7/8节具体步骤，本机自动化及脚本接缝不冒称这些生产项完成。
- 保持09 `claimed` 及整体验收复选框未勾，待完整验证退出与根独立审查。未提交产物或任何文档；最终源码/产物中文功能提交及本轮全部md唯一中文文档提交由根统一完成。


### 最终产物核对（首轮完整测试期间）

- 脚本把当前 `migrations.go` 仅移除末尾49/50/51三行后，与 `git show 1f4b742:internal/panel/migrations.go` **逐字节完全一致**，原1–48顺序与内容未改变。三份旧reader源码还逐字节对比固定历史提交原文件通过，不只相信原记录SHA。
- gzip实际解压字节与 `bin/nbpass-node-linux-amd64` 完全相同，前端index.html引用的全部六份asset存在；`bin/nbpass-node --update-check`实际输出读1/2、写2和四个IX能力，软件版本没有参与能力判定。正式构建与make test的重复web/bundle构建产物一致。
- 最终产物SHA256：嵌入gzip **`8800bb225d3d1e6f191af6fbb32abe7c37d03d64905cb78f3a7223844c26710b`**；Linux节点 **`d2342d8b2a75244314dc4398b1074d37f3c62a936d00ff201b7ff964c5cc664b`**；本机面板 **`4b5b03dc0b0813d6a380221b526be694f1ce5192e5baec55470c7ab08586e1a4`**；本机节点 **`7c7aacfe7bb66f4ac20f28184213f490fffb99574321dd12bf966192d726719d`**。bin仍忽略不提交，真实ix02两份SHA仍分别为原 `bed24386…03058` / `ef3ad98e…51b81`。
- 新前端主JS `index-Cy4jQ0PE.js` SHA **`d8e437532f13697d3937a196caa349f135dbb7197f69cea179a12d49818f49dc`**；主CSS `index-BhEznON1.css` SHA **`b63f3fa9922125cccb1b7179a4ad78ed13a1bdaef6e73fd19ee36322e3970abd`**；antd新JS `antd-DEdBxCuQ.js` SHA **`78c9c01472abb17ff9bd49b06274ff24a2193f337f1c544337c2a22ce08ee16a`**。旧三个hash资源由Vite正常替换，保留未变化React/runtime/antd.css，index.html更新引用。
- 阶段 `git diff --check` 退出0，三份修改测试 `gofmt -l` 无输出。构建只保留正式dist与nodebundle，不生成新增仓库辅助报告或日志；Node颜色环境和Vite既有大chunk提示不是失败。

### 根会话独立复核与额外界面事实（2026-11-12）

- 根另行安排 requesting-code-review 只读审查，范围为基线bddf428之上09最初三处新增测试、DESIGN/GLOSSARY/部署文档与spec/ADR对照，结论Critical0/Important0/Minor0。该结论不预先涵盖随后完整make失败项修复；这些改动仍需执行者终审。
- 根独立执行 git diff --check 退出0，两份ix02保留binary SHA与02基线完全一致。根补验02桌面/390布局、切服务器字段隔离、SNI实际复制与证书/轮换/被墙入口；03双成员排序保存回读、桌面/390及真实401；04桌面/390及保存时真实401；08实际428返回首次设置。细节已追加各原票，不生成辅助报告。临时面板及独立Chrome均已停止。
- 完整make首轮退出2（执行者报告墙钟490.89s，clusterharness6项失败，其余包通过）；原执行者正在逐项诊断并由唯一实施者修复，完整重跑与新修复审查尚待结果，不能标全仓通过。未验的本机细项/生产边界保留原记录，不把虚拟节点补验当作真实网络演练。

### 完整测试失败诊断与夹具修正（2026-11-12）

- 首轮完整 `/usr/bin/time -p env GOFLAGS=-timeout=30m make test` 退出 **2**，clusterharness **481.185s**／墙钟 **490.89s**，六项失败；没有减少 Makefile 测试范围或修改生产健康、授权、资源预算实现。
- 三项旧夹具与当前已定语义不一致：`TestNodeDeletionRecognizesFutureExitGroupRelations` 原直接写库制造带出口引用的直连规则，改为真实 HTTP 建立合法停用隧道，保留出口组和停用规则两处删除保护断言；`TestSingleEntryJSONFormatsRoundTripAndAtomicRejection` 保留格式1/2导入，当前导出版本期望更新为3并回导比较；`TestTunnelNodeAndRuleBudgetsShareDirectCapacityAndKeepControlResponsive/combined` 原新建 A→A 已被路径去重正确拒绝，改合法 A→B→C，由 B 中转与 B 直连共享原 MaxConnections=3/PerRule=2，并核对 C 两条业务、控制响应、释放后恢复。原三项定向退出 **0**，clusterharness **4.887s**／墙钟 **6.32s**。
- 三项异步夹具按真实持久状态推进假钟：取消主备动作先等待 CheckerOnly 的 `unreachable` 写入，再推进一秒并等待 `submitted`，才检验取消及原期望恢复；入口就绪先确认出口不可用，再验证连续成功恢复期间未满60秒仍不就绪、满60秒健康后新的入口采样就绪；全部发布记录审计读取持久 `next_check_at`，到期唤醒后在该截止点前移时冻结假钟，等待新的 writeback 动作和原记录值/启用状态，避免原35次10秒跳跃取消真实HTTP的10秒context。
- 诊断期间给 ModifyRecord 加100ms真实延时复现原审计失败：退出 **1**，clusterharness **7.206s**／墙钟 **9.12s**。中间三项联合 `-count=5` 退出 **1**，clusterharness **23.393s**／墙钟 **24.09s**：AllPublish五次通过、取消场景一次失败、就绪场景五次失败；随后修正取消前的持久屏障、恢复期 `healthy` 会清 `recovery_since` 的读取条件，以及审计最后tick后才注册定时器的唤醒条件。以上失败均保留，不把中间结果冒称通过。
- 接续实施者重新运行 `/usr/bin/time -p go test ./internal/clusterharness -run 'TestObservationCancelledPrimaryRestoresCurrentExpectation|TestEntryReadinessRequiresAnAuthorizedSuccessfulExitForEveryTunnelRule|TestAllPublishSetWatchAndPerRecordStatusAudit' -count=5 -timeout=5m` 退出 **0**，三项各五次，clusterharness **20.050s**／墙钟 **21.22s**。100ms延时保留为确定性慢HTTP回归夹具，明确注释其用途；删除临时 `diagnosticSlowWrite` 命名及 `DIAGNOSTIC` 输出，没有遗留诊断分支或新增辅助文件。
- 最终 `/usr/bin/time -p go test -race ./internal/clusterharness -run 'TestObservationCancelledPrimaryRestoresCurrentExpectation|TestEntryReadinessRequiresAnAuthorizedSuccessfulExitForEveryTunnelRule|TestAllPublishSetWatchAndPerRecordStatusAudit' -count=1 -timeout=5m` 退出 **0**，clusterharness **16.706s**／墙钟 **20.25s**，无race报告。其后已串行启动完整 `/usr/bin/time -p env GOFLAGS=-timeout=30m make test`，待全部包与最终退出码落定。
- 根安排的独立最终审查指出一项Minor文档准确性：GLOSSARY隧道传输仍提及未实现的WS+预共享密钥，新增部署步骤也写WSS/WS。执行者核对当前API仅允许 `forwarding.WSS`，已统一改为当前WSS；没有扩展实现、协议或ADR决策。仅文档修正，不重复无关已通过测试，最终 `git diff --check` 一并核对。

### 最终完整验证通过与冻结交接（2026-11-12）

- 原命令 `/usr/bin/time -p env GOFLAGS=-timeout=30m make test` 最终退出 **0**，墙钟 **471.82s**（user **268.66s**、sys **116.58s**）。完整执行 npm ci、TypeScript/Vite **3163模块/607ms**、Linux amd64节点及gzip嵌入包、`go vet ./...`、`go test ./...`；没有跳过、缩减或替换慢场景。

| 测试包 | 最终结果与耗时 |
|---|---|
| cmd/nbpass | 通过，1.665s |
| cmd/nbpass-node | 通过，cached |
| internal/clusterharness | 通过，458.275s |
| internal/clusterharness/fakedns | 通过，cached |
| internal/compatibility | 通过，cached |
| internal/dnsprovider | 通过，cached |
| internal/dnswatch | 通过，cached |
| internal/forwarding | 通过，cached |
| internal/node | 通过，29.661s |
| internal/nodeupdate | 通过，2.201s |
| internal/panel | 通过，98.322s |
| internal/platform | 通过，1.371s |
| internal/rotation | 通过，cached |
| internal/checker、controlpb、eventlog、ipregion、nodebundle、probe、publicip、selfcheck、timing；web | 原命令均报告 no test files，无遗漏 |

- 完整make后的嵌入gzip、Linux节点与三个变化前端asset的SHA256逐项等于上文正式构建值；最终proto/业务实现/迁移registry无额外差异。最终 `git diff --check` 退出 **0**，九份变更Go测试 `gofmt -l` 无输出。已通过的 `make proto`、`make build`、56项显式前端及缓存/真实旧节点race沿用本票已记录证据，无变化不重复昂贵检查。
- 09测试/正式文档/发行产物现冻结交根终审和提交：新增整体迁移、真实旧程序同身份重装、安装脚本双程序替换三处测试；六项失败所涉及夹具修正；DESIGN/GLOSSARY/部署说明；web/dist及嵌入节点包。仅100ms慢HTTP为明确保留的回归夹具，临时诊断输出/命名已移除，无新增辅助文件、日志、报告、浏览器操作或提交。父会话其余票据/spec改动全部保留。
- 本机自动化完成不代表整体验收或生产发布完成。09保持claimed；生产专线/NAT/防火墙/证书回程、实际回滚目标身份及真实Linux/systemd完整发行/重装/回滚仍待验；页面已验及剩余细项以各原票最新Comments为准。下一步由根完成独立终审、按授权提交功能/产物及本轮唯一中文文档提交。

### 根会话六项全量失败修复的只读复核（2026-11-12）

- 独立review_ix09再次只读检查all_publish/config_validation/entry_observation/readiness/single_entry/tunnel_limits六处最新测试diff：Critical0/Important0/Minor0；未削弱结果断言。DNS仍要求新writeback动作、EntryWritten与实际地址/启用恢复；取消仍覆盖unreachable→submitted后的期望恢复；readiness增加严格60秒恢复期；格式2导入后按当前3往返；删除引用仍409且列两项。
- combined容量测试改为合法A→B→C，在B验证中继与本机直连共享总3/每规则2及释放/已有流/控制响应。原A→A同机特殊路径不再由此HTTP用例覆盖，新建重复路径已由spec禁止，不放宽生产行为。DNS诊断命名已移除，100ms真实HTTP延迟保留作回归。
- 本结论仅审查当前六文件改动，不替代冻结后的定向绿与完整重跑。用户已明确反馈已耗时10小时；根已说明剩余20–40分钟是无新增问题前提的估计，当前仅收口已发现失败、必要终审、完整验证和统一提交，不扩大功能或额外浏览器范围。原授权与生产待验边界不变。


### 最终独立整体审查、提交与交付（2026-11-12）

- 独立 `final_ix_review` 只读审查起点 `1f4b742` 至 `bddf428` 的04–08全部实现与冻结后09测试/文档/正式产物，结论 **Spec符合已定决策和本机交付范围 / Quality Approved；Critical 0 / Important 0 / Minor 0**。此前两处WS文案Minor已修为实际WSS并复核ADDRESSED。授权编译到逐grant持久确认、relay预算/重试到缓存恢复、精确证据到资源事故归属、三模式API到页面刷新及迁移/真实旧版回滚边界均已核对；没有遗留重要问题。
- 根复核最终九份Go测试 `gofmt -l` 无输出、工作区/暂存区 `git diff --check` 退出0，proto两份生成文件、正式gzip/三个前端asset摘要与本票记录一致；原1–48迁移逐字节保留，仅追加49/50/51。gzip解压等于Linux节点，当前面板嵌入HTML/六个引用asset/gzip真实字节；IX02两份固定binary未覆盖。未对已通过且源码无变化的昂贵测试重复运行。
- 实际中文提交：**`8d6105a`**「补齐 IX 整体验收与全仓测试夹具」（九份Go测试）；**`3e808da`**「构建 IX 正式前端与 Linux 嵌入节点包」（仅web/dist和nodebundle，bin仍忽略）。两次提交前暂存diffcheck均退出0；未夹带.md。全部本轮14份既有Markdown（10票、spec、DESIGN、GLOSSARY、deploy-linux）统一为最后一次中文文档提交；Git历史记录该文档提交SHA，不在自身内容中预填未知SHA。
- 本轮可执行工作完成，09及spec标记resolved，含义是本机实现/自动化/独立审查/产物和文档交付完成。07两个边界与已记录真实页面验收已完成；08已记录三模式、逐边、慢刷新及401/428等事实保留，运维关联/事故跳转等未记录细项仍待主编排者。02未执行完整换IP/证书轮换及PEM复制逐字节检查，03双地址真实健康分行/10秒老化等原票未记录人工项不虚称通过；04虚拟库页面验收不代替实际节点源应用/生产网络。
- 独立审查明确 **Declined to judge**：生产回滚目标对应的历史源码/平台二进制身份，真实Linux/systemd双服务发行/同身份重装/旧版拒2与人工隔离回滚，以及真实逐边专线/NAT/防火墙/证书部署/回程/源IP。原票复合生产验收框保持未勾，部署说明第7/8节已交付具体步骤；生产发布/推送/合并均未执行。下一步由主编排者补剩余人工验收，再取得独立部署授权；本会话不请求重复实施确认。
