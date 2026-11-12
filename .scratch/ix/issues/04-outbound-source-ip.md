# 04 服务器出站源 IPv4 与本机地址应用校验

Status: ready-for-agent
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

- [ ] 可设置、回读和清除服务器出站 IP；名称编辑与出站配置不会相互覆盖。
- [ ] 目标实际看到选定源地址；下一跳 WSS 和健康探测不绑定。
- [ ] 非本机地址产生应用错误且无回退，错误能定位到受影响端口。
- [ ] 离线重启、部分失败和写盘故障不丢失源限制；旧节点不收到新配置。
- [ ] A 的源 IP/撤销缓存统一写格式 2；真实旧读取器拒绝且停转发，人工空启动与重连过滤边界已验证。

## 不做的事

不配置网卡或路由，不添加公网 IP，不做 IPv6、监听源绑定、按规则覆盖或源地址轮换；不改变管理/检测连接；不提交构建产物。
