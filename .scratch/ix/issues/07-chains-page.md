# 07 转发链管理页与逐跳路径预览

Status: resolved（两个自动化边界及已记录页面验收完成；真实生产专线/NAT 待验）
Blocked by: 05（CRUD 阶段）；06c（与多跳集成完成）
Migration: 无（只消费 05 的 50；不新增迁移）
Parallel: 05 后 CRUD 可与 06a/06b/06c 后端并行；本票独占 Chains 前端和 main.tsx 的 chains 路由替换。06c 后补多跳集成验收，08 等待两个边界都完成。

## 目标

把 `#/chains` 的规划中页面替换为可操作的链管理页，按有序端点组表达每跳路径，支持新增/编辑/重排/删除和引用、健康信息展示。

## 两个完成边界

- **CRUD 阶段完成（依赖 05）**：真实存储的新建/编辑/重排/删除与待观测状态可用，前端测试/构建通过；05 独立基线尚无规则引用，rule_count=0、observations 为空有真实语义。并行的 06a 接入引用后必须显示真实 rule_count；06c 前无观测显示待观测，不宣称多跳健康已集成。
- **与多跳集成完成（依赖 06c）**：真实引用数、逐边地址/配置摘要、后续路径健康和历史证据能正确展示，共享链编辑与引用删除保护已联调。只有此边界及 CRUD 均通过才整票完成；不能在 CRUD 后解除 08 的依赖。

## 具体改动点

- 新建 `web/src/Chains.tsx`、`ChainsData.ts`、`Chains.css`，按 05 的 `/api/forward-chains` API 读写，复用现有 `/exit-groups`、`/nodes`、`/tunnel-exits` 的公开信息；优先用服务端 hops 预览，不能根据服务器在线状态自己推算链健康。
- `main.tsx` 引入 Chains 并替换 `page === 'chains'` 的 PlannedPage 分支；`route.ts` 已有 chains/pageTitles，不新建第二个 hash，也不改变旧 rules/nodes/exit-groups 别名和 focus 语义。支持 `#/chains?focus=<id>` 定位。
- 列表显示名称、有序组路径、引用端口数、各跳候选与所选地址、各跳真实观测；新建/编辑为名称 + 按顺序添加端点组，可上下移动、移除，至少两组；展示组内主备顺序。409 原因保留在当前表单，不能清空输入或把重复节点错误吞成「保存失败」。
- 引用数为真实 rule_count，删除 409 显示引用原因；编辑说明影响所有引用端口，保存后重新读取。健康 observations 为空显示待观测，fresh=false 显示历史；复用 `EndpointData.healthStatus/healthIsFresh` 的 10 秒到期规则，展示发起者、目标地址及所属配置证据。区分本边可达与后续路径失败，不能把服务器其他地址成功/旧摘要成功或控制在线当整链健康。
- 使用 DESIGN.md 的 PageHeader、FilterCard、TableCard、FormSection、StatusDot、ColorTag、深色 token；搜索/分页、长地址/域名、窄屏、空列表、加载错误、401/428 处理与既有页面一致。页面专属 CSS 不重建主题。

## 迁移与兼容要求

- 无迁移、不改后端协议。05 即可完成 CRUD 与无观测状态；06a 填真实引用、06c 填逐边观测后，按既定字段完成多跳集成。并行时只按合约开发，不向后端塞演示字段。
- 不提前替换端口模式表单；此页链可以尚未被端口引用。旧书签、其他规划中页面和现有端点组管理不受影响。

## 测试要求

- 前端单元新建 `ChainsData.test.mjs`：顺序增删/上下移边界、至少两组、重复选择、防丢 ID、请求序列化、路径标签、未知/历史健康和 10 秒到期；补 `web/route.test.mjs` 的 chains/focus。运行 `npm --prefix web test -- src/ChainsData.test.mjs`、`npm --prefix web run build`。
- clusterharness：CRUD 阶段消费 05 的 `Test.*ForwardChainsAPI` 证明真实存储/拒绝；06c 后运行真实链/主备/非对称可达场景，核对 GET 的引用数、from/to、address_id、endpoint_config_digest、chain_fingerprint 和 rule/grant 代次，验证本边健康与下游失败可同时显示。必要时在相关测试补契约断言，不复制假 Go 数据面测试。
- 页面人工验收交由编排者在最终收尾核对：新增/编辑/移动/删除、被引用删除、长名称/地址、窄屏、会话失效、实时转历史；本次拆票任务不打开浏览器。

## 验收标准

- [x] chains 路由有真实 CRUD，路径顺序和服务器地址清楚可见。
- [x] 至少两组、错误反馈、编辑影响范围、引用删除保护在页面可理解。
- [x] 无证据显示待观测，有证据区分各边及历史，不生成假健康。
- [x] CRUD 阶段完成：05 的真实 API、前端测试与构建通过，空观测有明确含义。
- [x] 与多跳集成完成：06c 的真实引用/逐边/历史观测联调通过；达到此边界才解除 08 依赖。

## 不做的事

不实现后端/多跳数据面或端口链式模式（08），不添加智能跳数、拖拽拓扑画布、默认传输开关；不改 DESIGN/GLOSSARY，不提交 web/dist。

## Comments

### 主会话并行接管前端（2026-11-12）

- 主会话1099为缩短串行等待，按本票原有05后可并行的边界接管07前端实施，唯一实施者由主会话单独指派。子会话1101继续独占06c及后续08/09；请勿再派07实施者或修改本票所属Chains前端/main.tsx/route测试，本票完整结果由主会话更新原Comments供后续消费。
- 07仅修改Chains相关前端、chains路由测试及本票Comments；不修改06c后端、其他文档、构建产物。不建worktree、不切分支、不推送/部署。先完成真实CRUD和契约前端，06c完成后核对真实引用/逐边/历史观测联调；两个边界均满足才标resolved，08继续等待。

### 前端实施与 CRUD 自动化边界（2026-11-12）

- 已核对主会话独占归属，开始时 `web` 无其他修改；基线 `9963847bbb3ad66dfd55c0c693541883ccb25eff`、分支 `feat/ix`。实现仅限 `Chains.tsx`、`ChainsData.ts`、`Chains.css`、数据测试、`main.tsx` 的 chains 分支及 `route.test.mjs`；06c 的 Go 文件、其他票文档和构建产物完整保留。
- 页面真实调用 `/forward-chains` 的 GET/POST/PUT/DELETE，读 `/exit-groups`、`/nodes`、`/tunnel-exits` 公共选项。编辑保留链 ID 和有序组 ID，至少两组、重复组、200 字节名称校验；添加/上下移/移除稳定按组 ID。路径优先显示服务器 hops 的全部主备候选、所选地址 ID、地址及有效拨号端口。真实 rule_count 展示，共享编辑说明影响全部引用端口；保存失败原表单和服务端具体原因留在当前弹窗，删除 409 也留在删除弹窗，停用引用仍受保护。成功后重新 GET。
- 逐条显示 from/to、地址 ID、endpoint_config_digest、chain_fingerprint、rule_id/generation 和采样时间，不按服务器控制在线推算、不跨规则/来源/摘要合并。空观测为“待观测”；消费当前06c公开状态 `unknown/historical/edge_unhealthy/suffix_unknown/ready/grant_failed`，区分本边未就绪、后续未就绪与授权应用失败。复用 EndpointData 的 healthStatus/healthIsFresh，服务端 fresh=false 或采样满10秒均转历史；每秒本地老化，GET超时/挂起也不会保持健康色。候选身份不匹配的观测只显示历史，ready 明示未探测最终目标。
- 已按 DESIGN 复用 PageHeader/FilterCard/TableCard/FormSection/StatusDot/ColorTag 和深色 token；实现搜索、10条分页、chains/focus定位、长地址换行、窄屏布局、空列表/读取错误以及401/428回到既有会话流程。未修改08端口三模式、其他页面或主题，未打开浏览器。

### 数据红绿与验证证据（等待独立只读审查）

| 命令 / 阶段 | 退出码与实际结果 |
|---|---|
| `npm --prefix web test -- src/ChainsData.test.mjs`（红） | **1**：ChainsData模块尚不存在；5条路由通过，数据模块缺失明确失败。 |
| 同命令（首次实现） | **1**：13通过、1失败；错误文本正则未允许“至少需要两个”中的“需要”，校正断言为语义匹配。 |
| 同命令（基础绿） | **0**：14/14通过，包括顺序/边界/稳定ID、重复/最少两组、严格请求载荷与编码、路径搜索、未知/历史/10秒到期。 |
| 同命令（防借用其他地址的补充红） | **1**：13通过、1失败；不匹配当前候选的 ready 被错误保留绿色。修正为历史且在页面传入当前hop。 |
| 同命令（该修复后绿） | **0**：14/14通过，其他地址成功不会获得当前候选健康色。 |
| `npm --prefix web test -- src/ChainsData.test.mjs src/EndpointData.test.mjs src/EndpointMembers.test.mjs src/RulesData.test.mjs src/NodeServerData.test.mjs src/IncidentDisplay.test.mjs server-groups.test.mjs` | **0**：**41/41通过**，含隐式必跑的route.test.mjs；最后运行测试耗时83.184毫秒。 |
| `npm --prefix web run build -- --outDir /tmp/nbpass-ix07-build.hyX2NX` | **0**：完整`tsc -b`及Vite通过，3162模块，Vite260毫秒、墙钟2.47秒。首轮曾因React19 useRef缺初值退出1，已修复并两次完整构建通过。产物仅进系统临时目录，不覆盖web/dist。 |
| `go test ./internal/clusterharness -run 'Test.*ForwardChainsAPI' -count=1 -timeout=10m` | **0**：真实HTTP CRUD/引用拒绝/持久化等既有API契约集通过，包耗时**3.279秒**；没有改任何Go代码或跑全仓Go/make test。 |
| `git diff --check`；`git status --short -- web/dist internal/nodebundle bin` | **0**；产物范围无输出，暂存区为空。 |

- Node提示环境同时设置NO_COLOR/FORCE_COLOR；Vite提示临时输出目录在项目外、既有antd chunk大于500kB，均非失败。不安装依赖，不提交产物；尚未提交源码，接下来交独立只读审查者检查本票前端Spec和Quality。

### 等待06c最终提交后的集成核对

- 当前 `/forward-chains` 的 observations 合约只有单个 `generation`（规则代次）及上述公开字段；没有独立 grant_generation、local_ready/suffix_ready、逐流 failure_class 或失败边字段，页面不虚构这些字段。独立的rule.path.edges/operations丰富证据不混成管理页观测。需主会话在06c稳定后确认状态值、sampled_at和fresh含义、from/to/address/digest/fingerprint/rule/generation绑定及历史边界均仍符合最终公开合约。
- 最终以真实多跳/非对称可达、同服务器其他地址、同address_id改摘要、共享链编辑、真实rule_count和引用删除409核对页面；尤其A→B本边可达而B→C未就绪不能误报整链健康，控制离线/证据过期不能算恢复。CRUD自动化通过不解除08依赖；保持Status claimed，不勾最终06c集成或浏览器验收。人工浏览器检查由主会话安排。

### 独立前端审查与修正

- 独立只读审查者 `review_07_frontend` 首轮结论：**Spec符合07前端CRUD边界 / Quality通过，Critical 0 / Important 0 / Minor 1**；独立复跑定向数据/路由测试14/14通过。已逐项检查CRUD载荷/回读、顺序与稳定ID、409保留、引用与共享影响、路径候选/有效端口、完整观测身份/未知/历史/到期/状态区别、搜索分页/focus/token/窄屏及401/428；明确未验06c最终集成和浏览器，未改任何文件。
- Minor为首次辅助资源GET失败后，后续仅链轮询成功会清除错误、留下空的端点组选项。已沿 `Promise.all` 失败→未写入辅助数据→`load(true)`跳过辅助读取→清错误的真实控制流核实根因；增加成功加载标记，完整读取开始清标记，仅当前完整读取成功才置位，未成功前轮询自动重试全部公开资源，避免错误被仅链成功覆盖。本修正不新增接口或为简单布尔分支编写镜像测试；重跑既有真实数据/路由回归、TypeScript/Vite，并交原审查者复核。
- 修正后上述完整前端命令退出 **0**，**41/41通过**（81.771毫秒）；`npm --prefix web run build -- --outDir /tmp/nbpass-ix07-build.WxEENi` 退出 **0**，完整TypeScript与Vite通过，3162模块、Vite257毫秒、墙钟2.50秒；`git diff --check`退出 **0**，web/dist、internal/nodebundle、bin范围无修改。无后端源码变更，不重复与该前端修正无关的Go测试。
- 原独立审查者对修正只读复核通过：**Spec符合07前端CRUD边界 / Quality通过；Critical 0 / Important 0 / Minor 0**。确认完整资源失败会持续全量重试，只有当前请求成功才置位，旧请求受版本检查保护，未发现新增并发/轮询问题；审查者`git diff --check`通过。未覆盖最终06c联调或浏览器。
- 据自动化与独立审查仅勾选“CRUD阶段完成”；其他页面最终验收及06c多跳集成保持未勾选，Status继续 **claimed**。按主会话授权仅提交本票6个前端源码/测试文件，所有.md留待统一中文文档提交。

### 前端功能提交与主会话交接

- 已按明确授权完成中文功能提交：**`3bcd286d373d2330d388cf6adb419b388355a978`**「实现转发链管理页与逐边观测展示」，`git commit`退出 **0**。提交前两次逐项核对暂存清单，仅含 `web/src/Chains.tsx`、`ChainsData.ts`、`Chains.css`、`ChainsData.test.mjs`、`web/src/main.tsx`、`web/route.test.mjs`，共6文件、465新增/1删除；暂存`git diff --check`退出 **0**。
- 本票Comments与所有.md未暂存或提交；06c及其他人的Go改动未restore/add/commit。未创建worktree、切分支、push/deploy/merge或打开浏览器，web/dist、internal/nodebundle、bin未纳入提交。
- 前端CRUD自动化和只读独立审查边界完成；Status仍为 **claimed**。下一步由主会话等待06c最终提交后，按上列API/真实多跳/共享引用/历史证据点联调，并执行浏览器操作、窄屏/长地址、focus、401/428和实时转历史验收；完成该边界前不整票resolved、不解除08依赖。

- Frontend owner: Codeg task `7846e576-ed7d-4797-a7b8-1d0969470b3f`, session **1103**, assigned by root session 1099. Session 1101 must not start a duplicate 07 implementation; continue 06c. Both 07 completion boundaries remain required before 08.

### 前端CRUD交接（主会话1099）

- 已收到1103完成结果并核对前端提交 `3bcd286d373d2330d388cf6adb419b388355a978`。07前端独占实施阶段已结束；总执行者1101现在负责在06c稳定提交后核对本票第二边界，必要时可做明确的集成修复，不要重做CRUD。两个边界均满足后再进入08。主会话1099接手浏览器实际交互验收；其事实随后追加本票，不冒称多跳实时观测已验。

### 主会话浏览器 CRUD 验收（2026-11-12）

- 固定前端提交 `3bcd286d373d2330d388cf6adb419b388355a978` 的普通 git archive 临时副本，使用本机虚拟 SQLite 数据与未接入节点；后端不含正在修改的06c，因此以下仅证明CRUD交互，不代表实时多跳健康联调。未创建worktree。
- 实际页面验证：空态；少于两组的具体错误且保留名称；添加候选、已选组禁选、上下移及首尾边界按钮；同服务器重复路径被服务端拒绝且保留表单及顺序；合法两跳创建成功，展示所选专线域名/8443与第二节点公网/3443，均为待观测。
- 编辑显示共享影响及引用数；重排并改名后完整刷新，ID、名称、顺序保留；搜索所选专线域名命中，搜索未选地址不命中。虚拟停用链规则使引用数变为1，删除确认明确说明停用规则仍占引用，确认后服务端拒绝并保留链和弹窗；删除测试规则后引用变0，经页面正常删除链并回空态。
- 仅在虚拟验收库使会话过期，页面5秒轮询回到登录页；未见残留业务列表。测试链及测试引用规则均已删除。组2仅为本机验收改成节点2公网，未连接真实节点或生产。
- 已观察866×1175与1233×1225视口，后者长域名与编辑按钮无重叠。未验1366–1440、手机宽度、428及实时观测老化/非对称多跳，保持最终联调边界未勾选。临时服务与测试标签随后关闭。

- 428补验：同一固定提交与虚拟库，临时移除管理员并清理虚拟会话，HTTP GET /api/forward-chains实际返回428；已登录链页随后回到“首次设置”及密码/确认密码表单。管理员原记录已原样恢复，测试标签与回环服务已关闭。该项不再待验；其余未验项保持不变。最初仅移除管理员而保留有效会话属于人为不一致状态，未作为首次设置场景通过依据。

- 视口补验：使用独立临时配置的无头Chrome/CDP（非用户资料）实际设置390×844和1440×1000，检查创建空窗、含两跳及长专线域名的编辑窗截图。页面document宽度分别390/1440，无整页横向溢出；弹窗宽374/820，均在视口内。390下滚到保存/取消按钮成功，两跳上下移/移除按钮与长名称、换行域名无重叠；列表使用可见的横向滚动条承载宽表，不冒称所有列同时可见。1440下两跳长地址和保存按钮布局正常。纯虚拟测试链创建201、验后删除204；无头Chrome及面板进程均已关闭。截图仅在系统临时目录，未增加仓库报告或产物。该项补齐两种宽度的静态布局验收，实时多跳健康仍待最终联调。

### 第二边界：真实多跳管理 GET 集成（2026-11-12，冻结待独立审查）

- 以 `feat/ix` / `ccecd76` 为基线，完整消费本票 CRUD 提交 `3bcd286`、主会话浏览器事实及06c最终验收；保留现有9份修改中的md。此次只补真实 clusterharness 的管理页 GET 契约，没有重做 CRUD、修改生产 Go/前端代码或08端口界面，也没有新增假数据面或模拟转发结果。
- 新增测试辅助 `internal/clusterharness/chains_page_contract_test.go`，从真实 HTTP `/api/forward-chains` 读取每个 hop/observation；逐一检查全部规则 × 实际前驱 × 当前组全部主备候选，按真实下发到节点的 endpoint 和 grant 独立核对 `from_node_id/to_node_id/address_id/endpoint_config_digest/chain_fingerprint/generation`、候选顺序、所选地址及映射端口。该管理接口只有规则 `generation`，不虚构独立 grant_generation；独立确认来源使用真实 grant 的 AuthorizationGeneration/fingerprint。
- 扩展现有 `chain_failover_test.go`：B1下游全坏时，GET同次响应保留 A→B1 的新鲜 `suffix_unknown`、B1→C 的新鲜 `edge_unhealthy`、A→B2 的新鲜 `ready`。逐个故障/恢复 B1/B2/C1/C2 均核对GET与实际候选身份；最终目标真实拨号失败后，六条物理边仍为新鲜 ready，保持“未探测目标”的前端语义。
- 扩展 `chain_addresses_test.go`：同一B的坏公网地址不能借用好专线ready；同address_id改地址后，旧digest的迟到成功上报不成为当前观测，GET显示新digest/新fingerprint/更高规则代次且 unknown、fresh=false、sampled_at为空；停止注入后真实探测恢复ready。原事故“新版本替代”与未知不解除事故断言保留。
- 扩展 `chain_authorization_test.go`：两条真实启用端口共享同链，GET引用数2；通过真实PUT链替换首组，保持链ID并改变名称/内容，两条规则各自代次升高、指纹改变，旧流终止、新流真实传字节，独立直连端口仍通。切一条为直连后GET计数1，剩余停用引用仍导致删除409；删除最后引用后计数0、删除204。保留原组编辑、移入口、切模式和授权收敛断言。
- 扩展 `forward_chains_test.go`：真实两跳及三跳路径逐边GET显示当前ready及身份；断开全部控制后缓存链仍传字节，但管理GET逐边只显示 historical/fresh=false并保留相同摘要/指纹/规则代次，不能将离线缓存转发当新鲜就绪。

| 命令 / 阶段 | 退出码与实际结果 |
|---|---|
| `go test ./internal/clusterharness -run 'TestChain(Failover(Asymmetric\|EachHop\|Address)\|AuthorizationSharedEdit)' -count=1 -timeout=5m`（首轮新增断言） | **1**，包 **13.814秒**；唯一失败为新增辅助断言仍写死原专线域名，场景已通过API将同address_id改为127.0.0.1。改为独立核对实际下发snapshot的地址/端口；这是测试预期错误，不计生产功能红。 |
| `/usr/bin/time -p go test ./internal/clusterharness -run 'TestForwardChainHTTPRealMultiHopAndOffline\|TestChain(Failover(Asymmetric\|EachHop\|Address)\|AuthorizationSharedEdit)' -count=1 -timeout=5m`（修正后） | **0**，包 **15.443秒**，墙钟 **16.82秒**。真实多跳/离线、非对称、逐跳主备、地址旧digest及共享引用全部通过。 |
| 同一完整定向命令（自查补逐候选失败/恢复及目标故障GET后最终复验） | **0**，包 **15.063秒**，墙钟 **16.44秒**。本次最终5份测试文件均在该次执行中。 |
| `/usr/bin/time -p node --experimental-strip-types --test web/route.test.mjs web/src/ChainsData.test.mjs` | **0**，**14/14通过**，Node测试 **71.964毫秒**、墙钟 **0.10秒**；保留ready/edge_unhealthy/suffix_unknown/grant_failed/historical/unknown、10秒本地老化及ready未探测目标文案覆盖。 |
| `build_dir=$(mktemp -d /tmp/nbpass-ix07-build.XXXXXX)`，`/usr/bin/time -p npm --prefix web run build -- --outDir "$build_dir"` | **0**，完整TypeScript/Vite通过，3162模块、Vite **263毫秒**、墙钟 **2.77秒**；实际目录 `/tmp/nbpass-ix07-build.xN4cFG`，没有覆盖web/dist。 |
| `git diff --check`；5份测试 `gofmt -l`；`git status --short -- web/dist internal/nodebundle bin` | **0**；后两者无输出，产物范围无修改。 |

- 没有实际实现缺陷，因此没有人为修改生产实现来制造红绿，也未重跑昂贵旧binary矩阵/完整Go/全仓make。Node颜色环境及Vite临时目录/既有大chunk提示均非失败。此次实际修改仅上述5份Go测试（4份既有、1份新增）及本票Comments；其余既有md完整保留，未暂存或提交。
- 自查已核对新增断言独立于管理/operations投影，未按节点在线推算健康、未跨规则/地址/摘要合并；当前证据与历史/未知边界分开。**第二边界自动化已完成并冻结，交根编排者独立只读审查**；保留 claimed 和最终验收复选框，由编排者结合审查及浏览器收口。未打开浏览器，不宣称实时多跳健康、非对称观测或前端实时转历史已完成人工验收；该部分继续由主会话补验。未创建worktree/切分支/push/deploy/merge/子agent或辅助报告日志。

### 第二边界独立审查与收口

- 独立只读 `review_07_integration` 正式结论：**Spec compliant / Quality Approved；Critical 0 / Important 0 / Minor 0**。确认GET断言独立比对真实snapshot/grant，非对称同次响应、各候选故障/恢复、目标失败不污染边、旧digest迟到隔离、离线仅历史、共享链跨代次和引用保护完整；独立diffcheck/gofmt通过，无生产修复需要。
- 两个自动化边界均已完成并解除08依赖。验收复选框指已实现与自动化证据；主会话已记录的CRUD、401/428及390/1440静态布局事实保留，**实时多跳观测、非对称状态及实时转历史的浏览器人工验收仍待主编排者**，不宣称完整人工验收完成。

- 集成契约中文提交：**`77e95be`**「验证转发链管理页真实多跳观测与共享引用契约」，仅5份测试；暂存diffcheck及commit退出0，所有md/正式产物保留未提交。CRUD为`3bcd286`，共同完成07两边界。

### 主会话真实多跳浏览器补验（2026-11-12）

- 固定 `77e95be` 普通git archive临时副本；临时副本重新构建前端，将真实web.Assets交给现有clusterharness面板，仅加浏览器暂停点、按真实秒推进测试台假时钟和最后控制断开阶段。正式仓库源码/测试没有改变，不创建worktree，不模拟成功响应或转发结果。测试台2030假时钟与临时浏览器Date对齐，避免与本机2026时间差导致全部样本被判未来；每个阶段按测试台当前时刻校准，不改变采样或健康逻辑。
- 真实 `TestChainFailoverAsymmetricEdgesAndRecovery` 运行5个节点、WSS/yamux/TCP、专线域名与外部端口映射，实际业务传输及原主备断言保留。独立临时Chrome登录真实面板，初始6条边均显示“本边及后续路径就绪（未探测目标）”，1440×1100/1200截图已目视检查。
- B1两个后跳真实阻断后，浏览器DOM同次显示：A→B1“本边可达·后续路径未就绪”，A→B2就绪，B1→C1/C2“本边未就绪”，B2→C1/C2仍就绪；没有把A→B1本边判坏。展开本边证据显示实际from/to/address、规则ID/代次、摘要、指纹、采样时间与suffix_unknown/新鲜。非对称状态依据实际页面DOM，未将窄视口截图误称完整观测视觉截图。
- 恢复后6条边重新就绪，1440截图已检查。在确认6条当前就绪后，仅用临时浏览器网络阻断链GET，原有样本达到10秒新鲜界限后6条全部转为“历史·本边及后续路径就绪”，灰色且显示读取错误；此次从阻断到全部历史实测6042毫秒（样本在阻断前已有年龄，并非宣称阻断满10秒）。随后恢复请求，不伪造API结果。
- 断开全部5个真实控制连接后，GET实际6条均historical/fresh=false，页面同样6条“历史·状态未知”，刷新成功且无读取错误；1440截图已检查，不将缓存转发能力或控制离线当当前健康。
- 临时增设暂停点的单例最终退出 **0**：测试337.32秒，包338.057秒（含浏览器人工暂停）；原自动化无暂停的正式测试证据仍以前述15.063秒集合为准，不以本次替代全仓验证。测试自然退出并清理真实节点/面板，临时Chrome已停止。
- 本节补齐此前注明待验的实时多跳、非对称状态、真实恢复与控制离线历史、读取失败期间本地老化的浏览器边界。此前CRUD、401/428和390/1440静态布局事实保留；真实生产专线/NAT及生产重装/回滚依旧不是本机验收范围。
