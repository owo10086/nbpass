# 04 服务器出站源 IPv4 与本机地址应用校验

Status: resolved
Blocked by: 03（缓存按 spec 已定决策 7：A 实施）
Migration: 49（`outboundIPv4Migration`）
Parallel: 可与 05 的新模型/API/测试文件并行；共享 `migrations.go` 按 04→05 注册，05 等 49 落位后再注册 50。与 06a/06b 的 engine/proto/授权状态变更串行。

## 目标

服务器可设置可选出站 IP，仅在它连接最终目标时绑定该源 IPv4。直连入口和单跳出口先形成完整可测切片；06b 的链末跳复用同一路径。非本机地址报配置应用错误，不静默回退。按 A 写格式 2，让真实旧格式 1 读取器拒绝新缓存，避免忽略源字段后无绑定转发。

## 具体改动点

- 新文件 `internal/panel/outbound_ipv4.go` 定义迁移 49：`nodes.outbound_ipv4 TEXT NOT NULL DEFAULT ''`；提供 `GET /api/nodes/{id}/outbound`、`PUT /api/nodes/{id}/outbound`，形状 `{outbound_ipv4: string}`，空串清除。路由在节点相关 routes 注册；不存在 404、语法非法 400，保存走 `mutateForwarding`，成功 200 返回配置。面板仅检查可用 IPv4 语法，不能假装知道远端本机地址。
- `nodes.go:nodeView/listNodes` 增加 outbound_ipv4；保留 `renameNode` 只改展示名、不推进 revision 的行为。`web/src/Nodes.tsx` / `NodeServerData.ts` 编辑服务器时提供独立保存的「出站 IP」字段与状态，不把它当作公网地址观测或受管记录地址，也不在新建安装票据请求中夹带未支持字段。
- `api/control/v1/control.proto` 给 `ConfigSnapshot` 增加 `string outbound_ipv4 = 284`。`config_snapshots.go` 读取节点配置，仅对有能力的对应节点下发；`forwarding.State` 与 Restore、节点配置缓存持久化同一语义，缓存命中比较不能因 revision/digest 相等跳过实际源 IP 补齐。`make proto` 生成并提交协议源码。
- `internal/forwarding/engine.go:dialTargetWithin` 支持源地址参数，生产拨号用 `net.Dialer.LocalAddr=&net.TCPAddr{IP: ...}`、`tcp4`。现有 `Dependencies.DialTarget(ctx,address)` 是无源地址接缝，增加可注入的 `DialTargetFrom(ctx,address,sourceIPv4)` 与本机 IPv4 枚举接缝，不能让设置源 IP 时仍走忽略源地址的旧 DialTarget。
- 应用/恢复时核对本机接口 IPv4；非本机地址对该节点需要拨最终目标的规则报可见应用错误。监听规则、`tunnelServer` 的最终目标拨号共享校验结果；配置错误不能关闭该节点所有无关监听，且不能在运行时偷偷去掉源绑定。相同源配置再次应用、非本机地址在重启后出现等情况须重新核对。
- 保留 `DialTunnel` 和下一跳健康探测不绑定出站 IP。`direct.go` 的目标拨号和 `tunnel_limits.go:serveLimitedStream` 的最终目标拨号使用配置源地址；06b 的非末跳明确走另一条下一跳拨号路径，不得误用本方法。
- 门禁要求 `outbound_ipv4`：旧节点的相关直连规则或末跳授权必须被过滤并显示 01 的 blocker，不允许只省略该字段却继续无绑定转发；同节点只承担隧道传输的其他规则仍能工作。节点完成绑定、报错和缓存支持后才声明能力。

## 迁移与兼容要求

- 本票独占 49（48 后），05 固定占 50，不得争抢。默认空串保持系统路由；旧库规则/流量不变，换公网 IP 不改 outbound_ipv4。
- 保留先持久化撤销检查点、再应用、再报告的顺序；源地址变化属于配置变化，不能让失败后的重启恢复为无源限制的旧规则。复用 01 的 `StateCurrent=2`、读 1/2 写 2，不新增缓存版本：源字段和撤销检查点都随同一 `config.enc` 外层 `version: 2` 原子保存。新版读格式 1 缺字段等于未配置；真实旧 `config_store.go` 严格要求 version 1，在 Restore 前拒绝 2，不能保留旧格式副本供回退。旧版拒绝退出停转发；恢复按 spec 人工隔离缓存、空启动、重连取能力内当前配置，不承诺自动重连或离线带源配置回滚。
- 单跳出口的错误至少通过节点应用状态和引用端口的同步原因可见，不能把「出口源 IP 无效」伪装成入口到出口的链路故障；06a 定义逐跳结果、06c 接入状态。

## 测试要求

- 单元：IPv4/空串/IPv6/非本机地址，接口枚举失败、应用和 Restore 再校验；指定源时调用正确拨号接缝，隧道拨号未绑定；配置摘要及实际状态包含源 IP，写盘失败不确认、不恢复无绑定规则。
- 公共回滚验收（A）：用 01 固定来源的真实旧读取代码/旧二进制加载新版实际写出的直连/单跳源 IP 格式 2 缓存（含撤销检查点），断开控制后启动，必须明确拒绝且无业务监听/目标连接，不得无绑定连目标；记录实际返回与目标观察，不能只测新版读新版。人工隔离后断网空启动仍停转发，重连只接收能力内当前配置；新版读格式 1、同版本离线重启、源变更/撤销写盘失败均通过。无旧版拒绝保证就阻断，不改选 B。
- clusterharness 新建 `outbound_ipv4_test.go`：真实本机回环地址作为合法源，直连 A→目标和 A→B→目标由目标观察源地址；A/B 的下一跳连接不受源绑定。为 B 设置一个接口枚举确认不属于本机的 IPv4，必须有应用错误且无目标连接；清除后恢复。重启节点后源绑定仍生效，旧能力节点被拦而无关端口正常。物理地址隔离可注入，源地址正确性必须以真实目标收到的连接为断言。
- 运行 `go test ./internal/forwarding ./internal/node ./internal/panel -timeout=10m`、`go test ./internal/clusterharness -run 'Test.*OutboundIPv4' -timeout=10m`，前端字段序列化测试及构建。

## 验收标准

- [x] 可设置、回读和清除服务器出站 IP；名称编辑与出站配置不会相互覆盖。
- [x] 目标实际看到选定源地址；下一跳 WSS 和健康探测不绑定。
- [x] 非本机地址产生应用错误且无回退，错误能定位到受影响端口。
- [x] 离线重启、部分失败和写盘故障不丢失源限制；旧节点不收到新配置。
- [x] A 的源 IP/撤销缓存统一写格式 2；真实旧读取器拒绝且停转发，人工空启动与重连过滤边界已验证。

## 不做的事

不配置网卡或路由，不添加公网 IP，不做 IPv6、监听源绑定、按规则覆盖或源地址轮换；不改变管理/检测连接；不提交构建产物。

## Comments

### 实施决定与审查输入（2026-11-12）

- 本票仅追加迁移 **49**（48 后），`nodes.outbound_ipv4` 默认空串。独立 GET/PUT outbound API 保存、回读与清除，IPv4 语法不等于远端本机校验；保存通过既有 `mutateForwarding` 事务与快照编译。名称编辑仍只改展示名、不推进 revision。服务器列表返回源字段，编辑弹窗独立保存出站 IP，名称/安装票据 payload 仅含名称。
- `ConfigSnapshot.outbound_ipv4 = 284`，保留 280/281；已执行 `make proto` 真实生成 `control.pb.go`，未手改生成文件，未占用 06a 的 NodeReport 285–287。源字段进入确定性摘要、实际 State、撤销检查点及同一个 `config.enc` 格式 2；继续读 1/2、只写 2，不增加缓存版本、不留格式 1 副本。
- 最终目标拨号增加 `DialTargetFrom` 与本机接口 IPv4 枚举接缝；生产默认实现使用 `net.Dialer.LocalAddr` 与 `tcp4`。指定源时不能落入旧的无源 `DialTarget`。直连和出口最终目标使用同一原子校验结果；`DialTunnel`、WSS/yamux 载体与下一跳健康路径保留独立无源拨号。配置应用、相同配置再应用和 Restore 均重新枚举，非本机或枚举失败只阻断该节点的最终目标消费者，保留只承担隧道传输的监听与规则；运行时不去掉源绑定。
- 源变化先持久化带新源的撤销检查点，再更新运行时和最终实际状态。变化或失效的直连不进入可恢复检查点；最终写盘失败不确认新版本，重启不恢复旧的无绑定规则。有效源恢复后重新核验并清理历史源错误。缓存匹配还检查源字段与当前本机地址，不能仅凭 revision/digest 跳过补齐。
- 能力要求只落到最终目标拨号者：旧入口的相关直连、旧出口的相关 grants/peers 被过滤且返回准确 blocker；同节点只承担传输的其他端口继续工作。B 的源错误同时显示在 B 应用状态和引用端口的同步原因，原因保留出口服务器名与出站 IP，避免误报为入口链路失败。
- 独立审查修复轮 1：已确认配置的源地址消失后，同 revision/digest 重新应用必须撤回完整确认。引擎在已提交的源失败检查点及当前状态清空完整 AppliedRevision/Digest，保留无关逐规则成功。面板只对**当前认证会话声明 outbound 能力、当前实际快照源非空且与节点配置一致、精确当前 revision/digest、已提交撤销检查点、报告已撤回完整确认且指明当前源校验失败**的结果允许更新；完整确认撤回与逐规则失败同事务保存。真实 node.exchange 主循环串行发送 configReport，gRPC 同流有序；不新增序号。普通/legacy 重放保护保持，旧源、旧摘要、旧版本、旧会话、旧进程、无能力及无检查点报告不能回退当前成功。
- 审查 Minor 的生产默认拨号保护已补：仅注入本机枚举，让 OS 实际不可绑定的 `192.0.2.253` 通过应用枚举；保留生产默认 `DialTargetFrom`，真实目标零连接并观察明确 `bind:` 错误。原合法回环源场景继续用目标实际收到的连接确认源地址，未更改网卡、未 skip。

### 红绿证据与验证

- 必要红测试先复现 API 缺失（PUT 404 而应 400，panel 4.384 秒）、非本机源仍启动直连（forwarding 0.243 秒）；实现后 `go test ./internal/panel ./internal/forwarding -run TestOutboundIPv4 -count=1 -timeout=2m` 退出 0，分别 1.943/1.754 秒。扩展 `go test ./internal/forwarding -run TestOutboundIPv4 -count=1 -timeout=2m` 退出 0，1.648 秒。
- 单跳红测试通过真实 WSS/yamux/TCP 观察 B 已报源错误而引用端口仍“正常”（`go test ./internal/clusterharness -run TestOutboundIPv4 -count=1 -timeout=2m`，退出 1，6.229 秒）；补投影后最终同命令退出 0、1.751 秒。期间测试先选非回环实际接口到回环目标遇内核路由超时（3.667 秒），按票要求改用实际枚举的合法回环地址；清除 B 配置后测试曾在 B 应用确认前发业务（1.259 秒），已明确等待 B 当前配置正常。
- `go test ./internal/node -run TestActualV1Reader -count=1 -timeout=4m -v` 首轮退出 1（5.002 秒，出口夹具误用 direct kind）；更正为真实 tunnel grant 后退出 0，5.866 秒。直连/单跳出口 × 最终实际缓存/撤销检查点四格均由新版真实 Persist 写出加密格式 2，再由固定真实旧读取器与旧 Run 边界读取，均明确格式拒绝、Restore 前停止、业务监听不存在、目标观察连接 **0**；人工原样隔离后断网空 Run 返回 nil 且继续零业务连接、不生成 fallback 配置。
- 固定真实旧源码仍为 `3899928362a75b58ac74ff6844652d319760b656`，01 的三个原始源文件未改；仅扩展测试夹具 `reader_test.go`。额外从该提交完整源码临时构建实际旧程序，本机 SHA256 **`32054cfe3cfe3c040ded228e1052c6f145d70eb847714f1d60540e9968c18acd`**。该程序读取新版实际出站缓存返回 **exit status 1 / 节点配置缓存格式或身份不匹配**，无业务监听/目标连接。原样隔离后真实 TCP 控制代理断开，空启动保持停转发；重连仅收到源规则已过滤的当前快照，显式清除源后才恢复普通当前规则。生产回滚目标与此源码的对应核验仍由 09 完成，本票不冒称生产演练。
- `go test ./internal/clusterharness -run 'Test.*OutboundIPv4' -count=1 -timeout=10m -v` 初次完整矩阵退出 0、5.825 秒；去掉测试自定义源拨号、改为生产默认源拨号后退出 0、6.284 秒。恢复地址出现的红测试 `go test ./internal/forwarding -run TestOutboundIPv4RestoreRechecksAddress -count=1 -timeout=2m` 退出 1、1.301 秒（历史源错误未清）；修复后 Outbound 定向退出 0、0.604 秒。
- 审查轮 1 红证据：`go test ./internal/forwarding -run 'TestOutboundIPv4(SameSnapshot|DefaultDialer)' -count=1 -timeout=2m` 退出 1、1.159 秒，确实保留了完整确认。真实重发回归 `go test ./internal/clusterharness -run TestOutboundIPv4SameSnapshot -count=1 -timeout=2m` 在禁用面板新准入时退出 1、7.398 秒：同 revision/digest 的 A 源已消失，节点仍在线且 API 仍显示“正常”。初次该回归另遇 fake clock 连续推进导致控制超时（6.311 秒），已在每步推进后留实际收发时间。测试夹具曾把无返回的 awaitNode 当返回值、无能力夹具误清控制协议，均已更正，未放宽业务断言。
- 修复后 `go test ./internal/panel -run 'Test(OutboundIPv4|PartialRuleReportIsolationAndLateFailure|ConfigReportCannotReplaceConfirmedDigestOrRevision)' -count=1 -timeout=3m` 退出 0、3.481 秒；旧源/旧版本/旧摘要/旧会话/旧进程及无检查点/无能力逐项保持确认，当前源失败可见且恢复后重新正常。`go test ./internal/clusterharness -run 'Test.*OutboundIPv4' -count=1 -timeout=10m -v` 最终退出 0、**7.170 秒**：真实目标观察回环源、真实默认目标拨号、B 非本机错误与端口归属、清除恢复、离线双节点重启、A 源无效后新建无绑定 WSS 载体仍正常、旧入口直连被拦而传输端口正常、旧出口 grants/peers 被撤，以及同配置源消失/恢复均通过。一次测试曾在新建业务流之前等待新 WSS 计数而超时（10.027 秒），改为先传真实字节再断言新载体计数，未删除场景。
- `go test ./internal/forwarding ./internal/node ./internal/panel -timeout=10m` 初轮退出 0，forwarding **11.201 秒**、node **13.848 秒**、panel **49.577 秒**。审查修复后的三包及 compatibility 最终结果见下文。
- `make proto` 两次退出 0，生成物一致，字段仅增加 ConfigSnapshot 284，gRPC 文件无变化。`npm --prefix web test -- src/NodeServerData.test.mjs` 退出 0，**9/9**、80.906417 ms；新字段序列化测试最初明确失败（serverNamePayload 缺失，70.517625 ms），实现后单文件 5/5（58.641417 ms）。`node --experimental-strip-types --test web/route.test.mjs web/server-groups.test.mjs web/src/*.test.mjs` 最终退出 0、**31/31**、102.564083 ms。首轮与 make 的 npm ci 并行，react 正被重新安装导致 1 项模块暂缺（84.025875 ms）；ci 完成后完整重跑通过（31/31、83.537792 ms），属于测试调度错误，未修改测试来绕过。
- 已启动的 `GOFLAGS=-timeout=30m make test` 完整运行退出 **2**：clusterharness **397.094 秒**通过，forwarding 13.765 秒、node 20.535 秒、panel 63.178 秒等通过，唯一失败为 compatibility 旧测试仍将本票已经实现的 outbound_ipv4 当作“未实现”。已将其改为已实现能力断言，继续禁止 forward_chains/ix_addresses，定向 compatibility 已退出 0（1.306 秒）；该全量启动早于最终审查修复，不冒称最新代码全仓已绿。按编排要求不重复昂贵全量，09 统一最终全量。

### 交接与边界

- 实施完成时曾保持 `claimed`，等待主编排者独立只读审查；复核通过后按下节记录标记 resolved 并提交功能代码，文档仍留待统一提交，其他票并行文档完整保留。没有创建工作树、切分支、push/deploy/merge，没有生成额外报告、日志或方案。
- `bin/nbpass-node-ix02` 与 `-linux-amd64` 未覆盖。构建生成的 `web/dist`、nodebundle 已在验证结束后只精确恢复本轮产物，功能票不提交发行产物。浏览器验收未在实施者执行，交主编排者；05+ 未提前实施。

- 最终修复后 `go test ./internal/forwarding ./internal/node ./internal/panel ./internal/compatibility -timeout=10m` 退出 **0**：forwarding **11.390 秒**、node **14.677 秒**、panel **50.098 秒**、compatibility **0.783 秒**。包括真实旧读取四格、Restore/枚举失败、同源重核验、源变更与清除的最终写盘失败、普通/legacy 迟到报告保护及当前源失败恢复。
- 最终 `npm --prefix web run build` 退出 **0**，TypeScript 与 Vite 均成功（Vite 329 ms）；保留既有大 chunk/颜色环境警告。`git diff --check` 退出 **0**。只精确恢复本轮改动的三个已跟踪 dist 文件、nodebundle gzip，并移除本轮生成的 index JS/CSS；`git status --short web/dist internal/nodebundle` 无输出。
- 已复核 01 的三份真实旧源 SHA256 与 02 两个保留产物 SHA256 全部仍与原记录一致。该核验时所有源码尚未提交，随后独立复核与提交见下节；最终全仓绿结论留 09，浏览器检查留主编排者。


### 独立审查与提交

- 主编排者安排的独立只读审查及原审查者精确复核已通过：Spec 符合 04，Quality 通过，**Critical 0 / Important 0 / Minor 0**。修复轮 1 的同配置源地址失效确认掩盖（Important）与默认生产源绑定测试强度（Minor）均为 **ADDRESSED**，没有通过删测试或新增协议序号绕过。
- 本票自动化验收项全部勾选，Status 标记 resolved。浏览器交互检查仍交主编排者；09 的实际生产回滚目标核验、人工生产恢复演练及最终全量仍待执行，本票不虚称通过这些边界。
- 主编排者明确授权后按后端/协议/真实测试与服务器界面拆分中文功能提交；所有 `.md` 仍不进入功能提交，保留 02/03/09 并行记录，真实提交 SHA 见下条。
- 实际代码提交：**`34d508ae07b10a5eeba723a26a240f46a011ce1d`**「支持服务器最终目标拨号绑定出站 IPv4」（24 个后端/协议/测试文件）；**`3f3407fe90dbe58e4955663a777a0eb0e047b9c9`**「增加服务器出站 IP 独立保存界面」（3 个前端文件）。提交前后 `git diff --check` 均退出 0；提交后暂存区为空，工作区仅保留 02/03/04/09 四份 `.md` 未提交，dist/nodebundle/bin 未纳入。无 push/deploy/merge，未实施 05。

### 主编排者浏览器验收（2026-11-12）

- 基于固定提交 `3f3407f` 的临时副本、本机面板、纯虚拟测试库与未接入节点，通过 Codeg 内置浏览器实际输入、点击和完整刷新验收；视口为 818×1175 CSS px，未操作生产服务。
- 名称输入未保存值、出站输入 `127.0.0.1` 并仅点击「保存出站 IP」：提示已保存，未保存名称保留在表单；完整刷新后名称仍为原名，出站已保存为 `127.0.0.1`。
- 出站改为未保存的 `127.0.0.2`，名称改为 `IX独立保存验收1` 并仅点击底部「保存」：重开后名称已更新，出站仍为 `127.0.0.1`。两条保存路径互不夹带。
- IPv6 `::1` 保存被拒绝，字段提示「出站 IP 须为可用 IPv4 地址或空串」；清空后单独保存成功，完整刷新并重开后出站仍为空，名称保持已保存值。
- 虚拟节点未接入，实际源地址应用和错误投影以本票集成测试为证据；其他屏宽、会话失效及生产网络演练未验。临时面板及浏览器标签已关闭。以上事实留待最终统一文档提交。

### 主编排者最终补验（2026-11-12）

- 本次正式构建、隔离本机面板及未接入虚拟服务器，补查1440×1000与390×844编辑窗；出站IP输入和独立保存并排、说明及名称保存分区可读，窄屏纵向滚动，未改变业务代码。
- 在编辑窗仍打开时实际logout204，再点击「保存出站 IP」，真实401使页面返回登录，没有显示保存成功。此前818px互不夹带、IPv6拒绝/清空回读事实仍有效；本轮不冒称实际节点源应用或生产网络已验。临时服务和Chrome已停止。
