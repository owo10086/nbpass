> 来源：Codex 调研 ai-anti 的域名解析监控

报告文件：[dns-monitor.md](/Users/macmini/Documents/Aixiaoji/nbpass/.scratch/research/dns-monitor.md)。调研日期：2026-11-08。

本次仅静态阅读 ai-anti 的 Python 源码和 Python 测试，并查看目录、文件名；没有修改 ai-anti 文件，没有运行项目或测试，没有调用外部接口。没有读取 `.env`、JSON 配置、数据库、日志或其他非 Python 文件中的敏感信息。实际监控名单、启用的解析来源和运行配置未核实；文中公共 DNS 地址是源码示例或新项目建议，不代表线上配置。敏感凭据、账号统一以 `***` 表示。以下源码路径均属于 `/Users/macmini/Documents/Aixiaoji/ai-anti`。

**核心结论**

该功能准确地说是“域名 A 记录的 IPv4 地址集合变化观察器”：后端从本机向多个自选 DNS／DoH 来源查询，分别保存各来源上次成功的地址集合，记录变化、无 A／NXDOMAIN、采样故障。它没有业务期望 IP、自动跨来源一致性判断、GFW 污染识别、通知或 DNS 自动修复闭环。新项目最值得参考的是采样、状态和历史的组织方法；判断是否正确则应接入新项目已有的“入口组→活动入口”配置。依据：[backend/services/domain_resolver.py:105](/Users/macmini/Documents/Aixiaoji/ai-anti/backend/services/domain_resolver.py:105)、[backend/services/domain_observation_service.py:689](/Users/macmini/Documents/Aixiaoji/ai-anti/backend/services/domain_observation_service.py:689)。

**0. 项目概览**

这是围绕 V2Board 的经营分析、用户运维和订阅风控平台，域名观察是其中一个辅助模块。

| 部分 | 技术与职责 | 依据 |
|---|---|---|
| 后端 | Python、FastAPI；路由与业务服务分层 | [backend/main.py:49](/Users/macmini/Documents/Aixiaoji/ai-anti/backend/main.py:49)；[backend/routers/domain_observation.py:5](/Users/macmini/Documents/Aixiaoji/ai-anti/backend/routers/domain_observation.py:5) |
| 远端数据 | SQLAlchemy 管理 V2Board／日志库的 MySQL 连接，查询链路加只读保护 | [backend/database.py:4](/Users/macmini/Documents/Aixiaoji/ai-anti/backend/database.py:4)、[backend/database.py:43](/Users/macmini/Documents/Aixiaoji/ai-anti/backend/database.py:43) |
| 本地数据 | SQLite；订阅日志按月分库，域名观察使用独立数据库 | [backend/local_db.py:1](/Users/macmini/Documents/Aixiaoji/ai-anti/backend/local_db.py:1)；[backend/domain_observation_db.py:15](/Users/macmini/Documents/Aixiaoji/ai-anti/backend/domain_observation_db.py:15) |
| DNS 查询 | dnspython；DoH 经 `dns.query.https` 发 DNS 消息 | [backend/services/domain_resolver.py:22](/Users/macmini/Documents/Aixiaoji/ai-anti/backend/services/domain_resolver.py:22)、[backend/services/domain_resolver.py:94](/Users/macmini/Documents/Aixiaoji/ai-anti/backend/services/domain_resolver.py:94) |
| 前端 | 按用户提供的项目说明，为 Vue 3、Vite、Naive UI、ECharts；目录中有 `.vue` 页面。本次没有读取前端源码或依赖清单，未独立核实版本和组件使用情况 | 来源为本次提供的项目说明与文件名，非 Python 源码结论 |
| 后台任务 | 应用启动后开启同步、扫描、自动拉黑、工单提醒、域名观察线程 | [backend/main.py:42](/Users/macmini/Documents/Aixiaoji/ai-anti/backend/main.py:42)；[backend/scheduler.py:24](/Users/macmini/Documents/Aixiaoji/ai-anti/backend/scheduler.py:24) |

主要目录：`backend/routers/` 提供 API，`backend/services/` 实现业务，`backend/middleware/` 处理请求；`frontend/src/views/` 存放页面；`data/` 是源码指定的本地数据库目录；`docs/` 存放文档；`geoip/` 存放 IP 地理数据相关资源。本次没有读取文档、数据库或 GeoIP 资源内容。

主要功能包括经营仪表盘、用户／订单分析、访问与订阅日志、风险评估、同步管理、自动拉黑、用户生命周期、营销、工单、GoEdge 联动、内鬼风险工作台、域名观察。依据为已注册路由：[backend/main.py:101](/Users/macmini/Documents/Aixiaoji/ai-anti/backend/main.py:101)。

域名功能的实际调用链是：后台调度或管理 API → `DomainObservationService` → `domain_resolver` → DNS／DoH → 独立 SQLite → 总览／详情 API。对应入口：[backend/scheduler.py:145](/Users/macmini/Documents/Aixiaoji/ai-anti/backend/scheduler.py:145)、[backend/routers/domain_observation.py:12](/Users/macmini/Documents/Aixiaoji/ai-anti/backend/routers/domain_observation.py:12)、[backend/services/domain_observation_service.py:857](/Users/macmini/Documents/Aixiaoji/ai-anti/backend/services/domain_observation_service.py:857)。

**1. 监控对象与配置来源**

- **域名名单由管理员维护。** API 支持加入域名、设置单域名间隔、暂停／恢复。保存纯域名，不接受协议、端口和路径；支持小写、去尾点和 IDN／punycode 规范化，重复加入不会新建一条。没有看到从 V2Board、DNS 服务商或入口组自动导入域名的链路。依据：[backend/routers/domain_observation.py:86](/Users/macmini/Documents/Aixiaoji/ai-anti/backend/routers/domain_observation.py:86)、[backend/services/domain_observation_service.py:95](/Users/macmini/Documents/Aixiaoji/ai-anti/backend/services/domain_observation_service.py:95)、[backend/services/domain_observation_service.py:145](/Users/macmini/Documents/Aixiaoji/ai-anti/backend/services/domain_observation_service.py:145)、[backend/services/domain_observation_service.py:416](/Users/macmini/Documents/Aixiaoji/ai-anti/backend/services/domain_observation_service.py:416)。
- **主动查询固定为 A，只比较 IPv4 集合。** 没有 AAAA、CNAME、NS、MX、TXT 等类型的独立监控配置；也没有通配符展开。CNAME 可能出现在解析过程中，但不独立比较目标或链路。依据：[backend/services/domain_resolver.py:107](/Users/macmini/Documents/Aixiaoji/ai-anti/backend/services/domain_resolver.py:107)、[backend/services/domain_resolver.py:122](/Users/macmini/Documents/Aixiaoji/ai-anti/backend/services/domain_resolver.py:122)、[backend/services/domain_resolver.py:133](/Users/macmini/Documents/Aixiaoji/ai-anti/backend/services/domain_resolver.py:133)。
- **来源是全局列表。** 所有启用域名与所有启用来源配对采样，没有每域名专属来源组。来源可新增、停用／恢复、排序，不提供删除；排序不代表可信度、主从或权重。依据：[backend/services/domain_observation_service.py:485](/Users/macmini/Documents/Aixiaoji/ai-anti/backend/services/domain_observation_service.py:485)、[backend/services/domain_observation_service.py:538](/Users/macmini/Documents/Aixiaoji/ai-anti/backend/services/domain_observation_service.py:538)、[backend/services/domain_observation_service.py:794](/Users/macmini/Documents/Aixiaoji/ai-anti/backend/services/domain_observation_service.py:794)。
- **配置在 SQLite。** 域名名单、解析来源、全局间隔和并发数分别写入域名观察数据库相关表；这条链路不靠 JSON 规则文件或 `.env` 提供监控名单。没有“期望 IP／期望 CNAME”配置字段。依据：[backend/domain_observation_db.py:62](/Users/macmini/Documents/Aixiaoji/ai-anti/backend/domain_observation_db.py:62)、[backend/domain_observation_db.py:137](/Users/macmini/Documents/Aixiaoji/ai-anti/backend/domain_observation_db.py:137)。

没有读取运行数据库，因此不能列出实际正在监控的域名，也不能把脚本里的示例域名当作正式名单。

**2. 解析方法、探测位置与频率**

| 项目 | 已实现的方法与边界 |
|---|---|
| 传统 DNS | 接受裸 IPv4 解析服务器地址，使用 dnspython 默认传统 DNS 查询路径；存储注释标为 UDP/53。没有自定义端口、IPv6 解析器地址或专门的 TCP／DoT 选项。应用层未固定底层库所有传输行为。 |
| DoH | 接受完整 HTTPS 端点，用 RFC 8484 DNS 消息查询 A；不是第三方网站拨测，也不是浏览器请求。 |
| 公共解析器 | 没有硬编码自动启用的 114／223／119／8.8.8.8 来源集合。API 示例使用 `223.5.5.5`；测试还使用 `119.29.29.29` 和阿里 DoH。其他公共 DNS 可按来源格式加入，但不能据此认定当前部署已启用。 |
| 权威 NS | 没有 NS 自动发现、委派遍历、权威应答核验或权威专属模式。即便手工填入某个 NS 的 IPv4，也仍走普通来源查询逻辑。 |
| 探测位置 | DNS 请求由运行后端的服务器本机发出。多个 DNS 来源不等于多个地理探测点；没有节点探针、运营商／地区标识或第三方多地拨测派发。 |
| 单次超时 | 默认 5 秒，传统解析器同时设置 timeout 和 lifetime，DoH 也传入 timeout。API 没有暴露按来源调整超时。 |

依据：[backend/domain_observation_db.py:30](/Users/macmini/Documents/Aixiaoji/ai-anti/backend/domain_observation_db.py:30)、[backend/services/domain_observation_service.py:246](/Users/macmini/Documents/Aixiaoji/ai-anti/backend/services/domain_observation_service.py:246)、[backend/services/domain_resolver.py:33](/Users/macmini/Documents/Aixiaoji/ai-anti/backend/services/domain_resolver.py:33)、[backend/services/domain_resolver.py:81](/Users/macmini/Documents/Aixiaoji/ai-anti/backend/services/domain_resolver.py:81)、[backend/tests/test_domain_observation_service.py:706](/Users/macmini/Documents/Aixiaoji/ai-anti/backend/tests/test_domain_observation_service.py:706)、[backend/tests/test_domain_resolver.py:45](/Users/macmini/Documents/Aixiaoji/ai-anti/backend/tests/test_domain_resolver.py:45)、[backend/services/domain_observation_service.py:922](/Users/macmini/Documents/Aixiaoji/ai-anti/backend/services/domain_observation_service.py:922)。

**频率与调度规则：**

- 默认每 5 分钟；全局默认值可改，单域名可覆盖或跟随默认；范围为 1～1440 分钟。默认并发 4，可设为 1～64。依据：[backend/domain_observation_db.py:19](/Users/macmini/Documents/Aixiaoji/ai-anti/backend/domain_observation_db.py:19)、[backend/routers/domain_observation.py:39](/Users/macmini/Documents/Aixiaoji/ai-anti/backend/routers/domain_observation.py:39)。
- 后台启动等待 10 秒，此后每 30 秒认领一批到期项，循环异常后等 60 秒。30 秒是检查队列的周期，实际域名采样间隔仍受配置控制。依据：[backend/scheduler.py:159](/Users/macmini/Documents/Aixiaoji/ai-anti/backend/scheduler.py:159)。
- 每个“域名×来源”保存自己的到期时间和执行中标记；受剩余并发槽限制。本次完成后，下一次到期通常按“完成时间＋间隔”安排，还可能被取较晚值；因此不是严格固定时刻，负载高时可继续延后。依据：[backend/services/domain_observation_service.py:743](/Users/macmini/Documents/Aixiaoji/ai-anti/backend/services/domain_observation_service.py:743)、[backend/services/domain_observation_service.py:866](/Users/macmini/Documents/Aixiaoji/ai-anti/backend/services/domain_observation_service.py:866)。
- 可手动立即采样单个域名。它逐个请求启用来源，另记手动事件，但不更新正式状态／基线、不计入变化统计。另有“执行到期采样”API，会进入正式统计，两者含义不同。依据：[backend/services/domain_observation_service.py:574](/Users/macmini/Documents/Aixiaoji/ai-anti/backend/services/domain_observation_service.py:574)、[backend/routers/domain_observation.py:147](/Users/macmini/Documents/Aixiaoji/ai-anti/backend/routers/domain_observation.py:147)。

**本机 DNS 干扰的处理：**传统查询使用 `Resolver(configure=False)`，明确指定解析器，避免被观测域名回退到系统 DNS。源码说明这是为了减少本机代理 fake-IP 模式的干扰；但没有检查返回值是否属于 fake-IP／保留地址的规则。DoH 端点主机名自身仍需引导解析，源码明确承认它可能走本机常规解析路径。依据：[backend/services/domain_resolver.py:13](/Users/macmini/Documents/Aixiaoji/ai-anti/backend/services/domain_resolver.py:13)、[backend/services/domain_resolver.py:87](/Users/macmini/Documents/Aixiaoji/ai-anti/backend/services/domain_resolver.py:87)、[backend/services/domain_resolver.py:94](/Users/macmini/Documents/Aixiaoji/ai-anti/backend/services/domain_resolver.py:94)。

**3. 异常判定、规则与 GFW 污染识别**

解析层先分成三种状态，再由服务层比较历史基线：

| 情况 | 记录和处理 | 阈值 |
|---|---|---|
| 首次取得至少一个 A | success；建立该来源的初始 IPv4 基线，不记变化事件 | 一次成功 |
| 后续 IPv4 集合相同 | 更新最近成功／采样状态，不写事件；顺序变化、重复地址不算变化 | 集合相等 |
| 后续 IPv4 集合不同 | 写一条 change，包含新旧集合；立即用新集合更新基线 | 一次不同即记录，无连续确认 |
| NXDOMAIN 或正常应答无 A | unreachable；每次写失联事件，保留成功基线 | 一次即记录 |
| 超时、连接错误、SERVFAIL、REFUSED 等 | fault；每次写采样故障，保留成功基线 | 默认 5 秒超时；无业务层连续失败确认 |
| 故障／失联后恢复原集合 | 更新成功状态，不增加变化次数；没有单独 recovery 事件 | 一次成功 |
| 故障／失联后出现新集合 | 与故障前成功基线比较，记一次 change | 集合不同 |

依据：[backend/services/domain_resolver.py:49](/Users/macmini/Documents/Aixiaoji/ai-anti/backend/services/domain_resolver.py:49)、[backend/services/domain_resolver.py:105](/Users/macmini/Documents/Aixiaoji/ai-anti/backend/services/domain_resolver.py:105)、[backend/services/domain_resolver.py:120](/Users/macmini/Documents/Aixiaoji/ai-anti/backend/services/domain_resolver.py:120)、[backend/services/domain_observation_service.py:689](/Users/macmini/Documents/Aixiaoji/ai-anti/backend/services/domain_observation_service.py:689)。

每个解析来源独立维护基线，不对来源之间的结果做一致性判定。暂停保留历史和基线，恢复后的首次成功比较可携带“暂停后首次比较”标记。依据：[backend/services/domain_observation_service.py:606](/Users/macmini/Documents/Aixiaoji/ai-anti/backend/services/domain_observation_service.py:606)、[backend/services/domain_observation_service.py:449](/Users/macmini/Documents/Aixiaoji/ai-anti/backend/services/domain_observation_service.py:449)、[backend/services/domain_observation_service.py:731](/Users/macmini/Documents/Aixiaoji/ai-anti/backend/services/domain_observation_service.py:731)。

**用户关心的异常覆盖情况：**

| 异常 | 当前覆盖情况 |
|---|---|
| 解析结果与活动入口／期望不一致 | 没有期望值模型；只判断与该来源上次成功结果是否不同 |
| 错误 IP、污染、劫持 | 不识别；只要收到 A 地址就可成为 success |
| 失败、超时 | 有结果分类和历史事件，但不自动定位根因 |
| 多个 DNS 返回不一致 | 分别保存，供人工查看；不自动判差异异常 |
| 各地区解析不一致 | 没有多地探测数据和规则 |
| DNS 记录被篡改 | 无服务商配置审计、权威核验或 DNSSEC 验证；IP 变化最多提供线索 |
| TTL 异常／传播未完成 | 不解析为业务字段、不设阈值；raw 文本可能含 TTL，但不参与判断 |
| NS／CNAME 目标变化 | 没有独立监控；CNAME 改了而最终 A 相同不会形成 IP 变化 |
| 域名到期／证书到期 | 未纳入该模块；没有 WHOIS／RDAP／TLS 证书检查链路 |
| HTTP／TCP／转发可用性 | 不检测；A 解析成功不代表入口能连接或转发 |

上述边界依据为结果结构和完整判定入口：[backend/services/domain_resolver.py:39](/Users/macmini/Documents/Aixiaoji/ai-anti/backend/services/domain_resolver.py:39)、[backend/services/domain_resolver.py:105](/Users/macmini/Documents/Aixiaoji/ai-anti/backend/services/domain_resolver.py:105)、[backend/services/domain_resolver.py:120](/Users/macmini/Documents/Aixiaoji/ai-anti/backend/services/domain_resolver.py:120)、[backend/services/domain_observation_service.py:689](/Users/macmini/Documents/Aixiaoji/ai-anti/backend/services/domain_observation_service.py:689)。

**它没有实现 GFW DNS 污染识别。** 按现有规则推导：如果第一次就收到错误 IP，该 IP 会被接受为基线，之后一直返回同一个错误 IP 也不会产生变化；若先正常、后返回错误 IP，只会记普通 change。多个 DNS／DoH 来源可以提供人工分析材料，但程序没有进一步把差异判为污染。

另外，代码把 NXDOMAIN／无 A 命名为“域名失联”，把 SERVFAIL／超时等命名为“观测故障”，这只是在分类返回结果，不能证明故障归属。NXDOMAIN 可能是错误负应答，仅 AAAA 的域名也会被记“无 A”；SERVFAIL 可能来自权威端。新项目应保留 NXDOMAIN、NODATA、SERVFAIL、TIMEOUT 等具体原因，避免用这些名称直接断定“域名已下线”或“本机有问题”。

**4. 发现异常后的通知、修复和历史**

**域名模块目前只更新状态、记录事件，供管理 API 查询。** 没有调用通知服务、DNS 服务商写 API，也没有切换入口、重试修复或回滚动作。依据：[backend/services/domain_observation_service.py:689](/Users/macmini/Documents/Aixiaoji/ai-anti/backend/services/domain_observation_service.py:689)、[backend/services/domain_observation_service.py:857](/Users/macmini/Documents/Aixiaoji/ai-anti/backend/services/domain_observation_service.py:857)。

- **通知渠道／内容：**域名功能未接入通知，因而没有专用通知模板、接收人、恢复通知。
- **去重／冷却：**没有告警去重键、冷却时间或故障合并。相同成功不写事件只是存储降噪；持续 fault／unreachable 每次采样都写事件。
- **自动修复：**没有改 A／CNAME、Cloudflare／DNSPod 切换或联动 GoEdge 的路径。
- **历史取证：**事件保存新旧 IPv4、时间、来源、采样状态、raw、原因、手动操作标签和恢复观测标记。raw 上限约 4000 字符；传统 DNS 成功时主要是最终 RRset 文本，DoH 保存响应文本，不是完整原始报文档案。

依据：[backend/domain_observation_db.py:112](/Users/macmini/Documents/Aixiaoji/ai-anti/backend/domain_observation_db.py:112)、[backend/services/domain_resolver.py:33](/Users/macmini/Documents/Aixiaoji/ai-anti/backend/services/domain_resolver.py:33)、[backend/services/domain_resolver.py:117](/Users/macmini/Documents/Aixiaoji/ai-anti/backend/services/domain_resolver.py:117)、[backend/services/domain_resolver.py:141](/Users/macmini/Documents/Aixiaoji/ai-anti/backend/services/domain_resolver.py:141)。后台执行自身异常写普通错误日志，见 [backend/services/domain_observation_service.py:894](/Users/macmini/Documents/Aixiaoji/ai-anti/backend/services/domain_observation_service.py:894)。

项目其他业务已有 Telegram Bot 和自定义 Webhook 通知基础设施，但“项目有通知服务”不能推出“域名监控会告警”。Telegram 凭据／接收标识为 `***`，Webhook 地址及密钥为 `***`。依据：[backend/services/alert_service.py:23](/Users/macmini/Documents/Aixiaoji/ai-anti/backend/services/alert_service.py:23)、[backend/services/alert_service.py:46](/Users/macmini/Documents/Aixiaoji/ai-anti/backend/services/alert_service.py:46)。

**5. 数据存储与展示能力**

源码指定使用 `data/domain_observation.db`，独立于订阅日志月库，不按月拆分，以便跨月周统计；连接启用 WAL、外键、10 秒 busy timeout 和 NORMAL 同步模式。依据：[backend/domain_observation_db.py:1](/Users/macmini/Documents/Aixiaoji/ai-anti/backend/domain_observation_db.py:1)、[backend/domain_observation_db.py:51](/Users/macmini/Documents/Aixiaoji/ai-anti/backend/domain_observation_db.py:51)。

| 表 | 作用 |
|---|---|
| `domain_watchlist` | 规范化域名、展示名、启停、单域名间隔 |
| `domain_resolver_sources` | 来源类型、地址、标签、启停、排序 |
| `domain_observation_state` | 每域名×来源的基线、最近成功／采样、状态、到期时间、执行中标记 |
| `domain_observation_events` | change、unreachable、fault、manual_sample 事件流水 |
| `domain_observation_settings` | 全局默认间隔和采样并发数 |

建表依据：[backend/domain_observation_db.py:62](/Users/macmini/Documents/Aixiaoji/ai-anti/backend/domain_observation_db.py:62)。基线持久化，因此重启后仍能接着比较，不会每次启动重新把结果当首次采样。

后端提供以下展示数据：

- **总览：**每域名的今日／本周／本月变化数、今日不同 IPv4 数、最近变化、当前失联及持续时间，可按变化次数和最近变化等排序。
- **详情：**各解析来源的当前状态、成功基线、最近成功时间、下次到期、来源统计、分组事件及 raw 证据；可按来源和事件类型过滤。
- **配置和手动采样：**域名名单、来源列表、全局设置、即时采样结果。

依据：[backend/routers/domain_observation.py:39](/Users/macmini/Documents/Aixiaoji/ai-anti/backend/routers/domain_observation.py:39)、[backend/routers/domain_observation.py:77](/Users/macmini/Documents/Aixiaoji/ai-anti/backend/routers/domain_observation.py:77)、[backend/routers/domain_observation.py:132](/Users/macmini/Documents/Aixiaoji/ai-anti/backend/routers/domain_observation.py:132)、[backend/services/domain_observation_service.py:1270](/Users/macmini/Documents/Aixiaoji/ai-anti/backend/services/domain_observation_service.py:1270)、[backend/services/domain_observation_service.py:1402](/Users/macmini/Documents/Aixiaoji/ai-anti/backend/services/domain_observation_service.py:1402)。

**统计口径容易误读，使用时要明确：**

1. 只用 change 计算变化次数，失联、故障和手动采样不计入；采用上海时区的自然日、周一开始的自然周、自然月。[backend/services/domain_observation_service.py:957](/Users/macmini/Documents/Aixiaoji/ai-anti/backend/services/domain_observation_service.py:957)、[backend/services/domain_observation_service.py:1047](/Users/macmini/Documents/Aixiaoji/ai-anti/backend/services/domain_observation_service.py:1047)。
2. 域名级变化次数取各来源的**最大值**，不是求和；今日不同 IP 数也取单来源最大值，不是跨来源并集。来源停用后仍参与历史统计。[backend/services/domain_observation_service.py:1120](/Users/macmini/Documents/Aixiaoji/ai-anti/backend/services/domain_observation_service.py:1120)、[backend/services/domain_observation_service.py:1134](/Users/macmini/Documents/Aixiaoji/ai-anti/backend/services/domain_observation_service.py:1134)、[backend/tests/test_domain_observation_service.py:706](/Users/macmini/Documents/Aixiaoji/ai-anti/backend/tests/test_domain_observation_service.py:706)。
3. 总览“当前失联”只要一个启用来源当前为 unreachable 就成立，展示其中最早起点；不表示所有用户都无法解析。总览没有对等的 fault 汇总，需看来源详情。[backend/services/domain_observation_service.py:1192](/Users/macmini/Documents/Aixiaoji/ai-anti/backend/services/domain_observation_service.py:1192)、[backend/services/domain_observation_service.py:1370](/Users/macmini/Documents/Aixiaoji/ai-anti/backend/services/domain_observation_service.py:1370)。
4. 详情事件默认只取最近 200 条；服务内部 limit 限于 1～1000，路由没有分页参数。返回的 total 是本次列表长度，不是历史总事件数。[backend/services/domain_observation_service.py:1433](/Users/macmini/Documents/Aixiaoji/ai-anti/backend/services/domain_observation_service.py:1433)、[backend/services/domain_observation_service.py:1281](/Users/macmini/Documents/Aixiaoji/ai-anti/backend/services/domain_observation_service.py:1281)。

文件名显示有域名总览、详情和手动采样弹窗组件；**实际页面布局、有无图表、轮询刷新方式未核实**，本次没有读取 `.vue`／`.js`。Python 侧可确认的是管理总览／详情数据接口，未发现该模块独立公开状态页 API。由于正常不变采样没有完整流水，也没有延迟字段，现有数据不能直接重建精确的历史在线率或每次采样延迟曲线。

**6. 第三方服务／API 与用途**

| 服务或机制 | 用途 | 域名模块的接入情况 | 凭据 |
|---|---|---|---|
| 管理员配置的传统 DNS | 查询域名 A；例如可配置阿里、腾讯、114、Google 等公共解析器 | 已实现通用来源；实际启用名单未知 | 通用接口没有认证字段 |
| 管理员配置的 DoH | HTTPS 传输 DNS 查询；源码示例为阿里 DoH | 已实现通用来源；不是第三方拨测 | 没有独立认证配置；若 URL 自带敏感值，报告中应为 `***` |
| Telegram Bot API | 其他业务向管理员发通知 | 通知基础设施存在，域名观察未调用 | Bot token、接收标识：`***` |
| 自定义 Webhook | 其他业务发送 JSON 告警，可带 Bearer 密钥 | 通知基础设施存在，域名观察未调用 | 地址及密钥：`***` |
| Cloudflare／DNSPod 的权威记录管理 API | 新项目已有的入口 DNS 变更能力 | ai-anti 的域名观察链路中未发现读取／修改记录的实现 | 本次未读取，统一记为 `***` |
| 第三方多地拨测、WHOIS／RDAP、证书监控 | 地域探测、域名到期、证书到期 | 该模块未接入 | 不适用 |

用途和边界依据：[backend/services/domain_resolver.py:81](/Users/macmini/Documents/Aixiaoji/ai-anti/backend/services/domain_resolver.py:81)、[backend/services/domain_resolver.py:94](/Users/macmini/Documents/Aixiaoji/ai-anti/backend/services/domain_resolver.py:94)、[backend/services/domain_observation_service.py:246](/Users/macmini/Documents/Aixiaoji/ai-anti/backend/services/domain_observation_service.py:246)、[backend/services/alert_service.py:23](/Users/macmini/Documents/Aixiaoji/ai-anti/backend/services/alert_service.py:23)、[backend/services/alert_service.py:46](/Users/macmini/Documents/Aixiaoji/ai-anti/backend/services/alert_service.py:46)。腾讯公共递归 DNS 与 DNSPod 记录管理 API 是两种用途，前者返回解析结果，后者管理权威记录；不能因为出现 `119.29.29.29` 就认定存在 DNSPod 自动切换。

**7. 优点、弱点与新项目建议**

**值得借鉴的部分：**

- 解析、状态机、存储和 API 分层，便于用假解析结果验证逻辑；已有 Python 测试覆盖三态、集合比较、基线、暂停、统计、队列。测试本次只读，未执行。[backend/tests/test_domain_resolver.py:1](/Users/macmini/Documents/Aixiaoji/ai-anti/backend/tests/test_domain_resolver.py:1)、[backend/tests/test_domain_observation_service.py:372](/Users/macmini/Documents/Aixiaoji/ai-anti/backend/tests/test_domain_observation_service.py:372)。
- 每来源保留独立基线，按集合比较，能避免地址顺序造成误报；故障／无记录时冻结成功基线，恢复后继续比较。[backend/services/domain_observation_service.py:689](/Users/macmini/Documents/Aixiaoji/ai-anti/backend/services/domain_observation_service.py:689)。
- 手动采样不吞掉正式变化，状态和历史持久化；使用到期队列、并发上限，避免一个慢来源阻塞所有采样。[backend/services/domain_observation_service.py:574](/Users/macmini/Documents/Aixiaoji/ai-anti/backend/services/domain_observation_service.py:574)、[backend/services/domain_observation_service.py:743](/Users/macmini/Documents/Aixiaoji/ai-anti/backend/services/domain_observation_service.py:743)。
- 同时展示“变化次数”和“不同 IP 数”，有助于人工区分小 IP 池轮询与不断出现新地址，但不能自动证明原因。[backend/tests/test_domain_observation_service.py:818](/Users/macmini/Documents/Aixiaoji/ai-anti/backend/tests/test_domain_observation_service.py:818)。

**主要弱点：**

- 缺少业务期望，错误答案会成为新基线；仅监控 A，多源单地，无污染识别和告警／修复闭环。
- 一次不同就记变化，正常轮询、缓存差异、CDN 都可能产生较多事件；持续失联／故障反复写流水，没有独立恢复事件，也未见历史清理任务。
- 正常样本不留完整历史，无法精确计算在线率、查询耗时和历史探测覆盖；总览失联语义和故障展示需要谨慎解释。
- **推测：规模增大后调度频率可能跟不上设置。** 每个 30 秒调度只认领当前剩余并发槽数量，完成后不会立即补位。默认并发 4、查询很快时，后台路径约每分钟发出 8 个“域名×来源”任务，5 分钟约 40 个；不能把“并发 4”理解成持续满载工作池。此为源码推导，未实测。[backend/scheduler.py:166](/Users/macmini/Documents/Aixiaoji/ai-anti/backend/scheduler.py:166)、[backend/services/domain_observation_service.py:824](/Users/macmini/Documents/Aixiaoji/ai-anti/backend/services/domain_observation_service.py:824)、[backend/services/domain_observation_service.py:922](/Users/macmini/Documents/Aixiaoji/ai-anti/backend/services/domain_observation_service.py:922)。
- **推测：多进程部署可能破坏执行互斥。** 认领时会释放所有不属于当前 `_RUN_ID` 的执行中标记，适合恢复旧进程遗留任务，却可能把另一个仍在工作的进程当旧进程。新项目应采用单调度器或真正的过期租约。[backend/services/domain_observation_service.py:26](/Users/macmini/Documents/Aixiaoji/ai-anti/backend/services/domain_observation_service.py:26)、[backend/services/domain_observation_service.py:771](/Users/macmini/Documents/Aixiaoji/ai-anti/backend/services/domain_observation_service.py:771)。
- **推测：历史较多时总览成本会上升。** 总览逐域名读取历史 change 进行即时聚合，而非只读取当前统计窗口或预聚合结果。[backend/services/domain_observation_service.py:1047](/Users/macmini/Documents/Aixiaoji/ai-anti/backend/services/domain_observation_service.py:1047)、[backend/services/domain_observation_service.py:1355](/Users/macmini/Documents/Aixiaoji/ai-anti/backend/services/domain_observation_service.py:1355)。

**以下均为面向新项目的设计建议，不是 ai-anti 已实现功能，也未实施。**

**7.1 把监控挂在入口组和切换记录上，分清三种事实**

| 事实层 | 应从哪里取得 | 要回答的问题 |
|---|---|---|
| 期望配置 | 面板的入口组、活动入口、域名／记录类型、线路、配置版本 | 这个域名现在应该指向哪里？ |
| 权威配置 | 复用 Cloudflare／DNSPod 适配器读取记录；必要时查权威 NS | 服务商是否接受并发布了这次修改？ |
| 实际观察 | 指定探针通过递归 DNS／DoH 查询，并单独做入口连通检查 | 各探测路径看到什么？入口能否连接和转发？ |

“期望配置”应由面板控制，外部解析成功不能自动改写它。另保留“与上次观察相比发生了什么”，用于审计和排障。

例如，面板把 `entry.example.com` 从入口 A 切到 B：期望版本立即变为 B，并记录切换 ID、时间、旧值、新值和 TTL；DNS 服务商写入成功先标记“等待传播”；在合理窗口内同时接受旧值与新值并分别展示；窗口后继续返回旧值才判“未收敛”。窗口长度应结合旧 TTL、负缓存和服务商行为确定，不能只取写入后的新 TTL。

这里还要处理两种特殊情况：Cloudflare 开启代理时，公共 DNS 通常展示代理地址，不能直接与节点真实 IP 比较；DNSPod 按线路分流时，不同探针合法地看到不同集合。CNAME 则应分别保存“配置的目标名称”和“最终解析到的地址”，按业务策略检查，不能把任何结果差异都当异常。

**7.2 利用已有 Go 节点作为探针，但记录真实观测位置**

面板承担调度、判定和 SQLite 写入，Go 节点承担 DNS／DoH 与连接探测，只回传结构化结果。每份结果至少带探针 ID、位置标签、解析来源、记录类型、响应码、答案、TTL、耗时和采样时间。管理员选取少量有代表性的节点即可起步。

如果现有节点全在境外，它们只能代表这些境外路径；要判断中国大陆用户是否受影响，需要能代表目标网络的境内探针。对公共 DNS 地址的访问也有路径与 Anycast 差异，所以“阿里 DNS＋Google DNS”本身不构成境内／境外两地测量。

**7.3 规则先区分异常类别，再决定是否切换**

- **服务商写入失败／配置偏离：**优先走已有 DNS 适配器的重试、核对和修复流程，保留操作审计。
- **传播中／递归缓存未收敛：**按切换版本和 TTL 窗口观察，避免反复切回。
- **解析错误或疑似污染：**标明来源、探针和证据，先告警；与入口连接失败分开处理。
- **采样端故障：**保留最近成功结果并标“数据已过期／探针异常”，不直接宣布入口离线。
- **入口连接／转发故障：**由独立健康检查确认，满足已有切换策略后再选择健康备用入口。

作为初始参数建议，可普通每 3～5 分钟采样，切换后临时每 15～30 秒检查收敛；单次超时 3～5 秒；连续 3 次异常并有至少两份独立证据后升级告警，连续 2 次符合期望后恢复。所谓独立证据应考虑探针位置和解析服务商，不能把同一服务商的 DNS 与 DoH 简单算两票。参数需按域名数量、TTL 和网络状况调整。

**7.4 GFW 污染应输出“疑似”及证据，不能只凭一个错误 IP 定性**

可把“业务期望＋权威／服务商配置＋多个实际探测路径”组合起来判断：若某境内路径的明文 DNS 持续返回不在允许集合中的地址，而可信 DoH、其他位置和权威配置与期望一致，才形成“该路径疑似 DNS 注入／污染”的较强线索。仍需排除线路分流、CDN、缓存、代理 fake-IP 和探针问题。

保留响应码、答案、TTL、来源、位置和时间；支持按业务允许范围检查地址，必要时关注保留／fake-IP 地址，但这些特征不能单独证明 GFW。DoH 应有可靠的端点引导方案；验证 DNSSEC 可作为已签名域名的补充，未签名域名不能因此判异常。该归因方法也不能保证覆盖所有污染形式。

**7.5 自动化复用现有 DNS 切换链路，防止两套逻辑互相操作**

监控只产生带配置版本和证据的事件，统一交给已有入口切换流程执行。切换前重新核对当前活动入口和备用健康状态，防止旧采样覆盖新配置；同一入口组只允许一个切换操作进行，使用幂等键，切换后进入传播观察。

只有 DNS 解析异常时，应先判断是否属于配置错误、缓存未收敛或特定路径问题。若污染针对域名，换同一域名的 IP 可能无效；若域名已经失效，改 A 也不能解决。是否换入口、换域名或只通知，应根据故障类别决定。可先把自动操作限制在证据充分的规则上，采用例如 15 分钟冷却和次数限制，逐步调优。

**7.6 SQLite 与 React＋AntD 的最小实用形态**

保留现有入口组／DNS 记录作为期望来源，增加探针／解析来源、当前观察状态、短期样本、异常事件和通知投递记录；切换历史直接复用已有表或操作记录。正常样本保留可配置的短期窗口，长期仅存聚合和事件；事件采用游标分页，调度用持续补位的有限工作池。

AntD 总览以“入口组／域名”为行，显示活动入口、期望记录、权威写入状态、各探针解析状态、传播状态、最近成功、延迟和当前异常；详情展示按探针／来源分组的时间线，并关联切换前后证据。明确区分“变化”“与期望不符”“传播中”“探针故障”，避免一个红色失联标签涵盖所有情况。管理员自用场景先做好管理页与通知即可，公开状态页按需要再加。

通知至少包含入口组、域名／类型、期望和实测、探针／解析器、异常持续时间、关联切换 ID及操作结果。按“入口组＋域名＋异常类别”合并事件，首次确认、状态变化和恢复时通知；持续异常用可配置冷却提醒，投递失败另记状态。通知和 DNS 凭据留在面板端，节点只持有必要的探针身份信息。

下一步建议：以现有入口组和 DNS 切换的数据模型为基础，先确定“期望记录、传播窗口、探针结果、异常事件”之间的关系，再评审告警和自动切换条件；本次仅交付调研报告。
