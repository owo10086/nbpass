> 来源：Codex 调研会话 01a1b9aa-9960-7510-a57b-655257ca6f3a

核心结论：**ForwardX 有三类故障切换：目标地址切换、入口 DDNS 切换、隧道中继/出口切换。它们由不同组件执行，健康判定和恢复回切策略也不同。**参考重写时，应分别理解。

以下基于当前源码静态阅读；未修改任何文件，未运行项目、测试或构建。文中耗时均为代码参数，不是实测结果。

## 1. 项目概览

### 技术栈与组件

| 组件 | 实际实现 |
|---|---|
| 前端 | React 19、TypeScript、Vite、Tailwind CSS、Radix UI，使用 tRPC/React Query 访问面板 |
| 面板后端 | Node.js、TypeScript、Express、tRPC、Drizzle ORM |
| 数据库 | 支持 SQLite、MySQL、PostgreSQL |
| 节点管理 | Go Agent，负责配置执行、运行状态检查、探测、流量统计及上报 |
| 转发运行时 | iptables/nftables、realm、socat、GOST、Nginx，以及自写 Go 程序 `forwardx-fxp` |

依据：[package.json:94](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/package.json:94)、[drizzle/schema.ts:44](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/drizzle/schema.ts:44)、[agent/go.mod:1](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/agent/go.mod:1)。

整体上分为两条链路：

- **管理链路**：浏览器 → 面板 → Agent → 本机转发运行时。
- **业务链路**：业务客户端 → 入口转发运行时 → 可选中继/出口 → 最终目标。

面板提供管理 API；Agent 执行命令、管理服务并启动 FXP。依据：[server/index.ts:151](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/index.ts:151)、[agent/main.go:5151](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/agent/main.go:5151)。

### 面板与节点如何通信

**由 Agent 主动连接面板，主要使用 HTTP(S) 请求和 SSE 长连接。**

1. **注册和上报**：当前 Agent 将业务路径、请求数据、安装身份包装并加密，统一 POST 到 `/api/sync`；面板解密后分派到注册、心跳、流量、探测结果等接口。  
   依据：[agent/main.go:13721](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/agent/main.go:13721)、[server/agentRoutes.ts:276](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/agentRoutes.ts:276)。

2. **即时推送**：Agent 主动 GET `/api/stream` 建立 SSE。面板通过已有连接发送 `agent-refresh`、`agent-desired-state`、升级等事件；Agent 接收后唤醒同步或直接处理配置。  
   依据：[agent/main.go:4736](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/agent/main.go:4736)、[agent/main.go:4837](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/agent/main.go:4837)。

3. **鉴权和加密**：以管理员签发的 Agent Token 为基础；支持带签名的认证证明，当前有一次性 challenge-v2 认证，也保留旧版兼容路径。消息使用 AES-256-CTR 加密、HMAC-SHA256 校验，并有防重放处理；安装身份用于约束 Token 对应的 Agent 实例。  
   依据：[server/agentAuth.ts:81](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/agentAuth.ts:81)、[server/agentCrypto.ts:213](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/agentCrypto.ts:213)、[server/agentRoutes.ts:288](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/agentRoutes.ts:288)。

4. **配置下发**：面板生成“期望状态”，包含 `configRevision`、`configHash`、动作列表、运行规则和探测配置；通过 SSE 推送，同时保留心跳响应作为补偿。Agent 两条接收路径共用幂等机制。  
   依据：[server/agentHeartbeatRoute.ts:6622](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/agentHeartbeatRoute.ts:6622)、[agent/main.go:4929](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/agent/main.go:4929)。

5. **状态上报**：有规则状态及批量状态接口、流量接口、TCPing/隧道/组探测结果接口。新 Agent 还使用约 5 秒一次的轻量 presence；正常 SSE 连接下，全量心跳可以延长到 5 分钟。  
   依据：[server/agentStatusRoutes.ts:457](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/agentStatusRoutes.ts:457)、[server/agentReportRoutes.ts:679](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/agentReportRoutes.ts:679)、[agent/main.go:2873](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/agent/main.go:2873)。

## 2. 核心领域概念

### 数据模型

| 概念 | 表或结构 | 关键字段与含义 |
|---|---|---|
| 节点/主机 | `hosts` | `ip/ipv4/ipv6`、`entryIp`、`tunnelEntryIp`、`agentToken`、安装身份、在线状态、配置版本、流量限制 |
| 转发规则 | `forward_rules` | `hostId`、`forwardType`、`protocol`、`sourcePort`、`targetIp/targetPort`、`tunnelId`、限速/配额/启停状态 |
| 目标池 | 规则字段及 Go `failoverSpec` | 主目标在 `targetIp/targetPort`；备用目标放在 JSON 字段 `failoverTargets`；附带策略及失败/恢复窗口 |
| 转发组 | `forward_groups` | `groupMode`、`groupType`、`domain/recordType`、`activeMemberId`、`exitStrategy`、观察窗口、最后切换状态 |
| 转发组成员 | `forward_group_members` | `memberType=host/tunnel`、`hostId/tunnelId`、`priority`、关联规则、健康状态、失败及恢复起点 |
| 隧道 | `tunnels` | `entryHostId/exitHostId`、`entryGroupId/exitGroupId`、传输协议、端口、密钥/证书、`relayMode`、多出口策略 |
| 隧道拓扑 | `tunnel_hops`、`tunnel_exit_nodes` | 中继按 `seq` 排序；额外出口保存节点、监听端口及连接地址 |
| 规则出口分配 | `forward_rule_tunnel_exits` | 关联规则、隧道、出口节点以及该规则使用的出口端口 |

依据：[drizzle/schema.ts:239](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/drizzle/schema.ts:239)、[drizzle/schema.ts:391](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/drizzle/schema.ts:391)、[drizzle/schema.ts:454](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/drizzle/schema.ts:454)、[drizzle/schema.ts:509](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/drizzle/schema.ts:509)、[drizzle/schema.ts:546](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/drizzle/schema.ts:546)、[drizzle/schema.ts:606](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/drizzle/schema.ts:606)。

几个值得区分的概念：

- **入口、出口是节点在规则或隧道中的角色**；最终目标则是地址和端口，可以完全不属于受管节点。
- `host_groups` 是主机组织分组；`rule_groups` 是用户规则分类。故障切换主要使用 `forward_groups`。
- `forward_groups` 又有 `port/failover/chain/entry/exit` 五种模式，承担端口服务、入口主备、串联、多入口、出口候选池等不同职责。
- 组规则采用**逻辑模板 → 各成员实际子规则**的关系，通过 `forwardGroupRuleId/forwardGroupMemberId` 关联。

依据：[drizzle/schema.ts:345](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/drizzle/schema.ts:345)、[drizzle/schema.ts:376](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/drizzle/schema.ts:376)、[server/repositories/forwardGroupRepository.ts:2939](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/repositories/forwardGroupRepository.ts:2939)。

### 转发如何实现

它同时使用多种转发方式：

- iptables/nftables：面板生成内核 DNAT 等规则。
- realm、socat、GOST、Nginx：生成配置或服务定义，交给节点执行。
- `forwardx-runtime`：基于 GOST 源码构建，加入本项目的选择器补丁。
- `forwardx-fxp`：项目内自写 Go 转发程序，承担入口、中继、出口的数据转发。ForwardX 隧道还有 WireGuard V2、HTTP/WS/TLS 外层传输的 V3 分支。

依据：[shared/forwardTypes.ts:1](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/shared/forwardTypes.ts:1)、[server/agentActionCommands.ts:459](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/agentActionCommands.ts:459)、[scripts/build-runtime.sh:30](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/scripts/build-runtime.sh:30)、[server/forwardXTransport.ts:110](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/forwardXTransport.ts:110)。

## 3. 故障切换

### 3.1 切换对象

| 机制 | 切换对象 | 执行位置 |
|---|---|---|
| 单规则目标池 | 最终目标的 IP/域名和端口 | Agent 本地 TCP 代理 |
| `failover` 转发组 | 某台主机或某条隧道代表的入口成员 | 面板选择成员、更新 DDNS |
| `entry` 入口组 | DNS 中发布的入口地址集合 | 面板增删解析记录 |
| 隧道多中继/多出口 | 下一跳中继或出口端点 | 节点上的 GOST、FXP、Nginx |

因此，**切换目标地址、切换出口、切换用户连接的入口地址，是三件不同的事。**

### 3.2 单规则目标池：Agent 本地切换

**适用范围**

正常 API 校验限定为 **GOST + TCP**；支持的隧道类型为 `tls/wss/tcp/mtls/mwss/mtcp`。普通端口直转还有管理员权限限制。最多 10 个备用目标，加主目标共 11 个。

默认值为：

- `strategy=fallback`
- `failoverSeconds=60`
- `recoverSeconds=120`
- `autoFailback=true`

API 的观察窗口范围为 10～3600 秒。依据：[server/routers/rules.crud.ts:45](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/routers/rules.crud.ts:45)、[server/routers/rules.crud.ts:118](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/routers/rules.crud.ts:118)、[server/routers/rules.crud.ts:355](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/routers/rules.crud.ts:355)。

**工作方式**

面板将主目标、备用目标及策略下发给 Agent，同时让转发运行时连接本机内部代理端口。直接 GOST 转发的代理在本机；受支持的 GOST 隧道目标代理部署在出口节点。

依据：[server/agentHeartbeatRoute.ts:2788](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/agentHeartbeatRoute.ts:2788)、[server/agentHeartbeatRoute.ts:2831](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/agentHeartbeatRoute.ts:2831)。

**健康检查有两条路径：**

| 路径 | 实际行为 |
|---|---|
| 主动探测 | 每 5 秒触发一轮，逐个目标串行 TCP connect，每个目标超时 2 秒 |
| 主动探测失败 | 持续失败达到失败窗口后摘除，默认 60 秒 |
| 主动探测恢复 | 持续成功达到恢复窗口后重新标为健康，默认 120 秒 |
| 真实业务拨号 | 单目标拨号超时 10 秒；**一次拨号失败就立即摘除，并继续尝试其他未试目标** |

反向探测结果会清除相应持续计时。这里主要使用持续时间阈值。

尤其要注意：**真实业务拨号失败会绕过 60 秒观察窗口。**

依据：[agent/main.go:12128](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/agent/main.go:12128)、[agent/main.go:12145](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/agent/main.go:12145)、[agent/main.go:12229](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/agent/main.go:12229)。

**选择和回切**

- `fallback`：按目标列表顺序确定优先级，当前目标健康时继续使用；开启自动回切后，更靠前目标恢复可抢回。
- `round_robin`：健康目标间轮询。
- `random`：健康目标间随机。
- `ip_hash`：按本地代理看到的来源 IP 散列。
- 全部目标不健康时，仍会尝试故障目标。

依据：[agent/main.go:12053](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/agent/main.go:12053)、[agent/main.go:12103](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/agent/main.go:12103)。

**如何生效、连接影响**

切换只改变 Agent 本地的目标选择，不需要面板再次下发。尚未建立上游的新连接可以依次尝试不同目标；一旦上游建立，后续就是双向 `io.Copy`，没有中途迁移已有 TCP 会话的实现。

依据：[agent/main.go:12229](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/agent/main.go:12229)。

### 3.3 转发组：面板切入口 DDNS

**切换对象**

`failover` 组成员可以是主机或隧道。隧道成员取其 `entryHostId` 对应的入口地址。面板选择成员后，把组域名解析更新到这个地址。

依据：[server/repositories/forwardGroupRepository.ts:1836](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/repositories/forwardGroupRepository.ts:1836)。

**健康判定由 Agent 与面板共同完成**

有业务规则参与时，面板综合以下条件：

- 成员入口 Agent 是否仍有活性。
- 对应子规则是否启用、是否运行。
- 规则探测结果是否健康、是否过期。
- 可选“国内健康度”结果。

成员对应的任一启用子规则异常，都可能使整个成员不健康。依据：[server/repositories/forwardGroupRepository.ts:3960](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/repositories/forwardGroupRepository.ts:3960)。

具体探测如下：

| 检查项 | 实际实现 |
|---|---|
| 普通规则目标 | 节点向目标 TCP 端口探测；UDP-only 规则使用 ping |
| TCP 采样 | 每轮并发连接 3 次，共享 2 秒期限，**至少 1 次成功就算可达** |
| 隧道规则 | 出口节点探测最终目标；面板结合隧道探测结果 |
| “国内健康度” | 成员入口节点主动连接指定国内目标，默认 `www.189.cn:80` |
| 入口组关闭国内健康度 | 下发 `method=self`，Agent 直接报告成功 |

最后两项需要特别留意：**“国内健康度”检查的是节点向国内的出站可达性；`self` 也没有实际拨打入口业务端口。**

依据：[agent/metrics.go:1613](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/agent/metrics.go:1613)、[agent/metrics.go:2038](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/agent/metrics.go:2038)、[server/agentReportRoutes.ts:1563](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/agentReportRoutes.ts:1563)、[server/repositories/forwardGroupRepository.ts:1591](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/repositories/forwardGroupRepository.ts:1591)、[agent/metrics.go:1929](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/agent/metrics.go:1929)。

**检查间隔、阈值与防抖**

- Agent 使用稳定状态、待确认状态和待确认起点，默认连续失败 60 秒、连续恢复 120 秒；中间出现反向样本会重新计时。初始首次成功可以直接进入健康。
- 组健康探测间隔上限为 `clamp(min(失败窗口, 恢复窗口)/2, 5秒, 30秒)`；实际还受调度、探测时长和队列影响。
- 状态变化触发上报；稳定组健康结果每分钟补报。
- 面板识别 Agent 已经完成观察窗口的结果，避免再等一遍相同窗口。
- 健康数据新鲜度为 5 分钟；重连后要求新样本，避免沿用离线前的健康结果。

依据：[agent/forward_group_health.go:40](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/agent/forward_group_health.go:40)、[agent/main.go:3282](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/agent/main.go:3282)、[agent/tcping_report_gate.go:291](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/agent/tcping_report_gate.go:291)、[server/repositories/forwardGroupRepository.ts:1878](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/repositories/forwardGroupRepository.ts:1878)、[server/forwardGroupHealthRecheck.ts:9](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/forwardGroupHealthRecheck.ts:9)。

Agent 活性还有独立门槛：支持 presence 的节点连续沉默 **90 秒**才确认离线，面板重启也有 90 秒宽限；旧版兼容窗口为 150 秒。组失败起点从最后活性时间计算，因此不能机械地把默认 90 秒和 60 秒相加。

依据：[server/agentFastLiveness.ts:4](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/agentFastLiveness.ts:4)、[server/repositories/forwardGroupRepository.ts:417](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/repositories/forwardGroupRepository.ts:417)。

**切换策略**

- `priority` 数值越小，优先级越高。
- 当前成员失败且满足观察窗口后，选择优先级最高的健康成员。
- 开启 `autoFailback` 后，更高优先级成员稳定恢复才回切。
- 未配置组域名时，仅更新健康状态。
- 系统 DDNS 未启用时，只记录“建议入口”，不会实际更新解析。

依据：[server/repositories/forwardGroupRepository.ts:4837](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/repositories/forwardGroupRepository.ts:4837)、[server/repositories/forwardGroupRepository.ts:4854](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/repositories/forwardGroupRepository.ts:4854)、[server/repositories/forwardGroupRepository.ts:4656](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/repositories/forwardGroupRepository.ts:4656)。

**动作如何生效**

面板更新 DNS 服务商记录，然后保存 `activeMemberId`、解析值、切换时间和事件。健康事件触发的评估使用 `skipRuleSync=true`；这条入口切换路径不负责停止旧成员规则或迁移会话。

正常通过健康报告和活性变化触发，另有 250 毫秒事件合并、按组互斥、失败退避和窗口到期重评；5 分钟扫描主要用于补偿遗漏事件或面板重启。

依据：[server/repositories/forwardGroupRepository.ts:4682](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/repositories/forwardGroupRepository.ts:4682)、[server/repositories/forwardGroupRepository.ts:288](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/repositories/forwardGroupRepository.ts:288)、[server/scheduler.ts:729](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/scheduler.ts:729)。

**全员故障的处理**

普通故障下，会保留最后的保底解析；流量超限成员属于特殊情况，不允许继续作为保底，必要时可以清空记录。

依据：[server/repositories/forwardGroupRepository.ts:4192](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/repositories/forwardGroupRepository.ts:4192)。

### 3.4 入口组、隧道中继和出口切换

**入口组 `entry`**

它维护健康入口地址集合：A/AAAA 可以发布多个地址，故障观察期内保留旧地址，恢复稳定后重新加入；CNAME 分支选择首个适用成员。

这属于 DNS 地址集合维护，代码没有按每条业务连接执行轮询。

依据：[server/repositories/forwardGroupRepository.ts:4313](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/repositories/forwardGroupRepository.ts:4313)。

**隧道中继**

`relayMode=chain` 按顺序串联中继；`relayMode=failover` 把中间跳作为候选，例如：

`入口 → {中继 A / 中继 B} → 出口`

该模式要求至少两个中间候选，支持 ForwardX 和 GOST 系列隧道。依据：[shared/tunnelRelay.ts:11](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/shared/tunnelRelay.ts:11)、[server/agentHeartbeatRoute.ts:3725](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/agentHeartbeatRoute.ts:3725)。

**出口组**

出口组提供候选节点，真正选择发生在隧道运行时；该组评估分支只维护候选和展示状态。可选主备、轮询、随机、来源 IP 哈希等策略。

依据：[server/repositories/forwardGroupRepository.ts:4615](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/repositories/forwardGroupRepository.ts:4615)、[shared/exitStrategy.ts:1](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/shared/exitStrategy.ts:1)。

不同运行时的行为还要分别看：

| 运行时 | 源码中的故障与恢复行为 |
|---|---|
| GOST 主备选择器 | 被动记录拨号失败；存在未失败备选时持续使用备选，**不会仅因排除时间到期就主动回切主节点**；全部失败时才重新尝试故障候选 |
| FXP 端点选择器 | 拨号/建立失败后标故障，5 秒后重新获得选择资格；`fallback` 按顺序可能再次尝试原主，成功后恢复健康 |
| Nginx 多出口 | 生成 `max_fails=2 fail_timeout=10s`；主备模式将后续端点标成 `backup`，由 Nginx 执行 |

GOST 补丁还专门保证：全部故障时仍返回真实候选，避免上游把空选择解释为“绕过这一跳”。

依据：[scripts/runtime-patches/selector/forwardx_failover.go:13](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/scripts/runtime-patches/selector/forwardx_failover.go:13)、[forwardx-fxp/main.go:267](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/forwardx-fxp/main.go:267)、[forwardx-fxp/main.go:339](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/forwardx-fxp/main.go:339)、[server/agentHeartbeatRoute.ts:4012](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/agentHeartbeatRoute.ts:4012)。

### 3.5 切换耗时和已有连接

| 机制 | 能从代码确认的时间因素 | 已有 TCP 连接 |
|---|---|---|
| 本地目标池 | 5 秒探测调度、串行检查、失败窗口；业务单目标拨号最多等待 10 秒后换候选 | 保持原上游连接，没有会话迁移 |
| 入口 DDNS | 健康窗口、Agent 活性门槛、DNS API；TTL 默认 600 秒，配置解析最低 60 秒 | DNS 更新不会迁移已建立连接 |
| GOST/FXP 多候选 | 两者有多候选拨号 3 秒参数及候选重试；握手等还有其他时间开销 | FXP 明确只重试建立阶段，数据传输后失败终止流，不重放业务字节 |

依据：[server/ddns.ts:72](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/ddns.ts:72)、[server/agentHeartbeatRoute.ts:3671](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/agentHeartbeatRoute.ts:3671)、[forwardx-fxp/main.go:393](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/forwardx-fxp/main.go:393)、[forwardx-fxp/main.go:681](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/forwardx-fxp/main.go:681)。

**推测／工程含义：**DDNS 的用户侧恢复还取决于递归 DNS 缓存、客户端重新解析和重连。因此，“观察窗口 60 秒”不能等同于“业务 60 秒内恢复”。

### 3.6 通知、日志和切换历史

- **组级切换有数据库历史**：写入 `forward_group_events`，同时写面板日志；记录成员、事件类型、消息和时间，部分重复 DDNS 事件会去重。
- **组级有可选通知**：通过统一通知层发送给管理员，可使用 Telegram 或 Discord，包含切换前后地址、原因和时间；入口集合剔除、恢复也有通知。
- **本地目标池有 Agent 日志**：包括摘除、恢复、切换。沿该代理实现未找到专门的中心数据库切换历史或专用切换通知。
- FXP 会记录端点故障、恢复日志，Agent 还会解析这些日志形成端点事件状态。

依据：[server/repositories/forwardGroupRepository.ts:1745](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/repositories/forwardGroupRepository.ts:1745)、[server/forwardGroupSwitchNotifier.ts:60](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/forwardGroupSwitchNotifier.ts:60)、[server/notifications.ts:26](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/notifications.ts:26)、[agent/main.go:12093](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/agent/main.go:12093)、[agent/main.go:193](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/agent/main.go:193)。

## 4. 其他值得注意的功能

- **流量统计**：主机、用户、规则累计计数和历史统计；逻辑规则计数可跨生成子规则延续。依据：[drizzle/schema.ts:678](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/drizzle/schema.ts:678)、[server/ruleLimits.ts:23](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/ruleLimits.ts:23)。
- **多级限速和访问限制**：用户、隧道、组、规则限速，以及最大连接数、来源 IP 数限制。依据：[server/agentHeartbeatRoute.ts:2636](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/server/agentHeartbeatRoute.ts:2636)。
- **用户和配额**：管理员/普通用户、规则数、端口数、允许的转发方式、流量额度、到期时间、余额和订阅体系。依据：[drizzle/schema.ts:151](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/drizzle/schema.ts:151)。
- **流量触发故障转移及关机**：主机达到设定流量阈值后可以退出组调度，另有独立关机设置。依据：[drizzle/schema.ts:294](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/drizzle/schema.ts:294)。
- **运维能力**：链路测试、Looking Glass、iperf3、插件任务、Agent 升级及配置恢复。依据：[agent/main.go:840](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/agent/main.go:840)。

## 5. 优点、弱点和可改进之处

### 优点

1. **本地目标和路径切换可以独立于面板执行**，减少故障时对管理链路的依赖。
2. **主动检查与真实拨号失败结合**，既能提前发现问题，也能为新连接尝试备用目标。
3. **组级状态处理较细**：有未知状态、失败/恢复窗口、结果过期、重连后旧样本隔离，以及异步提交前后的活性复核。
4. **保留必要路由约束**：GOST 全故障时仍保留必经跳，FXP 明确限制重试阶段，避免错误绕路或重放业务数据。

### 弱点

1. **同名策略的语义不一致**：目标池能按恢复窗口自动回切，GOST 主备补丁保持健康备选，FXP 又允许 5 秒后重试旧主。
2. **健康检查覆盖有限**：TCP 连接成功不能证明应用正常；3 次仅成功 1 次仍可判健康；`self` 和向国内拨号都不能证明外部用户能访问入口。
3. **DDNS 依赖面板及解析收敛**，适合入口容灾，但无法仅靠服务器端参数保证恢复时间。
4. **故障可能扩大到组成员级别**：一个启用子规则异常可能影响整个成员，多个业务之间存在健康状态耦合。
5. **本地健康记忆会重置**：同监听配置更新会重置活动目标和目标健康状态；持久化只保存配置，未保存活动目标及健康计时。重启或编辑后会重新尝试原主。依据：[agent/main.go:11901](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/agent/main.go:11901)、[agent/runtime_persistence.go:492](/Users/macmini/Documents/Aixiaoji/源码参考/Forwardx/agent/runtime_persistence.go:492)。
6. **探测和重试可能累计等待**：本地目标健康检查串行执行，真实拨号也逐个尝试；目标越多，总等待可能越长。

### 面向新项目的重写建议

以下是设计建议，不是现有实现：

- **明确分开三种故障范围**：目标池、隧道路径、入口发布。分别定义健康来源、决策者和切换动作。
- **统一健康状态机及回切定义**：例如健康、待确认故障、故障、恢复观察；明确业务拨号失败是否允许绕过观察窗。
- **补充真实业务与外部视角**：按需加入 HTTP/协议握手检查、外部入口探测，并区分节点故障和某个业务目标故障。
- **给检查和重试设总预算**：限制并发、每轮总超时及单连接尝试时间，避免备用越多等待越长。
- **统一记录切换事件**：节点上报决策前后、原因、配置版本和健康证据；面板保存历史，同时明确已有连接的保留、排空和断开策略。

下一步：先确定新项目需要覆盖哪几类切换，再讨论对应的数据模型与状态机。