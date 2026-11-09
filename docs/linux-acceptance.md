# Linux 人工验收清单

用于票 40 实际执行；**本文件提供步骤，不表示任何真实 Linux 项目已经通过**。测试台通过不能代替本清单。准备隔离的 Linux amd64 + systemd 面板、至少两个节点、独立 TLS 代理、可控 TCP 目标和压测客户端；涉及真实 DNSPod/换 IP 的步骤只使用测试记录与测试实例。按 [部署说明](deploy-linux.md) 从集成后源码重新构建，不使用仓库中尚未更新的内嵌产物。

在票 40 Comments 记录每项结果（通过/失败/未执行）、时间、提交号、Linux/内核/systemd/Nginx 版本、硬件、网络、操作与观测证据；涉及新旧程序均记录 SHA256。秘密、完整票据、token、密钥、Cookie 不进入记录。每个小节独立验收，失败或环境不具备时如实标记，不能用模拟结果勾选。

## A. 面板安装与重复执行

- [ ] **A1 首次安装**。按部署说明执行面板一条命令安装；运行 `systemctl is-enabled nbpass`、`systemctl is-active nbpass`、`systemctl cat nbpass`、`ss -lntp`。通过：开机启用且 active，普通 nbpass 用户运行，两监听分别为回环 8080/8081，HTTPS 可完成首次设密和登录；unit 不含密码或 token。
- [ ] **A2 重复安装保留数据**。添加节点/规则、保存保留期和可信代理，记录数据库/密钥摘要及环境文件；以相同程序重跑安装命令，再登录并核对对象和设置。通过：`panel.key` 内容不变，账号、对象、设置保留（运行中数据库内容会正常变化），环境文件和 unit 自定义内容保留；不同程序文件被安装器拒绝，并提示走 upgrade，不会绕过备份迁移。
- [ ] **A3 下载与路径拒绝**。在空测试部署用错误 SHA256 执行 HTTPS 安装；另测试失效证书、非 HTTPS 地址、不支持架构、普通用户执行，以及把数据目录或程序路径换成指向测试文件的符号链接。通过：非零退出，不发布新程序、不改动链接目标、不启动错误服务；还原路径后正常安装成功。不要对生产路径做该故障注入。
- [ ] **A4 开机恢复**。重启面板主机。通过：服务自动启动，HTTPS 可登录，节点重新连接，既有配置和设置保留；启动后前 90 秒不把未确认旧节点直接判为离线。

## B. 真实安装票据兑换与节点重复安装

- [ ] **B1 首次接入**。网页添加节点，复制命令在节点执行；检查 `systemctl is-active nbpass-node nbpass-update`、`systemctl cat` 和面板节点列表。通过：两服务 active，票据经 HTTPS 控制源兑换成功，节点在线且活性约 10 秒更新；长期 token 只存加密凭据，unit 和持久配置不含票据或 token。
- [ ] **B2 过期与并发消费**。一张票据等待满 30 分钟再安装，应拒绝；另一张新票据在两台隔离节点同时执行，应只有一个完成兑换。可在各节点先用 `curl -o /var/tmp/install-node.sh` 保存脚本，再同时 `sudo sh /var/tmp/install-node.sh ...`。通过：过期或已消费票据不能接入；失败实例不替换原二进制/unit。不要对已运行的同一节点并发重装。
- [ ] **B3 保留缓存重新安装**。先同步一条规则并产生事件、非零业务流量；停止节点，记录 `node.key` 与现有 `*.enc` 的摘要（凭据除外）。网页重置 token，使用新命令重装。通过：节点 key、配置/撤权状态、事件/流量/换 IP 记录没有被安装器清空；两服务重新启动，新凭据可接入，旧 token 不再可用；运行后缓存因正常业务变化不应要求始终字节相同。
- [ ] **B4 失败与普通重启**。在已有节点上分别用消费过的票据、不可达下载源、坏控制证书尝试安装，前后对比二进制和 unit；随后普通 `systemctl restart nbpass-node`。通过：失败不覆盖原二进制/unit，普通重启不用票据；重置 token 已在面板撤销旧 token 时，不要求旧服务仍能在线。
- [ ] **B5 离线缓存**。已同步后阻断控制域名的连接，保持业务目标可达，再重启节点。通过：仍按实际缓存转发，配置显示待确认；节点不离线换 IP 或写 DNS。恢复控制网络后配置收敛，面板离线期间删除的规则被撤销，不能复活。

## C. 面板 CLI 与旧备份恢复

- [ ] **C1 密码重置**。两个浏览器登录，运行 `sudo -u nbpass /usr/local/bin/nbpass reset-password -data-dir /var/lib/nbpass`，终端交互输入两次。通过：不回显，不出现在参数/输出，两个旧会话立即失效，原锁定清除，新密码可用。输入不一致、错误目录均失败，旧密码不被更改；停服务重置后启动也有效。
- [ ] **C2 在线备份**。服务运行并持续保存测试配置时执行部署说明的 backup，备份目录必须不存在。通过：业务不重启，备份包括数据库、配对密钥、程序、摘要、RECOVERY.md；提交在快照前的数据可恢复，备份不是简单漏掉 WAL 的主库复制。以独立目录执行 restore 核对备份，而非覆盖正在使用的数据。
- [ ] **C3 升级成功**。先在服务运行时执行新程序 upgrade（应被数据锁拒绝），停服务后用新目录重试，显式 `-program` 指旧程序。通过：先完整备份旧程序/库/密钥，迁移成功才输出完成；旧程序未被 CLI 自行替换。按文档原子部署新程序再启动，原配置可读。
- [ ] **C4 迁移失败**。只在隔离环境用一个专用构建增加必定失败的末尾事务迁移，重复 C3；不在发行构建中保留故障代码。通过：失败前已备份，数据库版本/原数据不被部分提交，原密钥和旧程序保留；不部署坏程序，直接启动旧服务成功。记录错误和恢复范围（程序、数据库、密钥），不重复执行同一失败迁移。
- [ ] **C5 旧快照恢复保护**。备份后继续修改配置，使节点已应用版本高于备份，产生测试换 IP 动作并改变实际测试 DNS 记录；停服务，使用兼容程序 restore 到尚不存在的新目录。按部署文档切换 ExecStart 的数据目录及 ReadWritePaths，启动。通过：旧会话和备份内票据失效，旧自动动作不重放；`GET /api/recovery` 显示待核对。节点报告后新配置版本高于其高水位，实际应用成功。恢复至已存在目录或篡改备份摘要应拒绝。
- [ ] **C6 核对与历史不足**。先保持一个旧成员离线，在面板同源会话调用 `POST /api/recovery/reconcile`，请求 `{"confirm":true}`；再恢复它、等待应用并重新核对。通过：缺证据的节点/入口组继续暂停；DNSPod 只读核对并采用对应已核对成员的实际记录，不写回旧快照。换 IP 历史不足的节点仍暂停自动换 IP，补齐最近 24 小时日志或恢复满 24 小时才解除该项保护；未核对组、未知动作等其他门禁仍有效。用服务商调用记录核实没有重复换 IP/盲目 DNS 写入。记录 24 小时观察项，不能临时改系统时钟冒充真实等待。

## D. 真实 systemd 节点升级与回滚

使用重新构建并内嵌候选程序的测试面板，而不是在下载之后修改文件。候选 `-update-check` 必须按清单返回真实版本/平台；启动失败构建可在正式启动路径退出，业务失败构建可注入本地就绪失败。故障构建仅用于隔离验收。

- [ ] **D1 正常升级**。保持直连/隧道连接，在节点页发起升级，查看任务与两个服务日志。通过：下载→校验→切换→回连→就绪，只有新进程/版本一致、配置已应用且本地业务正常才成功；旧连接中断有明确提示，升级后新业务可用，二进制归 root 所有、0755、组不可写（更新器生成时组为 nbpass-node）。连续点击不能生成冲突任务。
- [ ] **D2 清单与固定路径**。用测试分发环境分别制造版本、平台、大小、SHA256 与实际产物不符，另用节点用户尝试写 `/usr/local/bin/nbpass-node`、更新器和暂存目录，以及提交带额外安装路径/命令字段的更新请求。通过：下载/自检校验失败不替换旧程序，普通节点用户写入被拒，更新服务拒绝非固定输入；不能以同样能启动的错版本程序通过。
- [ ] **D3 新版本启动失败**。内嵌专用启动失败候选，升级，记录切换前后旧程序摘要、systemd MainPID 和日志。通过：自动恢复旧二进制、重启旧服务并恢复业务，任务为回滚/失败而非就绪；节点用户仍可执行恢复后的 0755 程序。
- [ ] **D4 本地业务检查失败**。使用能启动但本地规则监听/检测监听/隧道授权握手无法就绪的候选，升级；同时保留一条可控业务检查。通过：3 分钟内未就绪后回滚，恢复旧程序与业务；不以陈旧 health.json 或旧缓存“成功”结果跳过自检。
- [ ] **D5 中断恢复**。分别在切换与回滚阶段对更新器 `systemctl kill --signal=KILL --kill-whom=main nbpass-update`，再重启更新服务。通过：残留安装事务被恢复、旧程序能重新运行；模拟旧服务第一次启动失败，再次恢复仍可成功，不丢事务。root 更新器不因节点重启一同退出。
- [ ] **D6 安全状态保留**。升级准备后停用测试规则，再触发失败回滚；此前完成一次测试换 IP 并记录动作 ID、额度和商家调用次数。通过：回滚不复活撤销规则，不清空动作去重/额度，不重复调用商家接口；只恢复程序，不整体还原节点数据目录。
- [ ] **D7 控制失联**。让新程序本地转发正常，在下载完成后阻断控制域名（保留网页源与业务网络）。通过：不因面板失联回滚，新程序和本地业务继续工作，恢复控制后正确确认。本基线票 34 保留“回连”；票 35 合入后才额外验收其 3 分钟“未确认”及实际产物核对，不能把未交付状态当成本票已通过。
- [ ] **D8 维护期**。升级中的活动入口断开控制连接，保留检测点可用。通过：10 分钟维护内不自动切换或换 IP；维护结束后重新观察完整 90 秒，用新检测证据与当前候选评估，不能直接使用维护内累积的离线时间或迟到失败。没有就绪候选保留最后解析。

## E. TLS、反向代理与长流

- [ ] **E1 两条链路**。运行 `nginx -t`、网页 `curl --fail` 和部署文档的 `openssl s_client ... -alpn h2`，执行真实节点兑换与控制连接。通过：网页、下载/安装、更新资源与 gRPC 均可达；控制域名使用正确 SNI/证书链、协商 h2，后端为 grpc_pass/h2c；远端不能直接访问 8080/8081。
- [ ] **E2 身份拒绝**。测试错误域名、过期/未知 CA 证书、缺失中间链，观察节点和更新器。专用 CA 正确安装后再试。通过：错误身份拒绝，无 `-k`、跳过校验或明文回退；正确 CA 下 curl、兑换、节点与更新器全部成功。
- [ ] **E3 HTTP/2 长流**。持续保持节点与正常业务至少 24 小时，每 5 分钟观察最近活性与代理/节点日志，特别覆盖 1 小时边界；期间浏览器读配置、另一个节点重连。通过：不因代理固定超时周期性假离线，活动双向流不被 keepalive_timeout 当作总寿命关闭，控制服务继续响应。非计划重连须记录原因，不能只凭最后仍在线判通过。
- [ ] **E4 可信代理与同源**。配置只信任实际网页代理地址，用两个来源尝试登录，来源 A 连续五次错密（第五次锁定 15 分钟），B 使用正确密码；从 A 伪造 X-Forwarded-For，并尝试跨源写设置。通过：伪造头不绕过锁定、不把所有用户混为代理来源，B 不受 A 失败预算影响，跨源写入拒绝。可用本机 reset-password 解除锁定。
- [ ] **E5 控制独立性**。停用所有测试入口转发规则、停止一个业务节点，仍从其他节点访问控制源并在面板管理。通过：面板网页、控制/票据与更新源不经过受管业务转发，其他节点正常；明文跨机时已限定可信网络和代理源防火墙。

## F. 文件权限与身份

- [ ] **F1 持久文件**。服务运行时检查（按是否存在跳过尚未生成的文件，但记录原因）：

  ```sh
  sudo stat -c '%U:%G %a %n' /etc/nbpass /etc/nbpass/panel.env /var/lib/nbpass /var/lib/nbpass/panel.*
  sudo find /var/lib/nbpass-node /var/lib/nbpass-update /usr/local/bin/.nbpass-update -printf '%u:%g %m %p\n'
  sudo stat -c '%U:%G %a %n' /usr/local/bin/nbpass /usr/local/bin/nbpass-node /usr/local/libexec/nbpass-update
  ```

  通过：逐项符合部署说明权限表，特别是运行中的 WAL/SHM、两份本机密钥、缓存及更新暂存；重启和重跑安装不放宽权限。
- [ ] **F2 运行用户与 socket**。`ps -eo user,group,pid,args`、`systemctl show -p User -p Group -p UMask nbpass nbpass-node nbpass-update`，检查两个 runtime 目录、健康记录、socket；用不在 nbpass-node 组的普通用户尝试连接 socket/读取缓存。通过：面板 nbpass、节点 nbpass-node、更新器 root，socket root:nbpass-node/0660，非授权用户不能访问；节点不能写程序/暂存；配置一条 80 或 443 端口规则时低端口能力有效。
- [ ] **F3 CLI 产物**。检查 backup 与 restore 后新目录身份/权限，尝试以无关普通用户读取密钥。通过：备份目录 0700、数据 0600、程序 0700，恢复目录 nbpass 可读写且其他用户不可读；程序、数据库和密钥配套保管，不出现在公开下载路径。

## G. 真实 Linux 指标与前端

- [ ] **G1 OS 指标对照**。节点页打开探针，分别用 `mpstat 5`/`top`、`free -b`、`df -B1 /`、`ip -s link` 相邻计数差/实际间隔、`ss -Htn state established | wc -l` 对照。通过：CPU、内存 total-available、根文件系统磁盘、非 lo 网卡收发速率和系统 TCP 符合采样口径（允许时刻差异）；CPU/速率首样本及读取失败显示缺测而非零；约 5 秒刷新，uptime 从节点进程启动计时。
- [ ] **G2 业务与曲线**。建立/关闭直连和隧道业务、控制连接，断控制网 3 分钟后恢复，再重启节点。通过：业务连接、物理隧道连接、系统 TCP 分开显示；控制连接不算业务。曲线分钟平均/最大值和鼠标读数可用，缺测不连线、不补零；仅断控制链路不重置 uptime，进程重启归零；离线历史探针不补报。
- [ ] **G3 页面功能与布局**。在桌面和窄屏依次检查首次设置、登录/退出/改密、安全设置、节点（版本/升级/进度/地区/维护）、入口组/出口组、转发规则（创建、启停、删除、JSON 导入导出及随机端口）、事件筛选/分页/缺口、探针、DNSPod/受管记录/解析监控、Telegram、换 IP 配置/动作/暂停，以及保留期设置。通过：列表、表单、单位、错误、加载失败重试、确认框和会话过期跳转可用；状态来自 API，秘密不回显，失败操作不显示成功。未交付的票功能明确记为未执行。
- [ ] **G4 保留与安全状态**。默认 90 天/24 小时，保存为事件 7 天/探针 2 小时，刷新和重启；用测试部署历史缩短保留期，随后延长。通过：范围校验为事件 1–3650 整天/探针 1–8760 整小时；历史逐批消失，延长不复活，近期数据保留。清理期间修改规则/设置与控制活性正常；动作、额度、冻结/暂停、版本和非零累计流量保持。探针图仍展示最近 24 小时，数据库文件大小不要求立刻缩小。

## H. 容量基线

先记录节点/目标/客户端 CPU、内存、网卡、RTT、文件描述符限制与资源参数；目标和客户端直连能力必须高于测试负载。直连和 WSS 隧道各执行一次（隧道使用独立入口/出口，避免自连）；压测路径只经过待验收转发规则。目标为私网时显式允许私网；不要把控制端点当业务压测目标。

- [ ] **H1 1000 并发及 24 小时长连接**。在可控目标运行下列 echo 服务，创建一条指向目标 IPv4:19090 的测试规则；压测客户端把示例 IP/端口替换为真实入口地址和监听端口。两边先 `ulimit -n 65536`（必要时由管理员提高 hard limit）。

  目标终端：

  ```sh
  python3 - <<'PY'
  import asyncio
  async def echo(reader, writer):
      try:
          while data := await reader.read(65536):
              writer.write(data)
              await writer.drain()
      finally:
          writer.close()
          await writer.wait_closed()
  async def main():
      server = await asyncio.start_server(echo, '0.0.0.0', 19090)
      async with server:
          await server.serve_forever()
  asyncio.run(main())
  PY
  ```

  客户端终端（Python 3.8+；约 30 秒一次有载心跳，避免默认 2 小时业务空闲期限）：

  ```sh
  python3 - 203.0.113.10 19090 <<'PY'
  import asyncio, sys, time
  host, port = sys.argv[1], int(sys.argv[2])
  async def main():
      gate = asyncio.Semaphore(32)
      connections = []
      async def connect():
          async with gate:
              reader, writer = await asyncio.wait_for(asyncio.open_connection(host, port), 15)
              connections.append((reader, writer))
              # TCP accept 不代表目标拨号/隧道握手已完成；先确认业务就绪，
              # 再释放并发预算，避免把并发连接测试变成待拨号洪峰测试。
              probe = b'nbpass-ready\n'
              writer.write(probe)
              await writer.drain()
              assert await asyncio.wait_for(reader.readexactly(len(probe)), 15) == probe
      try:
          await asyncio.gather(*(connect() for _ in range(1000)))
          print('1000 connections established', flush=True)
          deadline = time.monotonic() + 86400
          rounds = 0
          while time.monotonic() < deadline:
              async def exchange(i, reader, writer):
                  data = ('%d:%d\n' % (rounds, i)).encode()
                  writer.write(data)
                  await writer.drain()
                  actual = await asyncio.wait_for(reader.readexactly(len(data)), 15)
                  assert actual == data, (i, actual)
              await asyncio.gather(*(exchange(i, r, w) for i, (r, w) in enumerate(connections)))
              rounds += 1
              if rounds % 10 == 0:
                  print('alive:', len(connections), 'rounds:', rounds, flush=True)
              await asyncio.sleep(30)
          print('PASS: 1000 connections survived 24 hours', flush=True)
      finally:
          for _, writer in connections:
              writer.close()
          await asyncio.gather(*(w.wait_closed() for _, w in connections), return_exceptions=True)
  asyncio.run(main())
  PY
  ```

  通过：1000 条连接同时建立并保持至少 24 小时，不重连、不丢失或串线，心跳逐一正确；探针业务数符合 1000，控制持续响应，内存/FD 不持续泄漏。若提前异常退出即失败，保留终端错误与持续时长。该项验证有载长连接，不承诺默认空闲超时下的 24 小时完全静默连接。
- [ ] **H2 持续 100 Mbps**。目标运行 `iperf3 -s -p 19201`，单独建规则指向该端口；客户端执行 `iperf3 -c 入口IPv4 -p 规则端口 -P 8 -t 600 -i 10`，再加 `-R` 测反向。通过：每方向接收端平均至少 100 Mbit/s，持续 10 分钟，无异常断流；记录双方 CPU/内存/丢包/重传，保持网页读 API 与节点活性正常。此命令 8 条并发未设速率上限，需预留测试带宽；不以客户端发送速率代替接收吞吐。
- [ ] **H3 达到资源上限**。在隔离节点 drop-in 重设 ExecStart，在原控制地址/数据目录参数外加 `-max-connections 32 -max-connections-per-rule 32 -max-pending-dials 8`，重启；把 H1 客户端的 Semaphore 改为 4、连接数改为 32，先保持 32 条已就绪连接，再用另一客户端尝试第 33 条。另将测试目标设置为连接黑洞以占满待拨号预算，再恢复。通过：已接纳连接可继续收发，超额连接很快被拒/关闭（TCP accept 本身可能成功，必须尝试业务往返），预算释放后新连接恢复；面板节点活性持续更新、配置仍可下发、无 OOM 或 FD 泄漏。隧道还测试 `-tunnel-max-streams` 限制，避免只测入口而遗漏出口准入。结束后还原 drop-in 与测试防火墙。

完成后由票 40 汇总每个编号的结果与未通过原因；下一步才是生产部署验收放行。
