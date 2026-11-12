# 08 端口链式模式、完整路径与逐跳同步展示

Status: resolved（自动化及已记录页面验收完成；运维关联/事故跳转等未记录人工细项待验）
Blocked by: 06c, 07（CRUD 与多跳集成两个边界均完成）
Migration: 无（消费 06a 的 51；不新增迁移）
Parallel: 在 06c/07 完成后串行联调；本票独占 Rules/RulesData/RulesTransfer 与端口详情，不与 07 同改 main.tsx。

## 目标

端口编辑支持直连 / 单跳转发 / 链式转发三选一，链式选已保存链；列表与详情展示从实际入口到各跳候选再到目标的完整路径，错误能定位具体服务器。

## 具体改动点

- `web/src/Rules.tsx` 当前在文件内直接实现编辑 Modal，并无独立 RuleEditor 文件。扩展该表单及 `RulesData.ts`：UI 增加 mode=`direct|single|chain`，提交按 06a 的 API 形状转换为 `kind=direct|tunnel`，single 只带 exit_group_id，chain 只带 chain_id，direct 两者均不带。切换模式使用 preserve=false/明确清理，防止旧引用留在启停、编辑和复制请求里。
- 加载 `/forward-chains` 供链选择，保留原 `/exit-groups` 的单跳选择；链编辑提供前往 `#/chains?focus=...` 的入口。未创建链时显示可操作引导，既有规则的引用临时缺失不能默默改为直连。
- 规则列表、筛选、展开详情与模式标签增加链式转发。基于 06a 的 rule.path / 05 hops 显示入口（单节点或组）、第 1…N 组、各组主备与所选地址/端口、最终目标；无实时证据时按定义展示，不能伪造「当前正在走」的唯一服务器。组入口保留逐 A1/A2 就绪，不能只显示合并结果。
- `PortSyncDetails.tsx`、`ConfigSync.tsx` 展示逐边/grant 应用确认、失败归属与 configuration_blockers，区分「整条阻塞（需升级服务器）」与「可运行但备用成员能力不足」，保留具体 node/address/capability/reason。备用升级需重新确认才列候选。本边健康、后续路径失败、控制离线/历史证据分别显示；普通入口行显示监听版本，relay 不伪造监听端口。关联 06c 的 operations/incidents 受影响端口与历史原因，现有页面需要适配时归本票。
- `RulesTransfer.tsx` 更新格式 3 与 chain_id 引用说明，仍提示导入不会自动创建被引用的链/组/服务器；保留格式 1/2 的服务端兼容。`NodeServerData.ts` 等使用端口定义的类型只按实际需要加 chain_id，保持服务器组/节点入口、批量启停、随机端口、流量和列表排序原行为。

## 迁移与兼容要求

- 无数据库或 protobuf 迁移，不改已有 kind 的 wire/API 值；三种界面模式与两个后端 kind 的映射必须经过数据函数测试。
- 旧规则未带 chain_id 时仍回填原直连/单跳模式；编辑和批量操作不能清空目标、入口、地址选择或累计量。共享链编辑影响多端口的说明与 ADR 0009 一致，不复制链到每条端口私有配置。

## 测试要求

- 前端单元 `RulesData.test.mjs`：三模式往返、从链切直连/单跳的字段清理、链式筛选、启停序列化、单节点/组入口、随机端口；新增路径展示/逐跳错误数据测试，覆盖主备多成员、域名/映射端口、缺失引用、无证据及历史健康。运行 `npm --prefix web test -- src/RulesData.test.mjs src/ChainsData.test.mjs` 与其他新增测试、`npm --prefix web run build`。
- clusterharness：复用 06c 的真实场景补 HTTP 契约：三模式互改、完整 path、具体 blocker/逐边与 grant 原因、仅备用不足及升级重新确认、共享链编辑影响所有引用、格式 3 往返及 1/2 兼容，流量不重置。主备组/全部发布组→同链矩阵（仅 A1 专线坏、后跳全坏、恢复、控制离线）核对逐成员就绪、DNS 动作及换 IP 归属，仍经 ADR 0005 协调者。运行对应定向测试，字节断言沿用真实节点。
- 编排者最终人工核对三种模式、列表/详情完整路径、联动编辑链、升级提示、窄屏及会话过期。本次任务不打开浏览器。

## 验收标准

- [x] 三模式提交互斥且编辑回填正确，不残留隐藏字段。
- [x] 完整路径、地址、主备和最终目标能看懂，引用共享链的影响明确。
- [x] 逐边/grant/blocker 定位到服务器与地址，整条阻塞和备用不足有区别，无证据不误报实际路径/健康；组入口矩阵与事故关联已联调。
- [x] 旧单跳/直连、批量操作、导入导出和前端构建均通过回归。

## 不做的事

不再改后端模型或增加新链策略，不自动创建缺失引用，不实现 UDP/IPv6/智能跳数；术语文档、产物和全量交付由 09 负责。

## Comments

### 08 实施与公开合约接线（2026-11-12，冻结待独立审查）

- 唯一实施者，基线 `feat/ix` / `77e95be`；完整消费本票、spec、ADR 0006/0009、DESIGN/GLOSSARY/CLAUDE 与06c/07最终Comments，07两自动化边界已完成。原9份修改中的md原样保护，本轮证据只追加本Comments；不修改DESIGN/GLOSSARY/部署文档、不新增辅助方案/审查/日志文件。未创建worktree、切分支、push/deploy/merge、子agent或浏览器操作；未暂存或提交。
- Rules 表单使用 `mode=direct|single|chain`，按现有 wire `kind=direct|tunnel` 严格转换：single仅带exit_group_id、chain仅带chain_id、direct均不带。切换主动清理且条件字段preserve=false；编辑、复制定义与启停共用definition-only回填/序列化，不夹带流量/同步运行字段。保留单节点/组入口、目标/端口、自动分配端口、批量原API、流量、排序及拖动。加载真实 `/forward-chains`，空链有创建入口；保留暂缺的旧chain_id和链模式，由服务端校验，不自动直连；链编辑跳 `#/chains?focus=...`，说明共享影响。轮询请求版本保护防止旧响应覆盖新数据。
- 列表模式筛选/标签与完整路径、展开详情消费真实rule.path，保留所有入口与有序hop的全部候选、node/address_id、域名和有效映射端口、最终目标；无实时证据只展示定义，不声称当前唯一路径。链式按主备标注，旧单跳可能为最小连接数，因此只标候选顺序，不改变原策略。缺少path时可用已保存链的定义兜底，不借管理页其他规则健康；引用仍缺失时明确待确认。
- PortSyncDetails / ConfigSync 分开显示入口业务监听版本、逐A1/A2入口检查与原因、每跳服务器整体版本/控制连接/应用错误、逐边grant应用报告与确认。本边和后续状态沿06c，保留from/to、hop、address、endpoint digest、chain fingerprint、rule/grant generation、desired/applied revision、采样时间及具体错误；relay没有虚构监听端口。以服务端state证明确认，版本相等不能替代grant确认；备用升级须重新持久应用并确认。整条阻塞与备用不足沿既有状态/完整blocker展示；每秒本地老化，10秒过期/控制离线/历史不保留健康色，ready明示未探测最终目标。
- 真实API边界：末跳源绑定错误的具体IP原因在 `/nodes.apply_error`；该场景path.edges.apply_error实际为“授权未持久应用”。页面同时保留服务器整体错误与本端口grant报告，明确二者范围，不补造grant详细原因/时间/确认字段。普通节点整体版本不当成该端口授权确认。
- Overview 按path_nodes反查任一中间/末跳关联的所有端口，members保持原入口语义，展开可查看逐边/blocker；逐流实际失败类别/边/地址/代次/摘要单列。IncidentDetails支持真实chain refs跳转；结构化历史原因转换为可读失败归属并保留完整原始JSON证据，未知JSON/资源名/原始错误不改，历史不当当前健康。RulesTransfer说明当前格式3/chain_id共享引用、兼容1/2及导入不会自动创建任何引用资源；NodeServerData监听计数无需增加链字段，原行为通过回归。

### 红绿与夹具核对

- 数据先红：`/usr/bin/time -p node --experimental-strip-types --test web/src/RulesData.test.mjs web/src/RulePathData.test.mjs` 退出 **1**，8通过/4失败，Node **75.320ms**／墙钟 **0.10s**。捕获缺少三模式回填、模式切换残留tunnel引用、链筛选混入其他模式；新路径数据模块尚不存在。实现后15/16，唯一失败为“待确认”文本正则顺序与输出不符，改清晰文案后同命令 **0**、**16/16**、Node **73.785ms**／墙钟 **0.10s**。
- 历史原因测试首次有未使用命名空间导入的测试ReferenceError，修正后取得有效红：`node --experimental-strip-types --test web/src/IncidentDisplay.test.mjs` 退出 **1**，历史JSON没有可读实际失败边；墙钟 **0.09s**。实现后 **0**、**4/4**，Node **62.951ms**／墙钟 **0.09s**。自查追加旧单跳不臆造固定主备红测：路径测试 **1**、5通过/1失败、Node **62.870ms**／墙钟 **0.09s**；改为单跳候选顺序后包含于最终全前端绿测。
- 新 `TestPortChainHTTPModesPathTransferAndTraffic` 复用真实chainRig与真实HTTP、WSS/yamux/TCP，六个有向模式切换均真实传字节，累计从4/10递增到28/70，不按跳放大；完整两入口、两跳各两候选独立对实际下发snapshot/endpoint/grant核对全部身份、域名/端口、代次/指纹及确认。format3定义-only导出、预检和覆盖往返保留chain_id与累计量；缺链预检/提交原子拒绝且不创建资源；format1/2真实直连/单跳导入传字节，旧格式拒绝chain_id。
- 追加现有真实场景断言：EveryOldPosition的具体node/address/forward_chains/reason blocker；BackupUpgrade的备用不足、实际发送gate受阻时Rules GET不能复用旧grant确认、放行后全候选确认及真实接管；SharedEdit同时反映所有引用端口path；Asymmetric的A→B本边好、B→C坏、独立备用好同时保留；ForwardChainHTTP离线真实缓存仍传字节但Rules GET仅historical/fresh=false。新 `TestPortChainHTTPGrantFailureReason` 使用实际末跳非本机源IP应用失败，同时断言节点具体IP错误与grant“授权未持久应用”，恢复后真实传字节。未修改任何后端生产实现或模拟转发结果。
- HTTP首跑 `/usr/bin/time -p go test ./internal/clusterharness -run TestPortChainHTTP -count=1 -timeout=5m` 退出 **1**，包 **5.429s**／墙钟 **6.90s**：只等入口就绪便断言所有备用边新鲜，读到尚未采样历史；改为等全部真实当前边证据。随后新/既有授权定向首轮退出 **1**，包 **17.258s**／墙钟 **18.67s**：磁盘checkpoint失败没有逐grant原始磁盘错误、以及旧格式双入口同宿主监听同端口冲突，均为新增夹具预期错误；移除错误的grant文案假设，旧格式使用单入口组，并单独以实际源绑定失败验证grant报告。下一轮包 **14.577s**／墙钟 **16.00s** 退出 **1**，仅因仍假定grant含IP，而真实grant为上述持久应用原因；按实际合约分别断言/node与grant。期间一次误把无返回的awaitNode赋值造成编译失败（退出1，墙钟0.36s），改为等待后读取nodeRow。这些不计生产功能红，没有放宽确认/流量/业务断言或增加后端字段。
- 夹具修正后 `/usr/bin/time -p go test ./internal/clusterharness -run 'TestPortChainHTTP|TestChainAuthorization(EveryOldPosition|BackupUpgrade|TerminalDisk|SharedEdit)' -count=1 -timeout=5m` 退出 **0**，包 **13.914s**／墙钟 **15.33s**。

### 最终验证与冻结范围

| 命令 | 退出码与实际结果 |
|---|---|
| `/usr/bin/time -p node --experimental-strip-types --test web/route.test.mjs web/server-groups.test.mjs web/src/*.test.mjs` | **0**，**51/51通过**，Node **91.107ms**／墙钟 **0.12s**。显式包含所有数据测试，未依赖npm默认仅route的脚本行为；覆盖最终源码改动。 |
| `build_dir=$(mktemp -d /tmp/nbpass-ix08-build.XXXXXX)`；`/usr/bin/time -p npm --prefix web run build -- --outDir "$build_dir"` | **0**，最终完整TypeScript/Vite通过，3163模块、Vite **191ms**／墙钟 **2.43s**；最终目录 `/tmp/nbpass-ix08-build.jgGB0n`。前两次完整构建亦退出0（2.56s/2.46s），均仅系统临时目录。 |
| `/usr/bin/time -p go test ./internal/clusterharness -run 'TestPortChainHTTP|TestForwardChainHTTPRealMultiHopAndOffline|TestChain(EntryGroup|Authorization(EveryOldPosition|BackupUpgrade|TerminalDisk|SharedEdit|HTTPRuleTraffic)|Failover(Asymmetric|EachHop|Address))|TestRulesJSON' -count=1 -timeout=10m` | **0**，包 **45.528s**／墙钟 **46.93s**，覆盖最终5份Go测试改动。含未修改的 `TestChainEntryGroupPrivateFailurePublicationMatrix` 与 `TestChainEntryGroupOfflineAutomaticCandidateAndMinimumKept`：主备/全部发布 × 仅A1专线坏、后跳全坏、恢复、控制离线，逐成员就绪、真实DNS期望/实际动作、最少保留、换IP归属与ADR0005协调去重。沿用真实政策，单纯专线未就绪不冒称自动切换触发。 |
| `git diff --check`；本轮5份Go的 `gofmt -l`；`git status --short -- web/dist internal/nodebundle bin`；`git diff --cached --stat` | **0**；后三者无输出，格式/产物/暂存范围核对通过。 |

- Node NO_COLOR/FORCE_COLOR、Vite系统临时outDir及既有antd大chunk提示均非失败。没有跑全仓make、修改wire/proto/迁移/模型/业务政策，未重建dist/nodebundle/bin；全量发行09统一。最终Go通过后只改前端候选标注/类型/请求版本保护并已完整重跑前端和build，未再改Go，不无谓重复HTTP。
- 本轮源码/测试范围共 **19份**：前端14份 `Rules.tsx`、`RulesData.ts`、`RulesData.test.mjs`、`Rules.css`、`RulesTransfer.tsx`、`PortSyncDetails.tsx`、`ConfigSync.tsx`、新 `RulePathData.ts` / `RulePathData.test.mjs`、`Operations.ts`、`Overview.tsx`、`IncidentDetails.tsx`、`IncidentDisplay.ts` / `IncidentDisplay.test.mjs`；Go5份 `chain_authorization_test.go`、`chain_compatibility_test.go`、`chain_failover_test.go`、`forward_chains_test.go`、新 `port_chain_contract_test.go`。不含新增后端生产源码；加本票Comments为唯一新文档改动。
- **已冻结，交根编排者独立Spec/Quality只读审查，未提交。** 保留原Status及验收复选框待根收口；未宣称浏览器三模式交互、完整路径/升级提示、窄屏或会话过期人工验收已完成，按任务由父会话后验且不阻塞自动化边界。

### 独立审查修复轮 1：慢请求轮询饥饿与会话错误丢失（2026-11-12，冻结待 scoped 复审）

- 独立审查首轮为 **Spec Issues / Quality Needs fixes，Critical 0 / Important 1 / Minor 0**。唯一I1已核实：每次load递增version，5秒轮询不检查请求在途；若任一GET持续6秒，则下一轮持续使上一轮响应过时，页面永不初显/刷新，慢401/428也被catch的版本门禁吞掉。本轮仅修I1及其调度生命周期影响，其他已通过条款不扩改。
- 先将原page的请求版本逻辑等价提取为RulesData中的小调度边界，用可控deferred读取驱动实际异步状态，模拟跨多次5秒tick的在途HTTP；断言数据应用、错误交付、并行数和显式操作等待结果，不用源码grep、React大型测试框架或真实等待。`/usr/bin/time -p node --experimental-strip-types --test web/src/RulesData.test.mjs` 有效红退出 **1**，11通过/4失败，Node **74.656ms**／墙钟 **0.10s**：轮询生成4个并行读取、慢401未交付、4次显式重载生成5并行读取、卸载前已发额外请求。
- 修复语义明确分为两类：`poll()`在途直接跳过，不推进版本或排队，因而慢读取仍正常初显；`refresh()`供保存、启停、删除、批量、拖动失败重载、导入与手动刷新，若在途则合并一个必要尾随读取，共享的Promise等待最新读取完成。若尾随读取期间又有变更，则再读一次；有pending时旧成功数据不应用，但错误仍交付既有failed，慢401/428不会被后续读取吞掉。加载状态覆盖整个读取序列，避免每次尾随切换闪烁。
- useEffect每次挂载创建独立调度器，cleanup立即dispose并清ref/interval；dispose清pending、不再启动尾随读取，所有迟到成功/错误均无状态副作用。新增挂载后立即卸载/重挂边界：首测因先await未完成读取而被Node取消（测试等待接线错误，不计功能红）；改为先核对读取次数后，取得有效红退出 **1**、15通过/1失败，Node **74.443ms**／墙钟 **0.10s**（已卸载仍启动1次读取）。调度器微任务实际开始前补dispose检查，新挂载使用独立实例，不复用已销毁状态。
- 首4项修复后的定向数据测试退出 **0**、**15/15**，Node **74.727ms**／墙钟 **0.10s**；追加即时卸载边界修复后，以下最终全前端覆盖全部5项新回归：慢读取跨多个poll正常初显/随后继续刷新；慢401与428在pending刷新时仍交付且失败后可继续；保存/批量/拖动/手动刷新合并并等待最新已提交读取，旧数据不能覆盖；卸载后迟到成功/错误无副作用且不启动pending；即时卸载不启动网络、新挂载独立初显。

| 最终命令 | 退出码与实际结果 |
|---|---|
| `/usr/bin/time -p node --experimental-strip-types --test web/route.test.mjs web/server-groups.test.mjs web/src/*.test.mjs` | **0**，**56/56通过**，Node **94.564ms**／墙钟 **0.12s**；覆盖本轮最终源码。 |
| `build_dir=$(mktemp -d /tmp/nbpass-ix08-r1-build.XXXXXX)`；`/usr/bin/time -p npm --prefix web run build -- --outDir "$build_dir"` | **0**，完整TypeScript/Vite通过，3163模块、Vite **213ms**／墙钟 **2.50s**，目录 `/tmp/nbpass-ix08-r1-build.1nxNIg`。 |
| `git diff --check`；`git status --short -- web/dist internal/nodebundle bin`；`git diff --cached --stat` | **0**；后两者无输出，产物和暂存区未改。 |

- 本轮只改 **3份既有前端文件** `Rules.tsx`、`RulesData.ts`、`RulesData.test.mjs` 及本票Comments；未新增文件、改Go或重复HTTP，沿用上一轮45.528s最终真实HTTP证据。未改08其他已通过功能、其他md、wire/proto/迁移/政策/发行产物。Node颜色环境与Vite临时outDir/既有大chunk提示仍非失败。
- **修复轮1已完成并冻结，未提交，交原reviewer仅针对I1及本轮新增影响做 scoped 复审。** 不创建worktree/分支/子agent/浏览器/辅助文档日志，原Status及最终验收项交根编排者收口。

### 最终独立复审与功能收口

- 原独立 reviewer 对修复轮1仅审I1及新增影响，正式结论：**I1 ADDRESSED；Spec compliant；Quality Approved；C0/I0/M0**。确认在途poll跳过、显式重载尾随等待、旧成功不覆盖新数据、401/428交付与卸载/微任务生命周期均闭环；deferred Promise回归覆盖关键行为，未发现新问题。其余19文件首次审查已通过。
- 根核对最终原票测试证据、产物/暂存范围及diffcheck（退出0），自动化验收满足，解除09依赖。真实HTTP沿最终45.528s，前端最终56/56及build2.50s；源码冻结后不重复无变更的HTTP。浏览器三模式/路径/同步错误/运维关联/窄屏/会话人工验收由主编排者后验，未虚称完成。

- 实际中文功能提交：**`5d2824e`**「支持端口三种转发模式与完整逐跳同步展示」（14文件）；**`bddf428`**「关联逐跳受影响端口与链路事故历史原因」（5文件）。两次暂存diffcheck和commit退出0，仅08源码/测试，所有.md及正式dist/nodebundle/bin未纳入。提交后仅10份既有md未提交，进入09。

### 主会话端口页真实浏览器验收（2026-11-12）

- 使用原07普通临时源码副本的真实后端（77e95be；08无后端生产变更），将已提交08前端 `bddf428`（含5d2824e）归档覆盖到该副本并重新构建；临时副本继续使用07注明的真实多跳测试暂停点与浏览器时钟对齐，正式仓库源码/产物未改。实际5节点、两跳各两候选、WSS/yamux/TCP及真实专线域名/映射端口，不伪造业务或API响应。
- 页面原链式规则按UI切为直连并保存：重开直连选中，GET kind=direct且无chain_id/exit_group_id；再选单跳和真实layer0保存，重开单跳选中，GET kind=tunnel仅exit_group_id；再选已保存共享链保存，重开链式选中，GET仅chain_id。三次均同rule ID、目标chain-target.test、原监听端口，累计上传4/下载10字节保持；人工为这一轮三向切换，六向互改及实际各模式传输仍由前述正式自动化证明。
- 链选择显示真实引用数，离开链时变0，恢复链时变1；共享影响说明可见，管理/编辑按钮实际跳到 `#/chains?focus=<真实chain ID>` 并显示所定位链。手机折叠导航没有常驻ports链接，脚本一次找不到该链接后按已知ports路由返回，未当作产品缺陷。
- 1440桌面列表显示从入口组/实际入口到两跳全部主备候选、所选address ID、域名/映射端口和最终目标；展开详情的路径区域可读。实际入口行有监听端口，各relay/terminal只显示其整体版本/控制/错误，6条边另列本端口grant、规则/grant代次、期望/实际版本、摘要、指纹。配置应用进行中曾真实显示待确认，随后显示持久应用并确认；没有把整体版本相等直接当授权确认。
- 在原真实B1后跳失败/B2可用阶段，端口展开详情同时有“本边可达·后续路径未就绪”“本边未就绪”及独立备用就绪，grant确认与路径就绪分开。控制全部断开后，实际Rules GET六边均historical/fresh=false；页面六条“历史·状态未知”以及“已有应用报告，待确认当前授权”，各跳显示控制失联/缓存业务待确认，不假造恢复。
- 升级提示使用**未接入的纯虚拟候选**，没有把它冒称真实旧二进制或删除运行节点能力。API追加到首组备用后，页面实际显示“可运行但备用成员能力不足”，列出该node、public/实际后继address、forward_chains/tunnel_authorization/exit_failover/IX能力与需升级原因。临时首组仅保留该候选时实际显示“整条阻塞（需升级服务器）”；随后立即恢复原两真实成员，原测试继续成功。真实旧程序拒缓存/升级矩阵仍以前述专门自动化为证据。
- 390×844编辑窗截图上下段已检查：三种模式、长说明、共享链选择、开关及保存/取消均可滚动到达，无按钮重叠，页面scrollWidth=390。1440展开详情截图已检查；列表完整长路径会换成较高行，展开区域提供更宽的阅读空间，未宣称所有长ID可在单行同时展示。
- 慢响应实测：临时浏览器网络延迟6500毫秒，通过编辑窗改名保存，列表15620毫秒后实际显示新名称，证明慢请求和保存后尾随刷新能交付，之后恢复原名。控制离线阶段再加6500毫秒延迟，撤销浏览器自身会话后轮询收到慢401并回到登录页（logout204返回后7035毫秒）；不是直接导航登录页。延迟均已移除。
- 此次带人工暂停的原真实单例自然退出 **0**：测试811.57秒、包812.967秒（包含浏览器等待与三模式/能力不足夹具操作），面板/节点已由测试清理，独立临时Chrome已停止。正式45.528秒HTTP集合、56项前端和构建证据保持有效，本次不替代全仓make验收。
- 本节完成端口三模式、回填/隐藏字段、完整路径与逐边同步、共享链跳转、备用不足/整条阻塞、手机布局与慢401浏览器边界；真实生产专线/NAT、生产跨格式重装/回滚仍不在此次本机操作内。截图/临时脚本仅在系统临时目录，未生成仓库报告或日志。

### 主编排者最终会话边界补验（2026-11-12）

- 本次正式构建的独立临时库，端口页面已加载后在该虚拟库清空 administrator/sessions，实际 /api/rules 返回428；页面真实进入「首次设置」，未伪造HTTP成功/错误响应，未改健康或会话实现。该临时库不是生产库，验收后临时面板和Chrome均停止。
- 其他未记录的运维关联/事故跳转及错误展示仍不标为浏览器已验；原真实5节点三模式、逐边、慢刷新及401补验不重复。
