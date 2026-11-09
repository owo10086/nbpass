# 11: 隧道资源上限与清理

**What to build:** 隧道规则同样受节点资源上限约束：每个 yamux 会话的流数量有上限，握手有超时，隧道流正确处理半关闭和取消；达到上限时新连接被拒，控制服务继续响应。

**Blocked by:** 07, 08

**Status:** ready-for-human

**Refs:** 规格用户故事 132、133；必测场景 17（隧道部分）

- [x] 每个 yamux 会话最多 1024 个流，超出时新连接被拒（上限可配置，测试用小值验证）
- [x] 隧道握手有超时，半开握手不长期占用资源
- [x] 隧道业务连接计入节点和规则的并发上限
- [x] 隧道流半关闭、取消后资源被回收；达到上限期间控制服务仍响应
- [x] 以上行为由测试台通过面板 HTTP API 与真实 TCP 验证


## Comments

2026-11-09 实施记录（仅 `ticket/11` 当前工作目录；未创建工作树，未改 docs/ 或其他票，不推送、不合并、不切换分支）：

- **实现与范围**：主要新增 `internal/forwarding/tunnel_limits.go` 与 `tunnel_carrier.go`，复用票 07 的 `resourceLimiter/resourceLease`、`directInput/proxyDirect`、共享空闲时钟及固定复制缓冲。`engine.go` 仅把隧道准入分派到新入口；`tunnel.go` 保留票 08 的 WSS、证书固定、节点对认证和会话复用，仅接入容量、超时与新流生命周期。不改变控制报告的独立推进语义，不实现票 09 授权代次/已有流复查、票 10 流量累计或票 12 健康选择。
- **共享预算**：直连与隧道共同使用默认每节点 **20000**、每规则 **5000**、待拨号 **512**。入口接受 TCP 后、创建业务协程前原子预占；出口在读取逐流规则身份前预占节点及待拨号槽，授权身份核对后补占规则槽；超限直接拒绝，不排队、不影响健康载体上的已有流。DNS、WSS/认证及出口目标拨号计入待拨号。拨号完成释放拨号槽，失败、超时、RST、撤销和双向复制完成后幂等释放剩余预算。兼任入口和出口沿用经认证的本机节点身份去重，只扣一份逻辑业务及拨号预算；WSS 载体、目标 TCP 和控制连接不重复算业务连接。
- **yamux 容量与清理**：每个入口—出口仍只复用一个会话，默认 **1024** 流。入口在开流锁内检查含在途流的实际 `NumStreams`，出口独立在协议接收层限制流数；满额不通过另建载体绕过上限。入口拒绝反向 SYN，避免对端再占一份反向接收流预算。取消、错误及关闭使用 RST，FIN 只关闭写方向；双向复制、预读协程及空闲定时器退出后才归还业务预算。两个方向都正确支持半关闭，已半关闭但长期无数据的连接仍受默认 **2 小时**空闲回收。
- **依赖与协议兼容**：运行时从 `github.com/hashicorp/yamux v0.1.2` 改用 `github.com/libp2p/go-yamux/v5 v5.1.0`，以获得公开 `Reset/CloseWrite` 与 `MaxIncomingStreams`；新增间接依赖 `go-buffer-pool v0.0.2`。旧 yamux 仅保留为新旧入口/出口互通回归的测试依赖。WSS 路径、子协议、隧道版本 1、认证/请求帧和目标授权语义不变；两种滚动更新方向均验证 FIN 后完整请求/回复。
- **握手、总期限与库边界**：入口 TLS/WSS/认证共用默认 **3 秒**期限，逐流建连受默认 **10 秒**总预算；出口 TLS、HTTP 头、认证与逐流请求各有握手期限，出口 DNS/候选拨号共用默认 10 秒，仍保留最多 3 个不同 IPv4、单候选最多 3 秒及逐地址私网检查。核心认证/拨号期限支持注入时钟快进，真实网络 Deadline 同时兜底。新库在已取消 context 开流时可能遗留未发送的流：先检查取消，短暂开流阶段取得句柄后再按取消状态 Reset，并给开流、FIN、RST 的控制帧排队设置独立总期限。只有载体在期限内无法排入控制帧才关闭整个会话；正常单流取消保持其他流可用。超时返回前等待会话清理完成，覆盖“停止回调与超时相邻发生”的竞态。
- **缓冲口径与歧义决定**：沿用每方向 **32 KiB** 的复制缓冲和有界预读，禁用绕过固定缓冲的快速复制。yamux 协议接收窗口另有最小 **256 KiB/流**，固定在该值、禁止自动扩窗；它属于额外的协议接收缓冲，不能把复制缓冲 64 KiB 说成整条隧道流的全部内存。节点业务/待拨号预算与会话流数共同给复制及协议缓冲派生上界；不把内核 TCP 缓冲、TLS/WSS、Go 元数据算进上述复制缓冲。正常 FIN 不等同整连接取消；满额预读时不能再读客户端，未观测到的 RST 仍由拨号总期限兜底，沿用票 07 的背压取舍。配置先使用节点启动参数，与票 07 一致，本票不新增面板设置页面。
- **启动参数**：新增 `-tunnel-max-streams`（1024）、`-tunnel-handshake-timeout`（3s）；提供共享预算的通用名称 `-max-connections`、`-max-connections-per-rule`、`-max-pending-dials`、`-dial-timeout`、`-idle-timeout`。原 `-direct-*` 名称保留为同一字段的兼容别名，现作用于全部业务路径；同一字段重复传参按命令行最后一次赋值。零值使用安全默认值，负数和超出 uint32 的流上限在启动前拒绝。
- **验收逐条对照**：① HTTP 集群分别把入口或出口流上限设为 2，跨规则填满后连续 5 次真实 TCP 新连接被拒、会话仍只有 1 条，RST 后同会话恢复准入；物理测试实际创建默认 1024 条流并拒绝第 1025 条，另覆盖出口半开流及反向 SYN；② 经 HTTP 获取公开证书后，用真实 TLS/HTTP/WSS 分别制造半开握手，验证配置期限关闭；认证另用注入时钟验证；逐流请求不完整也会重置并释放预算；③ 分别在入口、出口、兼任节点配置节点 3/规则 2，隧道与直连混合占槽，验证规则与节点上限及兼任去重；④ 真实 TCP 分别验证客户端先 FIN、目标先 FIN，完整传输 300000 字节请求与 420000 字节回复；验证 RST、删规则、拨号超时、两小时空闲回收及单向数据续期，HTTP 探针归零后可重新准入；满额期间活性继续更新、新规则配置正常确认；⑤ 所有业务管理、配置版本、活性与探针断言只走面板 HTTP API，使用真实节点核心、gRPC/TLS/WSS/yamux/TCP。外部目标挂起仅通过可注入拨号依赖模拟。
- **TDD 与独立审查**：新增 **18 个顶层测试**（HTTP 集群 7、包内物理 10、启动参数 1）。入口/出口/兼任规则超限先在旧实现上失败；新增参数先因未注册失败；已取消开流回归先复现 100 次调用泄漏 53 个槽；控制帧队列回归先复现开流超过总期限；入口接收反向流先复现未拒绝。另以临时移除容量门禁、注入时钟取消与强制 Reset 验证相应回归确实失败，随后恢复实现。独立审查指出的取消开流泄漏、发送队列总期限、关闭回调竞态均已补齐；并发本地 Close 使用同一 `sync.Once` 等待完整 RST 清理，确定性回归先复现第二次 Close 返回时残留 66 条流，再修复；握手测试改为外部等待界限，避免客户端自行 deadline 关闭被误判为服务端回收；拨号取消测试先在真实一秒内等待目标 context 取消，再推进时钟查探针。
- **载体边界**：新 yamux 缓存底层写期限，原 WSS `NetConn` 写定时器可能在空闲后刷新期限时仍执行旧回调并取消下一次写；新增载体适配器保留读流适配，每次实际 WSS 写入改用独立的 context 期限，消除跨写入的共享定时器。写期限由认证串行流程或单一 yamux 写循环设置，运行中取消通过关闭载体执行。载体回收使用 `CloseNow`，不等待失去响应的对端完成 WSS 关闭握手；相应回归先以一秒内关闭未完成失败。认证失败和正常退出都显式取消载体子 context，避免出口长期存活时积累子 context。
- **迁移与字段号**：本票 **无新增数据库迁移、无新增 protobuf 字段号**，未占用分配的 **130–139** 号段，未改 proto 或生成源码。
- **后续接缝与产物**：票 10 请将隧道入口的生命周期计量接在 `acceptTunnel/serveLimitedTunnel`，实际成功写入字节接在复用的 `directInput.copyTo` 与 `activityWriter`；不能只接票 08 旧的 `listenerRule.serve/proxyTCP`，这两个旧入口现在不再被业务准入调用。票 09 可继续扩展 `authorized`/逐流授权，票 12 可区分 `errTunnelCapacity` 本地容量与真实载体故障，不能把容量拒绝当出口故障。本票不依赖前端改动；提交前还原 `web/dist internal/nodebundle`。**合并后必须重新生成内嵌节点包**（`make node-bundle` 或 `make build`），才能让安装下载得到本票节点实现。
- **最终验证与收尾**：最终源码下 `env -u FORCE_COLOR make test` 与 `go test -race ./... -count=1` 均全绿，包含 TypeScript/Vite、内嵌节点构建、go vet 与全部 Go 测试；Vite 仅有既有 AntD 大包提示。载体关闭、并发关闭和半开流三组回归另通过 `-race -count=100`。首轮全仓 race 暴露既有 `TestCombinedEntryExitCountsBusinessOnce` 在释放临时端口后被抢占的竞争，已只将该场景改用票 07 的预留真实监听器，保留原业务断言，连续 `-race -count=10` 及最终全仓复跑通过。面板、节点均通过 `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath`，`file` 确认为静态链接 x86-64 ELF。独立审查完成最终载体与端口修正复核，无遗留问题；`gofmt -l` 与 `git diff --check` 通过。已执行 `git checkout -- web/dist internal/nodebundle`，无新增未跟踪前端产物，已删除本票专用交叉编译二进制；仅提交源码、测试、依赖与本票记录。

人工验收清单（以下尚未执行；自动化小值测试不代替 Linux 部署与容量验收）：

1. 合并后统一 `make build`，在两个 Linux amd64 节点配置可入站 WSS 出口，入口/出口分别使用 `-tunnel-max-streams=2 -max-connections=3 -max-connections-per-rule=2 -max-pending-dials=1` 启动。通过面板建两条隧道规则和一条直连规则，检查满额拒绝、既有连接正常、探针口径与释放后重新准入；再核对兼任入口/出口只计一次。
2. 分别制造 TLS、HTTP Upgrade、认证与逐流请求半开；缩短 `-tunnel-handshake-timeout`，确认连接关闭且后续合法业务可用。挂起目标连接后 RST、删规则或停节点，检查资源回收、活性与配置确认仍推进。
3. 使用支持半关闭的真实客户端验证两种 FIN 顺序和大请求/大回复；用 `-idle-timeout=5s` 验证空闲回收与单向续期，按默认配置实际观察两小时回收；限制载体带宽制造发送队列背压，确认控制帧超时能结束阻塞。
4. 在受控 Linux 环境验证 1000 并发、100 Mbps 持续吞吐、24 小时长连接，记录 CPU、RSS、文件描述符和内核缓冲；满载时检查控制响应。分别让旧入口连接新出口、新入口连接旧出口，核对实际滚动部署互通。


2026-11-09 `merge/11` 合并本地 `main`（`e7958e2`）整合说明：

- **冲突与接续位置**：实际文本冲突仅在 `internal/forwarding/tunnel.go` 的 `tunnelClient.dial` 和旧 `tunnelServer.serveStream`。`dial` 接收完整 `*controlpb.ForwardRule`，通过 `streamAuthorization` 发送入口身份、授权代次、目标地址/端口/地址族，同时继续使用票 11 的可配置总期限、握手期限、开流容量检查和取消 Reset。旧 `serveStream` 的授权逻辑完整迁入 `tunnel_limits.go` 的 `serveLimitedStream`，不恢复旧业务入口。
- **逐流授权与资源生命周期**：出口在握手后先 `authorizeStream` 校验，再绑定规则预算；目标仅从出口授权配置取得。拨号前启动 `watchStreamAuthorization`，每秒用注入时钟复查，撤权同时取消未完成 DNS/拨号和已有流；拨号返回后、成功响应前再次核验。保留零代次旧请求兼容，main 的出口应用确认门禁原样保留。本机入口在出口继续按已认证节点身份去重，不重复扣业务/待拨号槽和业务计数；远端入口在读取规则身份前预占出口预算。所有失败、撤权、取消和双向复制结束路径仍归还预算。
- **票 10 计量接缝**：自动合并不会提示旧入口已不再调用，故将 `meterConnection` 生命周期和 `meteredConns` 实际成功写入字节计量接入 `acceptTunnel/serveLimitedTunnel`。出口不重复计量，预读、WSS、yamux 认证和逐流控制帧不计入业务字节；保留固定缓冲、半关闭、空闲回收和 WSS `CloseNow`。
- **拒绝帧与新 yamux**：首轮保留票 09 的 `TestTunnelPerStreamAuthorizationRejectsForgedRequests` 原断言即检出直接 RST 会使对端丢弃未读拒绝响应。授权拒绝、目标拨号失败、拨号后二次核验失败统一先发送有写期限的拒绝帧及 FIN，再由原生命周期 RST 清理，既保留明确拒绝响应又及时释放流槽；未增加 sleep，也未将协议错误放宽为“断开即可”。
- **测试适配与回归**：票 09 开流测试适配新 yamux 的 `OpenStream(ctx)`；票 11 拨号测试传递真实完整规则，所有原断言保留。仅 `TestTunnelAuthorizationDoesNotExpireWhileBothNodesOffline` 的 48 小时离线授权场景将两端空闲期限配置为 72 小时，以隔离票 11 默认 2 小时的空闲回收；48 小时时间跨度、原有流继续传输及新建流成功的断言不变，生产默认值不变。新增 `TestTunnelRejectsAfterTargetDialAndReleasesBudget` 两个分支，检查拨号失败/拨号返回前撤权的拒绝响应、目标关闭、业务计数归零和节点/规则/待拨号预算全部可重用；临时移除拒绝响应与拨号后二次核验时，两分支分别以 EOF 和错误成功响应失败，随后恢复实现。拒绝响应及新增回归另通过 `-race -count=10`。只读独立复核未发现关键或重要问题。
- **全仓检查暴露的既有测试竞争**：`TestEventOverflowProducesVisibleGap` 在 make 与 race 中复现预期 3 条实际收到 5 条：面板已断链不代表旧上报流程已退出，旧流程仍可能将新事件移入不参与 Count 淘汰的在途批次。仅调整测试依赖与同步：在下一轮心跳的注入公网 IP 查询处以通道暂停（位于事件打包之前），再断链、录入 5 条、放行并恢复链路。没有修改生产事件逻辑、队列容量或最新 3 条/溢出缺口至少 2 条的原断言，也没有增加 sleep；该用例修正后 `-race -count=10` 通过。
- **端口预留与其他时序观察**：全仓 make 在 `TestSingleExitUnavailableNeverFallsBackToDirect` 和 `TestTunnelCompromisedEntryCannotChangeAuthorization` 明确报入口/出口 `bind: address already in use`。仅调整测试台 `directPorts`，预先独占绑定端口，节点实际调用 `Listen` 时才激活监听并移交真实 TCP socket；隧道及授权 fixture 使用该预留方式。原“出口未确认时入口不得监听”断言先在旧预监听 helper 上明确失败，再用仅绑定 helper 验证通过；未将入口门禁改成业务交换失败。复核指出的 Linux `SO_REUSEADDR` 竞争绑定问题已按 socket(7) 核验，改在 `Listen` 成功后启用该选项，原 fd 和未取用 fd 均有完整关闭路径。24 小时补报用例在控制连接建立后仍可能尚未收到缺口，仅改为等待 HTTP 出现 1 条缺口再执行原数量及 `expired` 断言，边界事件断言保持不变。中间验证轮的既有 `TestDNSWatchPoisoningRequiresThreeSamePathSamplesAndNeverWrites` 还出现过节点状态等待超时；未改该用例或 DNS 生产逻辑，此前完整 race 已通过。
- **范围与产物约定**：仅在指定 `m11` 目录执行，不切分支、不创建工作树、不 push；不处理票 22。本次无新增迁移、无新增 protobuf 字段；proto 与 `control.pb.go` 均与合入的 main 完全一致，无生成代码冲突，无须重生成。交付沿用 main 的 `web/dist internal/nodebundle`，发布安装包前仍需统一重新构建内嵌节点包。
- **最终验证与交付**：最终代码下 `env -u FORCE_COLOR make test`、`go test -race ./... -count=1` 均全绿，包含前端 TypeScript/Vite、节点包构建、go vet 和全部 Go 测试。最终端口 helper 下，确认门禁、伪造入口、出口不可用、24 小时补报四组另通过 `-race -count=10`；事件溢出同步修正也通过 `-race -count=10`。面板与节点均通过 `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath`，`file` 确认为静态链接 x86-64 ELF；Linux 集群测试包也交叉编译通过。`gofmt -l`、`git diff --check` 通过。提交前已执行 `git checkout -- web/dist internal/nodebundle` 并核对其与本轮合入的 main `e7958e2` 完全一致；专用交叉编译产物已删除。
- **合并与后续 main 验证**：代码整合提交 `b3e14f9`，父提交为票 11 的 `cd8a0ae` 和本轮开始时的 main `e7958e2`。验证期间本地 main 又合入票 19，前进至 `cfc17aa`；在当前分支执行 `git merge-tree --write-tree cfc17aab99ac95111bd741e7202f13f24bbc4339 HEAD` 返回 0，确认与该 main 可自动合并、无冲突。票 11 文档在本轮验证和复核完成后单独统一提交。


2026-11-09 `merge/11` 再次合并本地 `main`（票 22 `bf93057` / `32ed49e`，并接续验证期间前进的 `5673195`）整合说明：

- **合并提交**：`bae708b` 合入 `32ed49e`，整合票 22 出口授权预检与票 11 资源限制；验证期间 main 又合入票 38，随后以 `792805d` 合入 `5673195`。全部项目操作在指定 `m11` 目录完成，未切分支、未创建工作树、未 push，未操作主项目目录。
- **隧道冲突**：`internal/forwarding/tunnel.go` 的能力协商与预检入口保留，业务 `dial` 与预检 `ready` 共用 `open`，通过票 11 的 `openStream` 检查实际会话流数、处理取消和控制帧排队期限；继续使用票 11 的 WSS carrier 与 `CloseNow` 清理。`ready` 预占入口节点、规则及待拨号预算，同时受配置握手期限、拨号总期限、快照共享 3 秒期限和调用方取消约束；同机入口/出口仍按认证身份去重。
- **出口接续与兼容**：票 22 原 `serveStream` 的预检逻辑移入 `tunnel_limits.go/serveLimitedStream`，在出口预占预算、核验逐流授权并绑定规则预算后才确认 Ready；不拨目标，不计入业务字节。成功预检与拒绝响应都先 FIN 保留控制帧，再由原生命周期 RST 清理流槽和预算。保留票 09 的身份、授权代次、目标核验及每秒撤权，拨号前和成功交付前复核；票 11 的 FIN/RST、取消、空闲、固定缓冲及目标清理全部保留。旧出口不声明能力时不发 Check，沿用既有确认门禁；票 22 旧入口的完整规则对象 Check 继续按认证身份、代次及目标授权核验，监听端口等元数据不影响授权。
- **测试夹具冲突**：`tunnel_test.go` 保留票 11 的仅绑定端口预留；`authorization_test.go` 同时保留资源限制注入、仅绑定预留及 main 的真实监听器计数。`directPorts` 在未命中自身预留 socket 时转交原注入的监听函数，兼容 main 的 `Harness.ListenTCP`，修正混用两类预留端口导致的确定性 bind 冲突。两边测试和断言完整保留，未用业务交换失败代替入口监听门禁。
- **预检回归与独立复核**：新增入口/出口节点、规则、待拨号预算六分支回归，验证超限拒绝、恢复准入、不拨目标及预算归零；另覆盖预检拨号期限、握手期限、取消、会话流数上限和 RST 清理。独立复核指出普通互斥锁会使预检等待既有业务时绕过总期限，新增实际持锁、活的 100ms context 回归，两类锁均先在旧实现的一秒外部界限失败；改为零值可用、支持 context 的 `tunnelMutex` 后通过，关闭客户端仍保留原等待语义。预检、锁等待、旧出口及旧规则对象兼容用例通过 `-race -count=10`；复核无遗留问题。
- **仲裁时序定位**：首轮完整检查发现 `TestCheckerArbitrationReleasesCompletedTasks` 的新轮检测点集合偶发为空。遵照负载排查要求先单独重跑，单次通过但连续十次可复现，确认不是可直接忽略的负载超时。其快进 helper 仅等待目标节点 HTTP 活性落库，尚未消费的控制确认可能随下一步快进触发 15 秒超时；改为沿用 main 已有 `awaitControlIdle`，每步快进前等控制消息处理完成。原十轮、每轮 abnormal 和任务槽回收断言不变，无新增 sleep；修正后普通及 race 连续十次通过，最终两轮全仓检查也通过。
- **追赶 main 的事件测试冲突**：第二次合并仅 `events_test.go` 冲突。溢出场景同时保留票 11 的查询前通道同步和 main 的批次持久化门禁；24 小时场景采用 main 的真实时间条件轮询，同时等待业务事件与 expired 缺口，不推进假时钟。最新三条事件、淘汰数量、窗口边界消息和缺口原因断言全部保留，冲突整合未新增固定 sleep；两个场景通过 `-race -count=10`，独立只读复核通过。
- **最终验证与产物**：基于最终整合源码，`env -u FORCE_COLOR make test` 全绿（前端 TypeScript/Vite、内嵌节点构建、go vet、全部 Go 测试）；`go test -race ./... -count=1` 全绿，集群包耗时约 257 秒，无数据竞态。面板和节点均通过 `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath`，`file` 确认为静态链接 x86-64 ELF。Vite 仅有既有大包提示。protobuf、生成源码及迁移与 main 一致，本票无迁移、无新字段，无须 `make proto`。每次提交前均执行 `git checkout -- web/dist internal/nodebundle`，最终产物与 main `5673195` 完全一致；专用交叉编译二进制已删除。`git diff --check` 通过，`git merge-base --is-ancestor main HEAD` 和 `git merge-tree --write-tree main HEAD` 均返回 0，确认当前 main 已完整包含且可无冲突回合。
