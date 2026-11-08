# nekopass：出口负载均衡、失联撤销与多目标调研

调研日期：2026-11-08。源码快照：`26a8dd847a10a9cf3582b9e235dcaae4502c5388`，调研开始时 nekopass 工作区无未提交改动。

范围：只做本地静态阅读，未执行网络请求、未运行 nekopass 程序或测试、未修改 nekopass。本文的“源码确认”表示当前实现直接可见；“推测”表示依据控制流推导的时序或部署结果，不代表实测保证。配置示例均为虚构，真实凭据如需出现统一写成 `***`。

## 问题 1：出口选择与最小连接数

### 1.1 支持哪些策略

**源码确认：**规则可以直连目标、指定单个出口，或者指定一个出口组；单出口与出口组不能同时指定。出口组支持以下三种策略，默认是 `least_connections`。证据：[规则字段与互斥校验](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/control/rules.go:22)、[出口组默认值](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/control/nodes.go:512)。

| 出口组策略 | 实际逻辑 | 源码 |
| --- | --- | --- |
| `least_connections` | 加权最少连接，比较 `active / weight`，实现通过交叉乘法避免浮点计算。 | [比较与计数](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/egress_group.go:215) |
| `round_robin` | 平滑加权轮询：候选累计自身权重，选累计值最大者，再减去总权重。 | [轮询实现](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/egress_group.go:197) |
| `random` | 按权重区间随机选择。 | [随机实现](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/egress_group.go:206) |

**源码确认：**目标地址另有独立的 `balance` 策略，支持随机、轮询、最少连接；这与出口组策略是两层选择，详见问题 4。[目标策略校验](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/control/rules.go:127)

### 1.2 连接数由谁统计、统计什么

- **源码确认：入口本地统计。**每个 Agent 的 Engine 创建自己的 `egressManager`，后者在内存的 `egressHealth.active` 中计数。面板接收的 `active_connections` 是节点整体上报值，出口组选择函数不使用它，也不读取出口上报的全局负载。[管理器创建](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/engine.go:139)、[本地状态](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/egress_group.go:33)、[节点上报](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/engine.go:550)
- **源码确认：统计一次业务 TCP 连接或 UDP 会话对出口的占用，包含正在拨号的请求。**`choose` 在实际拨号前加一；失败立即释放，成功把释放函数交给业务连接/会话，在其结束时减一。因此复用同一条 H2/H3/Mux 物理隧道的多个业务流仍分别计数，健康探测直接调用 `dialEgress`，不经过该加一逻辑。[预占与失败释放](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/egress_group.go:232)、[业务拨号路径](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/egress_group.go:288)、[TCP 释放](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/engine.go:382)、[UDP 释放](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/udp.go:284)、[探测路径](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/egress_group.go:383)
- **源码确认：计数并非严格按规则或组隔离。**相同 `peerKey` 的出口计数可被同一入口上的多个规则、多个组共享；key 包含出口 ID、链路 token 和传输配置。轮询进度、失败/恢复阈值则按“出口组 ID / 业务协议”维护。不同入口之间不共享这些计数。[peerKey](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/egress_group.go:29)、[共享 peers 与独立 schedules](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/egress_group.go:57)、[scheduleKey](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/egress_group.go:143)、[传输 key](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/h2_client.go:76)

### 1.3 同数、故障、恢复与后备

- **源码确认：最少连接的同数处理是“加权随机初选，再用严格更小的归一化连接数替换”。**全部候选的 `active / weight` 相同时保留随机初选；不能概括为对最终所有并列最小者再次严格按权重抽签：初选若不是最小者，后续扫描顺序也会影响结果。轮询累计值相同时保留先遇到的候选。[选择循环](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/egress_group.go:192)
- **源码确认：入口主动做认证隧道探测。**默认间隔 5 秒，连续失败 3 次屏蔽，连续成功 2 次恢复；间隔可设 2–60 秒，失败/成功阈值各为 1–20。探测使用特殊健康目标，要求有效链路凭据和规则授权，但不拨打业务目标。业务拨号中的出口连接失败也可计入健康结果；业务目标失败、Mux 容量/排空错误以及请求整体取消不会按同样方式污染共享出口健康。[默认及范围](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/control/egress_groups.go:30)、[数据库默认值](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/store/migration029.sql:3)、[结果分类](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/egress_group.go:253)、[业务反馈条件](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/egress_group.go:299)、[健康请求授权](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/tunnel.go:38)
- **源码确认：只有组内一层主用/备用，没有递归后备组。**成员职责是 `active`、`standby`、`disabled`。优先选主用；当本次请求已无未尝试且健康的主用时才选备用，因此请求内重试可以先于“累计失败阈值”使用备用。每个 peer 在一次拨号中最多尝试一次；没有健康候选就报错，不自动退回直连。[成员职责](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/control/egress_groups.go:45)、[主备筛选](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/egress_group.go:169)、[有界重试](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/egress_group.go:288)
- **源码确认：该模型限制为入口选一个出口，出口直接拨目标。**链路表禁止入口等于出口，候选投影也排除入口自身；没有组引用组的字段或递归选择路径。因此不需要后备组递归深度限制；但这不能等同于检测任意人工配置的网络转发环路，所读路径没有通用跳数/路径环检测。[链路约束](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/store/migration008.sql:9)、[候选投影](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/store/migration032.sql:3)、[出口直接拨号](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/tunnel.go:403)
- **源码确认：调度恢复针对新业务会话，不迁移既有 TCP 连接/UDP 会话。**组成员和策略更新可保留入口监听器；但移除授权、轮换凭据或传输池清理仍可能关闭原会话，不能把“调度不迁移”理解为“任何组更新都保活”。另有 `FallbackUrl` 用于隧道伪装网站回落，并非后备出口组。[运行规则比较](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/egress_group.go:412)、[Mux 池移除](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/raw_mux.go:225)、[伪装 Fallback](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/h2_camouflage.go:43)

### 1.4 与面板失联时

**源码确认：**选择策略继续运行，依据缓存下来的规则、成员、权重、凭据，以及入口本地实时计数和入口到出口的探测结果。控制连接断开不会取消独立的出口管理器；重启时加载配置后重新创建计数和健康状态，不从磁盘恢复旧的活跃连接数。[失联保留配置](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/client.go:23)、[管理器独立生命周期](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/egress_group.go:69)、[配置保存内容](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/state.go:76)

## 问题 2：失联行为与权限撤销

### 2.1 缓存、重启与离线授权期限

- **源码确认：失联后继续按已应用配置转发，重启也先加载缓存再连接面板。**缓存为 bbolt 数据库，默认路径 `/var/lib/nekopass-agent/state.db`，可由 `-state` 指定；完整控制配置以 protobuf 存入 `meta/config`，用量及额度另行持久化。控制失败后保留配置并约每 2 秒重连。[默认路径](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/cmd/nekopass-agent/main.go:28)、[配置读写](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/state.go:65)、[启动及重连](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/client.go:23)
- **源码确认：没有统一的“失联 N 秒即授权过期”租约。**用户策略和出口规则有 `expires_unix`，面板取用户到期时间与下一重置时间的较早值，节点用本机时钟检查；值为 0 时没有此时间上限。有限额的计费转发还受已发放可用额度限制，离线无法获得新增额度；无限额策略没有该总量限制，零倍率转发也不消耗计费额度。这些约束与控制连接失联时长无关。[策略到期时间](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/control/stream.go:306)、[出口到期时间](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/control/stream.go:402)、[本地有效性](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/account.go:78)、[额度消耗与零倍率](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/metering.go:51)

### 2.2 各种撤销如何生效

**源码确认：**正常在线同步是“Agent 报告 → 面板返回完整配置 → Agent 应用”，循环中通常等待 1 秒；不是面板在管理操作提交后立即主动推送。以下“在线生效”均以相关节点成功收到并应用新配置为前提。[控制循环](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/client.go:66)、[服务端交换](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/control/stream.go:61)

| 操作 | 在线节点 | 离线节点 | 源码依据 |
| --- | --- | --- | --- |
| 删除/禁用规则 | 入口移除监听器并取消该规则上下文，已有业务连接随之关闭；出口的新授权快照不再包含该规则，新流被拒绝，已有流由授权复查关闭。 | 本地不会自动知道该变更，仍可能按旧规则转发；若链路另一端在线且已撤销，可由另一端阻断。双方均离线时只能依赖旧策略的到期、额度及连接自身结束。 | [规则修改](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/control/rules.go:500)、[入口取消](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/engine.go:248)、[取消后关连接](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/engine.go:438)、[出口授权生成](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/control/stream.go:402) |
| 停用/删除节点 | 正常删除要求先停用、迁移或删除相关规则；仍在线时必须先确认停用配置已同步。停用配置使本节点入口和出口停止服务；删除后旧控制凭据查不到节点。 | 删除接口允许离线节点不等待同步，因此删除数据库记录不保证旧 Agent 已停止；磁盘缓存不会被远程清除。其他在线端可应用相关授权消失后的新快照。 | [删除前置条件](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/control/nodes.go:415)、[入口停用条件](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/engine.go:248)、[出口停止](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/tunnel.go:207)、[链路级联删除](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/store/migration008.sql:9) |
| 重置节点 token | 已建立控制流也会在下一次交换重新核对 token 哈希，发现变更后失败；但 Agent 随即进入保留配置的重连循环。单纯重置节点登录 token 不会自动轮换入口—出口链路 token，也不保证关闭数据连接。 | 使用旧 token 无法重新认证面板，但仍持有旧缓存及独立的隧道凭据，可继续满足旧授权的转发。 | [逐轮校验](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/control/stream.go:104)、[token 变更处理](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/control/nodes.go:333)、[认证查询](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/store/queries.sql:20)、[断线保留](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/client.go:54) |
| 轮换入口—出口隧道凭据 | 当前代码在出口隧道协议改变时轮换相应链路 token。入口获得新 token 后使用它；出口应用后拒绝旧 token，已有业务流也会复查并关闭。两端非原子同步，可能出现短时不一致。 | 离线出口继续接受符合旧快照的旧凭据；只有入口更新而出口未更新，通常表现为新凭据认证失败。只有出口更新则可阻断仍用旧凭据的入口；两端均未更新时旧凭据继续有效。 | [轮换触发点](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/control/nodes.go:359)、[向入口/出口下发](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/control/stream.go:327)、[出口持续检查](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/tunnel.go:411) |

表中离线双方状态组合属于**推测**，由“缓存继续运行”和“出口使用当前本地授权”共同推导；没有执行断网实验。[缓存行为](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/client.go:54)、[本地授权校验](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/tunnel.go:57)

### 2.3 出口怎样认证，旧隧道还能否开新流

- **源码确认：节点登录 token 与隧道凭据是两套东西。**隧道链路凭据作用域为一对 `(ingress_node_id, egress_node_id)`，由 32 个随机字节编码成 64 位十六进制字符串；同一对节点的多条规则可共享链路凭据，再叠加逐规则授权。出口的 `EgressRule` 约束规则、入口、用户、额度 epoch、到期时间和目标列表。[链路主键](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/store/migration008.sql:9)、[凭据生成](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/control/server.go:80)、[出口授权结构](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/api/control.proto:114)
- **源码确认：凭据形式随传输不同。**普通 TCP 隧道请求头直接携带链路 token、入口 ID、规则 ID、目标；其用户/epoch 从出口规则授权取得。H2/H3 使用链路密钥生成 HMAC 证明，绑定连接材料和包含目标、用户、epoch、随机数/序列号的请求元数据，出口逐请求校验。随机加密传输还有外层加密握手，并查询当前链路密钥；不能把所有协议概括为直接发送同一种 Bearer token。[TCP 请求头](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/tunnel.go:65)、[TCP 授权身份](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/tunnel.go:379)、[H2 元数据与 HMAC](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/h2_client.go:28)、[H2 校验](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/h2_server.go:158)、[H3 校验](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/h3_server.go:189)、[随机加密握手](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/random_tunnel.go:33)
- **源码确认：出口逐业务流校验目标，旧物理会话不构成永久授权。**目标必须精确匹配本地授权的 `host:port` 字符串；H2/H3/Mux 还检查业务协议。H2/H3 每个新流重新读取当前 policy；Mux 在旧 carrier 上新开流也重新校验当前链路密钥与规则。因而出口已应用撤销后，旧会话不能继续为已撤销规则或旧凭据成功开启业务流；仍获授权的其他规则可以继续使用共享物理连接。[目标匹配](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/tunnel.go:38)、[协议检查](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/h2_server.go:305)、[Mux 逐流校验](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/raw_mux.go:589)、[H3 当前 policy](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/h3_server.go:183)
- **源码确认：已有业务流会主动复查授权。**普通 TCP、H2、H3、Mux 的业务中继通常每秒复查一次；UDP 中继在数据包和每秒定时器上复查。目标拨号结束后还可再次检查，减少“拨号期间授权已撤销”的竞态。物理会话是否整体关闭因协议而异：Raw Mux 在链路密钥变化时有每秒关闭 carrier 的检查，H2 则可保留承载其他有效流的连接。[TCP 复查](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/tunnel.go:411)、[H2 二次校验与复查](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/h2_server.go:219)、[H3 复查](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/h3_server.go:300)、[Mux carrier 复查](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/raw_mux.go:540)、[UDP 复查](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/udp_relay.go:13)

**推测：**“已应用后约下一次 1 秒检查关闭”是正常调度下的工程预期，不能当成从管理员点击撤销起算的 1 秒硬上限；还要加控制交换、配置应用和线程调度时间，离线端没有固定传播上限。[控制等待](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/client.go:98)、[每秒复查](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/tunnel.go:414)

### 2.4 界面能否看到已生效

**源码确认：**有节点级 `config_revision`、`applied_revision`、`sync_error`。Agent 只有在本轮配置没有汇总错误时才推进已应用 revision，并在下一次报告中上报；管理界面显示“已同步 / 等待同步”或错误。规则列表也根据相关节点 revision、错误和在线情况显示状态。[revision 推进](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/engine.go:304)、[节点报告](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/engine.go:550)、[界面同步列](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/web/src/NodesPage.vue:61)、[规则状态计算](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/control/rules.go:368)

**源码确认：**这不是逐次撤销完成证明：没有“某条规则的全部旧流已经关完”的独立 ACK；出口组规则的状态计算也没有逐成员比较其已应用 revision。节点已删除后节点列表不再保留它的同步状态。因此“规则暂停”“节点已删除”不能直接读作所有离线副本已执行撤销。[报告字段](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/api/control.proto:18)、[组规则状态逻辑](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/control/rules.go:398)、[节点列表查询](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/control/nodes.go:62)

## 问题 3：离线判定与切换时长

### 3.1 面板怎样判定离线

| 项目 | 源码确认的行为 | 证据 |
| --- | --- | --- |
| 面板在线阈值 | 查询时判断 `last_seen > now() - 12 seconds`；超过窗口即不在线，没有额外等待离线定时任务。 | [节点在线表达式](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/control/nodes.go:62) |
| 心跳/报告节奏 | 每次成功报告、收到配置并应用后等待 1 秒；面板在交换事务中更新 `last_seen`。所以不是严格固定频率的独立心跳。 | [Agent 循环](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/client.go:73)、[last_seen 更新](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/control/stream.go:146) |
| Agent 控制连接超时 | 单轮发送/接收配置设 8 秒 watchdog；失败后通常等待 2 秒重连。 | [watchdog](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/client.go:78)、[重连等待](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/client.go:54) |
| 面板重启宽限 | 当前在线表达式没有面板启动时间或额外宽限参数；启动路径未见专门的离线判定宽限。重启后仍按数据库原有 `last_seen` 与当前时间比较。 | [在线表达式](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/control/nodes.go:62)、[服务启动](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/cmd/nekopass/main.go:99) |

### 3.2 哪些自动动作会发生

- **源码确认：出口组自动切换不等待面板的 12 秒离线判定。**候选数据库投影没有按 `last_seen` 过滤；入口根据自己的认证探测与拨号结果屏蔽出口、转用其他主用或备用。探测调度每 250 毫秒扫描，最多 8 个并发探测，单次设 2 秒超时；失败/成功阈值是次数，不是“离线持续 N 秒”。这提供了其他节点即入口对出口的直接证据，但没有面板组织多节点投票的机制。[候选投影](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/store/migration032.sql:3)、[健康计数](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/egress_group.go:253)、[探测调度](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/egress_group.go:354)
- **推测：不能承诺固定 15 秒切换。**默认 5 秒间隔、3 次失败，在没有排队、探测快速失败且没有业务成功重置计数的简化情况下，故障发生到屏蔽通常约 10–15 秒；若探测超时，还需考虑最后一次最多约 2 秒及调度开销。业务拨号可在当前请求内重试别的出口，实际转用备用又可能更早。共享 peer 使用最短探测间隔、并发排队和业务结果交错，都会改变时长。[共享间隔](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/egress_group.go:101)、[拨号重试](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/egress_group.go:288)、[调度和超时](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/egress_group.go:354)
- **源码确认：DDNS 是节点自行维护本节点地址，不是“某入口离线后把域名切到另一入口”。**Agent 独立发现自身 IP 并同步 DNS，默认检查间隔与 TTL 均为 300 秒，检查间隔允许 60–86400 秒。该路径不读取面板节点在线状态；在所查控制、DDNS 和路由路径中未见基于离线时长自动迁移入口或改写到其他节点的功能。[DDNS 循环](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/ddns.go:54)、[DDNS 默认及范围](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/ddns/config.go:35)、[入口切换为显式操作](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/control/rules.go:518)
- **源码确认：另有“自动换 IP”，依据本节点 ICMP 结果，不依据面板失联。**节点每 5 秒探测配置的公网 IPv4，默认连续失败 6 次触发换 IP，默认冷却 300 秒；失败阈值范围 1–720，冷却范围 30–86400 秒。探测程序出错会清空失败计数并报错，不能直接当成目标不可达；没有叠加其他节点探测投票。换 IP 成功会唤醒 DDNS/公网地址刷新。[默认与范围](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/iprecovery/config.go:13)、[触发与冷却](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/iprecovery/runner.go:132)、[刷新联动](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/ip_recovery.go:81)

### 3.3 恢复窗口与最短驻留

**源码确认：**出口组通过连续成功次数抑制抖动，默认 2 次成功恢复；失败重置成功计数，成功重置失败计数。恢复后的主用立即重新参与新请求选择，备用没有独立的最短驻留或延迟回切参数；这是次数窗口，不是固定秒数的稳定窗口。自动换 IP 的冷却以最近尝试/成功变更时间为基准并持久化，属于另一机制，也不表示恢复后回切旧 IP。[出口恢复逻辑](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/egress_group.go:263)、[主用优先](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/egress_group.go:185)、[换 IP 持久状态](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/iprecovery/runner.go:10)、[冷却判定](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/iprecovery/runner.go:160)

**推测：**仅按默认周期探测、成功快速返回时，恢复到重新参与选择约为 5–10 秒量级；业务成功反馈或共享 peer 的更短探测间隔可缩短它，不能把“连续成功 2 次”固定解释成 10 秒。[成功计数入口](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/egress_group.go:265)、[业务反馈](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/egress_group.go:305)

## 问题 4：一条规则的多个目标

### 4.1 数据结构与策略

**源码确认：**一条规则支持 1–32 个 `host:port` 目标，支持 IPv6 的 `[IPv6]:port` 格式。API 的 `RuleInput.Targets []string` 和协议的 `Rule.targets` 保存列表；旧的单目标字段仍用于兼容。目标策略默认随机，也可轮询或最少连接，没有目标权重或目标主用/备用字段。[输入与校验](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/control/rules.go:99)、[策略默认](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/control/rules.go:127)、[协议字段](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/api/control.proto:55)

**源码确认：**入口的每条规则维护 `targetActive[]` 与轮询位置。随机是等概率初选；轮询推进下标；最少连接从随机初选开始扫描更小计数。拨号前目标计数加一，失败或会话结束释放；这里同样包括拨号中的请求，同数时的准确实现也是“随机初选后严格比较”，不是另建并列集合再抽签。[目标运行状态](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/rule.go:18)、[目标选择和释放](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/rule.go:74)

### 4.2 入口与出口各做什么

- **源码确认：入口先选目标，再为该目标选出口。**TCP 的外层循环挑选一个目标，再调用出口组拨号；某个目标拨号失败会在剩余时间内尝试其他目标。出口组内部可重试其他出口，但明确的业务目标失败会直接返回外层，不用它把共享出口标坏。UDP 也复用入口的目标选择，并以客户端源地址/端口建立会话，同一会话固定使用建连时选定的目标与出口，不是逐数据包轮询。[TCP 两层选择](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/rule.go:112)、[目标错误处理](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/egress_group.go:305)、[UDP 会话标识](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/udp.go:35)、[UDP 目标选择](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/udp.go:229)
- **源码确认：没有独立的业务目标周期健康检查/熔断状态。**所读目标选择状态仅有活跃数、轮询位置和本次请求的失败排除集合；失败目标不会因此被跨请求持续屏蔽。周期探测针对隧道健康特殊目标，出口收到它直接响应，不验证真实目标是否可用。业务目标是否能连接由出口实际拨号得知，再反馈给入口重试；UDP 的 `Dial` 成功也不能证明目标应用会响应。[目标状态及重试](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/rule.go:18)、[特殊探测](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/egress_group.go:383)、[出口健康响应与业务拨号](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/tunnel.go:399)、[原生 UDP 拨目标](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/udp_native.go:352)
- **源码确认：出口按入口本次指定的目标拨号，不自行从列表另选目标。**出口持有整份授权目标列表用于校验，但正常中继路径只拨请求中的 `target` / `open.Target`；未在授权列表中的请求被拒绝。域名解析及最终 IP 的管理员 CIDR 策略检查也在实际拨号的出口执行。因此允许的是授权列表内的目标字符串，不代表固定绑定某个解析 IP。[字符串授权](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/tunnel.go:49)、[实际拨目标](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/h2_server.go:206)、[解析地址策略](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/target_policy.go:13)

### 4.3 一个具体配置流程

以下为**说明性示例**：入口节点 10，出口组 20 内有主用出口 21/22，目标填写 `app-a.example:443`、`app-b.example:443`；规则 `balance=round_robin`，出口组 `strategy=least_connections`。

1. **源码确认：**管理员启用出口、关联允许的入口，把出口加入组并配置职责、权重和健康阈值，再向用户授权相关节点组；候选投影同时要求出口启用、入口—出口关联存在、用户有相应访问权限。[关联入口](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/control/nodes.go:235)、[组成员配置](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/control/nodes.go:543)、[候选授权投影](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/store/migration032.sql:10)
2. **源码确认：**创建规则选择入口 10、出口组 20，在界面逐行填写两个目标，并在高级选项选择目标轮询。服务端保存 `targets`、`balance`、`egress_group_id`。[界面表单](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/web/src/RulesPage.vue:145)、[保存规则](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/control/rules.go:323)
3. **源码确认：**面板向入口下发目标列表、出口候选和组策略；向相关出口下发该规则允许的目标列表及授权身份。入口每个新连接先轮询目标，再按本地加权连接数选 21 或 22；出口验证当前授权后只拨这次指定的目标。[入口组配置组装](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/control/egress_groups.go:106)、[出口授权下发](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/control/stream.go:402)、[运行顺序](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/rule.go:121)

## 对 nbpass 的启示

以下是**设计建议**，不是 nekopass 已具备能力的声明。

### 对问题 1

- 第一版可以采用入口本地的“业务活跃数 + 正在拨号数”，按权重归一化；明确统计共享范围。若以后需要跨入口全局负载，再单独设计出口上报与数据过期处理。[参考本地预占实现](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/egress_group.go:232)
- 主备先限制为组内一层，并对每次请求设置候选去重和整体拨号预算；若增加后备组引用，再增加深度限制、配置环检测及运行时防环。[参考主备和重试](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/egress_group.go:169)

### 对问题 2

- 把“控制登录凭据”“入口—出口链路凭据”“逐规则转发授权”分开；同时明确离线租约到期时间，避免把重置节点 token 当成数据面已经撤销。[参考两层授权](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/tunnel.go:57)、[参考断线保留行为](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/client.go:54)
- 每个新流校验当前规则、目标、凭据版本，已有流支持授权变化后主动关闭；界面分别展示期望 revision、已应用 revision，以及入口和所有相关出口的撤销确认，离线者显示“待确认”。[参考逐流校验](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/raw_mux.go:611)、[参考同步展示](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/web/src/NodesPage.vue:61)

### 对问题 3

- 分开定义“面板控制失联”和“入口到出口不可用”；出口摘除优先使用实际隧道证据。涉及入口/DDNS 切换时，额外定义多点探测和面板重启宽限，防止面板故障引发全局误切。[参考两种独立判据](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/control/nodes.go:62)、[参考认证探测](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/egress_group.go:354)
- 将失败阈值、恢复窗口、最短驻留和延迟回切分别配置，并按“探测间隔 + 超时 + 并发排队 + 重试”估算切换预算，避免只公布一个固定切换秒数。[参考恢复计数](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/egress_group.go:263)

### 对问题 4

- 将目标策略与出口策略分别建模，并明确选择顺序；一条 TCP 连接/UDP 会话建立后固定目标和出口。[参考两层选择](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/rule.go:121)、[参考 UDP 会话](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/udp.go:284)
- 若增加目标健康检查，按“出口到目标”维护结果，由实际拨目标的出口探测并反馈入口；保留出口逐流目标白名单与解析后 IP 检查，分别处理出口故障和业务目标故障。[参考目标故障隔离](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/egress_group.go:317)、[参考出口拨号策略](/Users/macmini/Documents/Aixiaoji/源码参考/nekopass/internal/agent/target_policy.go:13)

下一步：据此讨论 nbpass 的统计口径、离线租约和撤销确认语义；本次仅交付调研报告，未实施方案。
