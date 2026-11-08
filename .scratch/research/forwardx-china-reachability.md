> 来源：Codex 调研 Forwardx 国内可达性

**1. 一句话结论：该项目的数据来自自有 Go Agent 的主动探测及面板汇总，两套“国内健康”都是节点向指定目标的出站探测；未发现内置第三方全国拨测或能够确认 IP/端口“被墙”的数据来源。**

调研基于 `/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx` 当前源码，提交为 `f4a32c2e1f6d2dff57da19932f4900233a75040b`。只读搜索面板、客户端、Go Agent、共享类型、数据库结构、文档、脚本、依赖清单和插件；另只读检查了 3 个插件压缩包中的 56 个文件。未运行项目、测试或真实网络探测，未修改项目文件；仅按要求写入本回报。

**2. 实际存在的机制与探测方向**

**（1）转发组的“国内健康度”：成员入口 → 国内目标。**

- **发起方：**成员主机自己的 Agent；成员是隧道时，使用该隧道的入口主机 Agent。面板通过心跳下发任务，标记为 `probeType: "china"`。
- **目标、协议：**配置的域名/IP及端口，留空默认 `www.189.cn:80`；执行 TCP 建连，不请求该网站的 HTTP 页面，也不向它查询其他节点是否可达。适用于 `failover`、`entry` 组。
- **判定：**普通自动 TCPing 每轮并发 3 次连接，共享 2 秒期限，至少一次成功即本轮可达，延迟为成功样本平均值；再经 Agent 的持续异常/恢复窗口稳定状态，默认异常 60 秒、恢复 120 秒，首次成功可直接健康。面板收到结果后保存 `chinaHealthStatus`，健康数据超过 5 分钟视为过期。
- **用途、动作：**故障转移组根据健康与优先级切换 DDNS，可按配置自动回切；入口组根据健康剔除/恢复 DDNS 地址。未配置 DDNS 域名时仅更新健康状态。这套机制不调用商家换 IP 接口。

证据：[server/repositories/forwardGroupRepository.ts:322](</Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/repositories/forwardGroupRepository.ts:322>)（默认目标）、[同文件:1619](</Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/repositories/forwardGroupRepository.ts:1619>)（来源与目标）、[同文件:1720](</Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/repositories/forwardGroupRepository.ts:1720>)（保存健康）、[同文件:4341](</Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/repositories/forwardGroupRepository.ts:4341>)（入口组选择）、[同文件:4837](</Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/repositories/forwardGroupRepository.ts:4837>)（无域名处理及后续 DDNS 切换）；[agent/metrics.go:2038](</Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/agent/metrics.go:2038>)（TCP 采样）、[agent/forward_group_health.go:40](</Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/agent/forward_group_health.go:40>)（稳定窗口）、[server/forwardGroupHealthRecheck.ts:9](</Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/forwardGroupHealthRecheck.ts:9>)（过期时间）。

**这验证的是“该入口节点能否连到指定国内目标”，没有从国内反向连接该节点的入口 IP/业务端口。**

**（2）主机服务的“国内健康检测”：指定单个 Agent → 管理员指定固定公网 IP。**

- **发起方：**服务配置中唯一选定的主机 Agent；不是面板服务器。它与上一项转发组国内健康度是两套配置和状态。
- **目标、协议：**管理员填写的固定公网 IP；支持 Ping（ICMP），或 TCPing 到指定端口。此模式禁止 URL 探测、禁止域名，探测间隔不得超过 300 秒。**代码只校验固定 IP 和安全公网地址，没有验证目标实际属于中国大陆，也没有要求发起主机位于国内。**
- **判定：**面板核对报告主机、服务、目标、方法、端口和样本计数，用 `probeSuccesses > 0` 判本轮成功。默认同时满足“连续至少 3 轮失败”及“从首次失败持续至少 120 秒”才进入异常；默认连续 2 轮成功恢复。过密报告被忽略，报告间隔超过 `max(120 秒, 3×配置间隔)` 会重置连续计数。
- **用途、动作：**异常/恢复通知管理员；明确启用自动换 IP 并配置接口后，可自动执行换 IP。通知原文为：“连续探测失败，疑似国内连通异常；不等同于确认被墙”。

证据：[server/hostProbeHealth.ts:69](</Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/hostProbeHealth.ts:69>)（配置限制）、[同文件:220](</Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/hostProbeHealth.ts:220>)（报告校验、通知与动作）；[shared/hostChinaHealth.ts:22](</Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/shared/hostChinaHealth.ts:22>)（默认阈值）、[同文件:45](</Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/shared/hostChinaHealth.ts:45>)（状态转换）；[client/src/components/hosts/HostChinaHealthSettings.tsx:113](</Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/client/src/components/hosts/HostChinaHealthSettings.tsx:113>)（界面说明）。

**（3）通用主机探测服务：可以手动让国内节点充当探针。**

- **发起方：**管理员配置 `hostScope=all/exclude/specific` 及主机 ID，面板将任务分给对应 Agent。可以手动选择部署在国内的主机，探测其他节点。
- **目标、协议：**指定域名/IP及端口，使用 TCPing、Ping；也可指定 HTTP/HTTPS URL。TCPing 为上述 3 次 TCP 建连采样；周期 Ping 每轮请求 5 次，使用原生 ICMP，不可用时回退系统 `ping`。
- **判定：**记录成功次数、延迟与超时。URL 模式执行全新直连 GET，不使用 HTTP 代理、不跟随重定向，HTTP 状态码须等于配置的 `expectedStatus`；计时包含 DNS、TCP、TLS及首字节。
- **用途、动作：**写入主机服务延迟/可达历史；只有另外启用上一项健康配置，才进入国内健康与自动换 IP 流程。普通服务可以分配给多台主机，国内健康模式限制为一台。

证据：[server/routers/hosts.ts:109](</Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/routers/hosts.ts:109>)（配置）、[server/repositories/hostProbeServiceRepository.ts:183](</Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/repositories/hostProbeServiceRepository.ts:183>)（按主机分配）、[server/agentReportRoutes.ts:1484](</Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/agentReportRoutes.ts:1484>)（保存报告并交健康模块）；[agent/metrics.go:1924](</Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/agent/metrics.go:1924>)（协议执行）、[同文件:2300](</Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/agent/metrics.go:2300>)（Ping）、[agent/url_probe.go:22](</Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/agent/url_probe.go:22>)（URL 判定）。

**所以，“自有国内节点 → 其他节点 IP/端口”可以配置；没有发现专门自动识别国内探针、按省份/运营商布点并汇总全国连通性的机制。**这些任务按主机 ID 分配，没有中国地域选择条件。

**（4）规则自动 TCPing 与人工规则自测：规则 Agent/隧道出口 → 业务目标。**

- **发起方、目标：**普通规则由执行该规则的 Agent 探测规则目标 IP/端口；周期隧道规则的最终目标探测只分配给隧道主出口 Agent，入口和中转不会绕过隧道直接探测最终目标。
- **协议、判定：**TCP、`both` 规则使用 TCP 建连；UDP-only 规则改用 Ping，**不验证 UDP 业务端口是否可用**。自动采样采用上述 TCP/Ping 规则。人工自测通过面板排队给 Agent，普通 TCP 单次期限 1.5 秒，依赖链路运行时的任务可等待就绪并重试，以 `targetReachable` 上报。
- **用途、动作：**规则延迟、丢失/超时、自测结果及相关链路合成；携带转发组健康配置的自动报告还参与组健康处理。人工隧道规则自测包含各隧道分段，并为各实际出口增加“出口 → 最终业务目标”测试。没有全国或 GFW 判定。

证据：[agent/metrics.go:1613](</Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/agent/metrics.go:1613>)（自动规则来源与协议）、[server/ruleLatency.ts:104](</Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/ruleLatency.ts:104>)（限定出口）、[shared/latencyProbe.ts:10](</Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/shared/latencyProbe.ts:10>)（UDP 映射 Ping）、[server/routers/rules.selfTest.ts:114](</Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/routers/rules.selfTest.ts:114>)（选择实际规则及排队）、[agent/selftest.go:133](</Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/agent/selftest.go:133>)（实际执行）、[server/ruleTunnelManualProbe.ts:14](</Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/ruleTunnelManualProbe.ts:14>)（隧道规则逐段和出口目标）。

**（5）转发链链路探测：前一节点 → 下一节点；规则自测可追加最后节点 → 业务目标。**

- **发起方、目标：**按已配置转发链和入口组拓扑，面板生成每段 `fromHostId`，由该主机 Agent 探测下一节点。
- **协议、判定：**转发链页面的“链路自测”明确指定 `method: "ping"`、`includeFinalTarget: false`，只测节点间 IP 的 Ping；没有规则模板时的周期链路探测也默认 Ping。对链中的具体规则执行自测，才按规则协议选择 TCP/Ping，并追加最后节点到业务目标；TCP 段使用相应监听端口。
- **用途、动作：**逐段可达结果、延迟历史、链路自测汇总；不将 Ping 成功解释为节点入口 TCP 端口或业务成功。

证据：[server/services/forwardGroupService.ts:376](</Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/services/forwardGroupService.ts:376>)（链路自测方法及发起主机）、[server/repositories/forwardGroupRepository.ts:1358](</Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/repositories/forwardGroupRepository.ts:1358>)（逐段目标、默认 Ping、最终目标）、[server/routers/rules.selfTest.ts:59](</Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/routers/rules.selfTest.ts:59>)（规则自测）。

**（6）隧道链路自动探测与人工测试：入口/中转 → 下一跳。**

- **发起方、目标：**直接隧道为入口 Agent → 出口连接地址及监听端口；多跳为各段来源 Agent → 下一跳；中转故障转移、多出口等按其候选路径分别生成探测。
- **协议、判定：**普通隧道为 TCP 建连。ForwardX WireGuard v2 特例通过 WireGuard runtime 对 peer 端口执行 TCP 探测，自动探测期限 8 秒，runtime 尚未就绪时可跳过该次上报。人工逐段测试至少有一条候选路径的全部段成功，整体才成功；结果延迟先按路径求和，再取成功路径中的最大值。
- **用途、动作：**更新隧道链路可达状态、自测结果、逐段延迟和历史；来源由隧道拓扑确定，没有自动指定“中国大陆探针”。

证据：[agent/metrics.go:1589](</Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/agent/metrics.go:1589>)（隧道任务）、[同文件:1937](</Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/agent/metrics.go:1937>)（WireGuard 特例）、[agent/wireguard_runtime.go:1830](</Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/agent/wireguard_runtime.go:1830>)（实际 peer 探测）、[server/tunnelManualProbe.ts:23](</Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/tunnelManualProbe.ts:23>)（拓扑及来源目标）、[同文件:80](</Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/tunnelManualProbe.ts:80>)（成功规则）、[同文件:169](</Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/tunnelManualProbe.ts:169>)（结果落库）。

**（7）Looking Glass：用户指定 Agent → 用户指定公网目标。**

- **发起方、目标：**浏览器提交所选 `hostId`、公网 IP/域名、方法和端口；面板解析并校验目标、下发任务；真正发包的是所选 Agent。选国内 Agent、填其他节点 IP/端口，就能得到该国内来源到该节点的单点结果。
- **协议、判定：**TCP 模式默认端口 443，可改端口，执行一次 10 秒期限的 TCP 建连。Ping 调系统 `ping -4/-6 -c 4 -W 3`；Traceroute 和 MTR 调系统工具，未强制 TCP，采用工具默认方式；命令总期限 30 秒。面板依据 `timedOut`、`exitCode===0` 设置任务状态，保存原始输出。
- **用途、动作：**人工查看连接、延迟和路由；此任务完成逻辑仅更新测试状态，没有自动换 IP 或全国被墙判定。

证据：[server/routers/lookingGlass.ts:188](</Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/routers/lookingGlass.ts:188>)（来源、目标与下发）、[agent/main.go:4463](</Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/agent/main.go:4463>)（TCP 与命令执行）、[同文件:4536](</Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/agent/main.go:4536>)（命令参数）、[server/lookingGlassAgentTasks.ts:201](</Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/lookingGlassAgentTasks.ts:201>)（结果判定）。

**（8）运行时故障转移代理：承载代理的 Agent → 各候选转发目标。**

- **发起方、目标、协议：**`failoverProxy` 所在 Agent 对 `p.spec.Targets` 的 IP/端口进行 TCP 建连健康检查，每 5 秒调度一轮，各目标期限 2 秒。
- **判定、动作：**持续失败/恢复达到 `FailoverSeconds`/`RecoverSeconds` 后改变目标健康状态，选择备用目标；真实业务 TCP 拨号失败也会标记目标失败并尝试其他目标。支持按配置自动回切。这是当前代理到目标的故障转移，没有国内地域判断，也不触发商家换 IP。

证据：[agent/main.go:12145](</Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/agent/main.go:12145>)（主动检查）、[同文件:12103](</Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/agent/main.go:12103>)（备用/回切）、[同文件:12229](</Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/agent/main.go:12229>)（业务拨号失败切换）。

**（9）iperf3 测速：用户客户端 → 选定节点的临时 iperf3 服务端。**

- **发起方、目标、协议：**面板请求选定 Agent 启动 `iperf3 -s -p <端口>`，给用户生成 `iperf3 -c <节点地址> -p <端口>` 及反向下载 `-R` 命令。生成命令默认是 iperf3 TCP 测速，由用户客户端执行。
- **判定、用途：**Agent 上报服务端启动/停止状态和输出；实际连通与吞吐由客户端直连测试。启动前本地绑定端口成功，只代表可监听。项目没有在此接入全国测速服务，也没有以服务端启动成功判定国内可达。

证据：[server/routers/lookingGlass.ts:157](</Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/routers/lookingGlass.ts:157>)（客户端命令）、[agent/main.go:4328](</Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/agent/main.go:4328>)（启动服务端）、[同文件:4423](</Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/agent/main.go:4423>)（本地端口绑定检查）。

**（10）容易被误认为连通探测的机制。**

- **入口组未启用国内健康时：**给成员 Agent 下发 `probeType: "entry"`、`method: "self"`，目标为空、端口为 0；Agent 直接生成成功样本，再参与入口组健康和 DDNS 选择。**这没有发送网络探测。**证据：[server/repositories/forwardGroupRepository.ts:1593](</Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/repositories/forwardGroupRepository.ts:1593>)、[agent/metrics.go:1929](</Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/agent/metrics.go:1929>)。
- **网络诊断：**只检查配置、权限、Agent 最近通信与资源指标；代码明确“配置体检不发送探测、不验证目标业务成功”。**规则创建时的 `checkPort`：**核对端口范围、权限与数据库冲突，不从面板连接节点端口。证据：[server/networkDiagnostics.ts:24](</Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/networkDiagnostics.ts:24>)、[同文件:46](</Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/networkDiagnostics.ts:46>)、[server/routers/rules.ports.ts:81](</Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/routers/rules.ports.ts:81>)。

**自动换 IP 的完整依据与接口**

自动换 IP **确实存在**，与第（2）项主机服务国内健康检测关联，流程如下：

1. **触发依据：**指定 Agent 到固定公网 IP 的连续 Ping/TCPing 失败，达到次数与时长阈值；不是向任何“被墙查询 API”取得结论。需启用健康检测、`autoSwitch`、配置 hook，且 Agent 在线上报换 IP 能力。执行前再次核对仍异常、服务配置及主机地址未变化。
2. **接口与执行方：**管理员配置 HTTP/HTTPS URL，支持 GET/POST/PUT、请求头、body、默认 15 秒超时，以及 `{{hostId}}`、`{{serviceId}}`、`{{ip}}`、`{{incidentId}}` 模板变量；没有写死特定商家的换 IP API。面板排队，由被检测主机自己的 Agent 请求该 URL。Agent 先向面板 POST `/api/agent/ip-switch-claim` 做执行前校验，调用商家接口后 POST `/api/agent/ip-switch-result` 上报。
3. **频率与防重复：**默认请求间隔 3600 秒，滚动 24 小时最多 3 次，历史按主机共用；结果不明确时保护后续自动请求，需核对后解除。`actionBlockedReason` 中的 `blocked` 指操作被冷却、限额、Agent 状态等阻止，不是 GFW 判定。
4. **成功依据：**HTTP 2xx/JSON 成功字段只是接口回执；真正完成以 Agent 随后上报的、同地址族主公网 IP 确实变化为依据，已知副 IP 或另一地址族变化不算证明。观测后更新仍指向旧 IP 的相关入口/隧道入口/首选地址，并刷新关联转发和 DDNS。**换 IP 完成与国内连通恢复分开判断，恢复仍看后续探测。**

证据：[server/hostProbeHealth.ts:101](</Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/hostProbeHealth.ts:101>)（触发、保护与请求）、[shared/hostChinaHealth.ts:4](</Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/shared/hostChinaHealth.ts:4>)（接口格式与默认配置）、[server/hostIpSwitchProvider.ts:5](</Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/hostIpSwitchProvider.ts:5>)（模板及仅 Agent 执行）、[agent/ip_switch.go:407](</Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/agent/ip_switch.go:407>)（claim、商家请求与结果上报）、[同文件:236](</Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/agent/ip_switch.go:236>)（HTTP/JSON 回执）、[server/hostIpSwitchObservation.ts:20](</Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/hostIpSwitchObservation.ts:20>)（实际 IP 变化）、[同文件:76](</Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/hostIpSwitchObservation.ts:76>)（地址/转发/DDNS 同步）。

**3. 搜索过但未发现接入的第三方服务**

以下名称/域名在当前仓库全文搜索均未发现命中，插件包内容搜索也未发现相关接入：

| 服务 | 搜索名称/域名 | 结果 |
|---|---|---|
| ITDOG | `itdog` | 未发现 |
| 拨测网 | `boce`、`boce.com` | 未发现 |
| 阿里云拨测 | `boce.aliyun.com`、阿里云拨测 | 未发现 |
| 17CE | `17ce` | 未发现 |
| TCPTest | `tcptest` | 未发现 |
| 站长工具 | `chinaz` | 未发现 |
| ping.pe | `ping.pe` | 未发现 |
| Globalping | `globalping` | 未发现 |
| Check-Host | `check-host`、`check.host` | 未发现 |

另搜索了 `probe`、`tcping`、`国内`、`china`、`gfw`、`blocked`、`墙`、`拨测`、`全国连通` 等，并沿命中追到任务生成、Agent 执行、结果处理和自动动作。未发现内置全国省份/运营商拨测节点库、自动从国内反测指定节点入口的调度器，或独立的 GFW/被墙确认规则。普通 `blocked` 命中包括安全地址限制、协议拦截、操作等待等，不能当作被墙状态。

**实际出现、但不提供国内可达性结果的外部数据来源：**

| 来源 | 实际用途与判定 | 代码证据 |
|---|---|---|
| `api.ipify.org`、`ipv4.icanhazip.com`、`v4.ident.me`；IPv6 对应 `api6.ipify.org`、`ipv6.icanhazip.com`、`v6.ident.me` | Agent 用 HTTP GET 查询自己的公网 IP；IPv6 优先取本地公网地址。返回须能解析成 IP，供地址上报及换 IP 观察使用；不拨测节点业务端口。 | [agent/main.go:14438](</Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/agent/main.go:14438>)、[同文件:14489](</Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/agent/main.go:14489>) |
| `ipapi.co/<IP>/json/` | 面板查询主机 IP 的国家、地区、经纬度等地理字段；不返回国内访问成功率或被墙结论。 | [server/hostGeo.ts:204](</Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/hostGeo.ts:204>) |
| 随仓 ip2region IPv4/IPv6 离线库 | 将实际业务来源 IP 定位到国家/国内省份，用于运维来源统计；不是主动拨测。代码中的 `blocked` 地址表用于过滤内网/保留 IP。 | [server/operationsSourceGeo.ts:22](</Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/operationsSourceGeo.ts:22>)、[同文件:32](</Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/operationsSourceGeo.ts:32>) |
| 中国区域白名单插件的 metowolf/iplist、APNIC、ipverse 数据 | 国家、省份、ASN 的 IP 段，用于本机白名单防火墙；不是省份拨测结果。 | [plugins/china-region-whitelist/THIRD_PARTY_NOTICES.md:7](</Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/plugins/china-region-whitelist/THIRD_PARTY_NOTICES.md:7>) |

以上结论限于当前检出代码及随仓内容；管理员实际填写的健康目标、换 IP URL 和部署位置不能由这些默认代码确定。没有将出站失败推断为确认被墙，也没有将某个自有国内 Agent 的结果扩大为全国结果。

完整回报已保存至 [forwardx-china-reachability.md](/Users/macmini/Documents/Aixiaoji/nbpass/.scratch/research/forwardx-china-reachability.md)。下一步可按上述来源主机、目标和判定条件核对现有配置。
