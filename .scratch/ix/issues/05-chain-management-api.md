# 05 转发链持久化、管理 API 与全路径静态校验

Status: resolved
Blocked by: 03；04 仅阻塞迁移 50 注册和最终升级验收
Migration: 50（`forwardChainsMigration`；注册前必须已有 04 的 49）
Parallel: 领域模型、新 API 及测试可在 03 后独立推进，与 04 并行；迁移注册按 04→05 串行交接。完成本票后 06a 和 07 CRUD 可并行。

## 目标

通过 HTTP 保存可复用的有序端点组路径，完成新建/编辑/删除、路径预览与静态防环。独立交付可持久化的链管理能力，不提前下发尚未实现的多跳业务配置。

## 具体改动点

- 新建 `internal/forwarding/chains.go` 定义 `ForwardChain{ID,Name,ExitGroupIDs}` 和有序路径展开/重复节点校验；新建 `internal/panel/forward_chains_migration.go` 定义迁移 50：`forward_chains(id TEXT PRIMARY KEY,name TEXT NOT NULL)`；`forward_chain_hops(chain_id REFERENCES forward_chains(id) ON DELETE CASCADE,position INTEGER,exit_group_id REFERENCES exit_groups(id))`，主键 `(chain_id,position)`，组引用默认 RESTRICT。不允许组删除级联截短链。
- 新建 `internal/panel/forward_chains_api.go`，在 `tunnelRoutes` 注册：`GET /api/forward-chains` 返回 `{chains:[...]}`；`POST /api/forward-chains`、`PUT /api/forward-chains/{id}` 输入 `{name,exit_group_ids:[id1,id2,...]}`，返回保存对象（201/200）；`DELETE` 成功 204，不存在 404。写入复用 `mutateForwarding`，认证、同源限制、严格 JSON 与现有 API 一致。
- 链对象公开字段固定为 `id,name,exit_group_ids,rule_count,hops`；`hops` 每项 `{position,exit_group_id,name,members,observations}`，position 从 1 起，members 为 `[{node_id,node_name,address_id,address,port}]`，port 为有效拨号端口；observations 合约为 `[{from_node_id,to_node_id,address_id,endpoint_config_digest,chain_fingerprint,rule_id,generation,state,fresh,sampled_at}]`。同链可由不同规则授权，不能混用 grant 代次；端点摘要沿用 02/03。本票 rule_count 为真实 0、observations 为空；06a 接引用、06c 接真实逐边证据，不造样本。
- 名称去空白且 1–200 字节；至少两组，组必须存在并非空；展开每组的所有成员，同节点跨任何两跳重复均拒绝，不能只校验主成员；重复组自然也拒绝。依赖 03 的地址引用与有效端口，不按 IP 相同就认作同节点。
- `internal/panel/config_validation.go` / 新链校验文件按变更影响范围校验链：新建/编辑链及端点组成员变更时检查受影响链，即使链尚未绑定规则也不得造成跨跳重复。本票同时落实单跳规则（含入口组）的受影响范围校验，06a 再扩到链引用规则及全部入口集合；不要把新增重复节点校验塞入每次全量编译，历史规则不能阻断无关保存/重连/授权确认，不写可被误认为可用多跳的数据面占位实现。
- `tunnel_api.go:deleteExitGroup` 增加来自 forward_chain_hops 的引用保护，409 指出引用链；`node_references.go` 在现有端点组引用基础上补充间接链名称，避免用户看不到删节点的原因。链删除在本票只有无规则引用的情形，06a 再加入规则引用检查，任何接口不能级联删业务规则。
- API 路径展开统一函数供 06a 的编译/校验和 07/08 查询复用；定义「变更对象→受影响链/规则」接缝，覆盖组、链和入口组的直接/间接引用。稳定顺序，读取不推进 revision，不把凭据或私钥放入预览。

## 迁移与兼容要求

- 本票独占 50，04 独占 49；仅在 49 注册后追加，不插空迁移、不重排 1–49。并行阶段可以完成新文件与模型测试，注册和最终升级测试必须等待 04。
- 新表为空，旧规则/组/节点配置内容不变；保存尚未被引用的链不应给节点制造配置版本变化。保持规则、流量、授权数据原状。
- 不新增 protobuf 字段、不提前声明 forward_chains 能力；本票 API 能管理链不等于节点已经能运行链。

## 测试要求

- 单元：迁移 49→50、新库、FK/级联边界；名称/跳数/未知组/重复组/备用成员重复/顺序；失败整笔回滚，组编辑导致已有链失效被拒；两个不同节点使用相同地址不误判重复。
- 单元/集成：在含历史重复节点单跳规则的基线上，新建无关链/保存无关规则、节点重连、授权确认仍可全量编译；改到该规则或其引用组时才按新标准拒绝，事务不留半状态。06a 补链式与入口组影响范围验收。
- clusterharness 新建 `forward_chains_api_test.go`：经 HTTP 创建、重排、重命名、回读、面板重启、删除链；删除引用组被拒；修改组备用成员造成环路被拒且链/组保持原值；未引用链不改变现有直连/单跳业务和 revision。无权/跨源管理请求拒绝。数据面仍使用真实现有节点与旧规则证明隔离。
- 运行 `go test ./internal/forwarding ./internal/panel -timeout=10m`、`go test ./internal/clusterharness -run 'Test.*ForwardChainsAPI' -timeout=10m`。

## 验收标准

- [x] 有序链可独立管理并持久化，预览显示所有候选与所选地址。
- [x] 所有成员参与静态防环，引用组不可删除；失败操作不留下半条链。
- [x] 旧规则运行不变，未引用链不下发任何多跳配置。
- [x] 06a/06c/07 使用固定 API 与路径展开形状；新增校验只针对受影响对象，不阻塞历史规则的无关编译。

## 不做的事

不把链关联到端口、不实现节点多跳或 UI（分别由 06a–06c/07/08 负责）；不做智能选择、固定跳数、服务器级默认传输、组间 fallback 或更改已有主备策略。


## Comments

### 实施内容与技术取舍（2026-11-12，等待独立审查）

- 在既有 49 后仅追加迁移 **50**：链名称表、带从 1 起顺序的跳表，链删除只级联自己的跳，端点组外键明确 `ON DELETE RESTRICT`，另建组引用索引。49→50 保留旧节点名称、出站 IP、revision、规则与组，新表为空；新库/API、位置主键、未知链/未知组 FK、链删除清理跳和组删除保护均有覆盖。不占 51、不修改 protobuf、缓存或节点能力声明。
- `forwarding.ForwardChain`、`ChainGroup/ChainMember/ChainHop/ChainObservation` 与 `ExpandForwardChain` 提供 06a/06c/07/08 共用的有序路径形状；`ValidatePathNodes` 按服务器身份展开全部主备候选，不按 IP 判重。管理 API 固定只公开 `id,name,exit_group_ids,rule_count,hops`，成员带服务器/所选地址身份、名称及有效拨号端口；`rule_count` 为当前确无规则引用的真实 0，`observations` 为真实空数组，不生成健康样本、授权或多跳数据面配置。
- POST/PUT/DELETE 都复用 `mutateForwarding`，GET 用一致的只读事务并在写响应前释放连接；名称去空白后 1–200 字节，至少两组，未知/空组、重复组及任何跨跳备用重复拒绝。读列表按链 ID、路径按 position、成员按既有组内 position 稳定排序。预览 SQL 只读公开拨号字段，无法带出私钥、节点对凭据或 grant。
- `pathChanges → affectedPaths → validatePathChanges` 定义链、端点组、入口组、规则的变更依赖接缝；本票落实端点组→链/单跳规则、入口组→其单跳规则、链自身及规则自身，06a 增加链引用规则。组变更在替换成员后、事务提交前验证全部受影响对象（含停用规则和备用成员）。`storeRule`、导入 preflight/commit 与批量启停均不能绕过单跳路径校验；删除规则可移除历史无效路径。
- 新路径重复校验没有进入 `compileSnapshots` 或原有全量监听/目标回环检查。真实历史基线既覆盖停用重复单跳，也覆盖数据库中原有的启用自跳：无关链/规则保存、无关批量/导入、节点重连、持久授权确认及面板重启仍可全量编译；改到该规则或引用组才拒绝，失败不留下半状态。注入第二跳 INSERT 存储故障后，创建没有半条链，编辑保留原名称与完整旧顺序。
- 端点组删除 409 指明引用链名称；节点引用投影增加间接 `forward_chain` 与链名称，节点删除错误也显示转发链。现有服务器删除弹窗会显示该引用名称；转发链界面及引用类型标签的前端衔接留 07/08，本票没有 web 改动。
- 两个既有集成夹具此前新建入口=出口，与本票新保存标准冲突：`TestTunnelManagementValidationAndGates` 改为独立入口；`TestCombinedEntryExitCountsBusinessOnce` 保留新自跳保存 409，再用 A 直连与 B→A 单跳同时运行，让兼任两种角色的 A 精确计 2 条、B 精确计 1 条真实业务连接。历史自跳兼容另在本票集成中注入旧库并保留真实业务断言，不放开新保存。04 迁移测试最终版本断言从固定 49 改为 `len(migrations)`，仍核对 48 升级后旧节点的名称、revision 与空源字段。

### 红绿证据及本轮验证命令

以下除明确写墙钟外均为 Go 输出的包内耗时；失败均保留原场景并修正实现或夹具，没有删测试绕过。

| 命令 | 退出码与耗时 | 结果 |
|---|---|---|
| `go test ./internal/panel -run 'TestForwardChains' -count=1 -timeout=2m` | 1；panel 1.824 秒 | 必要红证据：新 API 404、迁移仍 49、新重复单跳保存仍 201。 |
| `go test ./internal/forwarding -run TestExpandForwardChain -count=1 -timeout=2m` | 1；墙钟约 0.087 秒 | 新领域模型与共享展开函数尚不存在，编译明确失败。 |
| `go test ./internal/forwarding ./internal/panel -run 'Test(ExpandForwardChain\|ForwardChains)' -count=1 -timeout=2m`（实现后第一轮） | 1；forwarding 1.164、panel 2.221 秒 | 领域断言通过；旧通用 API 夹具把成功 DELETE 204 当 JSON 解析而失败，换为本票支持空响应的测试辅助函数。 |
| 同上（辅助函数修正后） | 0；forwarding 1.019、panel 1.940 秒 | 路径、迁移、管理、回滚及历史规则定向通过。 |
| `go test ./internal/clusterharness -run 'Test.*ForwardChainsAPI' -count=1 -timeout=3m -v`（第一轮） | 1；6.204 秒 | 另建 socket 预留池没有交给已启动节点，真实监听报端口占用；改用测试台已有监听器移交。 |
| 同上（第二轮） | 1；6.365 秒 | 链操作、业务和版本断言已通过；面板重启后节点在 fake clock 重连退避中，补实际收发间隔与时钟推进。 |
| `go test ./internal/forwarding ./internal/panel -timeout=10m`（第一轮） | 1；forwarding 11.724、panel 49.839 秒 | 唯一失败是旧 04 迁移测试固定期望最终版本 49；50 已正确追加，改为当前迁移数。 |
| `go test ./internal/clusterharness -run 'Test(TunnelManagementValidationAndGates\|CombinedEntryExitCountsBusinessOnce)' -count=1 -timeout=2m` | 1；0.798 秒 | 两个旧夹具均实证新自跳保存被 409 拒绝，按上节改合法路径并保留拒绝断言。 |
| `go test ./internal/clusterharness -run 'Test(.*ForwardChainsAPI\|TunnelManagementValidationAndGates\|CombinedEntryExitCountsBusinessOnce)' -count=1 -timeout=3m -v` | 0；1.841 秒 | 链真实 HTTP/业务/重启/安全与两项既有回归全部通过。 |
| `go test ./internal/panel -run 'Test(ForwardChains\|OutboundIPv4Migration)' -count=1 -timeout=2m`（扩大边界后第一轮） | 1；1.850 秒 | 无关导入实际成功返回既有合约 200，夹具误期望 201；校正期望，未更改导入 API。 |
| 同上（最终） | 0；1.888 秒，墙钟 3.22 秒 | 增补第二跳写入故障、200 字节边界、节点间接引用、旧数据与无关批量/导入全部通过。 |
| `go test ./internal/forwarding ./internal/panel -timeout=10m`（最终） | **0**；forwarding 有效缓存（此前完整 11.724 秒且随后未改）、panel **46.637 秒**；墙钟 **47.21 秒** | 本票要求的两个完整包通过。 |
| `go test ./internal/clusterharness -run 'Test.*ForwardChainsAPI' -timeout=10m`（最终） | **0**；**1.384 秒**；墙钟 **3.38 秒** | 真实 HTTP、当前旧单跳 WSS/yamux/TCP、历史启用自跳重连/授权确认、专线有效端口预览、链 CRUD/revision 隔离、重启持久化、无权和跨源拒绝通过。 |

- 最终 `git diff --check` 退出 **0**，墙钟 **0.02 秒**；此前自查两次也退出 0。只读检查未发现 protobuf、数据面或能力声明的越界改动。

### 审查交接与边界

- 自查已按原票逐项核对；当前保持 **claimed**，尚未 commit 或标记 resolved，等待主编排者安排的独立只读审查及批准。正式提交 SHA 在批准后的原 Comments 追加；所有 `.md` 不进入功能提交。
- 没有运行本票不要求的全仓 `make test`、前端构建或浏览器，没有生成 dist/nodebundle、额外报告、日志或方案；最终全仓及发行产物留 09。未覆盖 `bin/nbpass-node-ix02` 和 `-linux-amd64`，`git status --short web/dist internal/nodebundle bin` 为空。
- 始终在指定目录与 `feat/ix`、基线 `3f3407fe90dbe58e4955663a777a0eb0e047b9c9` 上实施；没有工作树、切分支、push/deploy/merge。只改原 05 Comments，02/03/04/09 原有未提交文档完整保留。


### 独立审查与功能提交

- 主编排者安排的独立只读 `review_05` 已正式批准并复核最新 diff 与本票 Comments：**Spec 符合 / Quality 通过，Critical 0 / Important 0 / Minor 0**。本票自动化验收勾选，Status 标记 **resolved**。
- 主编排者明确授权后完成中文功能提交：**`ec59ecbc12064e23618ed5c1d560a9256c25a285`**「支持转发链管理与受影响路径静态校验」，仅含 15 个 05 源码/测试文件。提交前工作区与暂存区 `git diff --check` 均退出 0；没有重复已通过且未发生新源码变更的测试。
- `.md`、dist/nodebundle/bin 均未暂存或纳入功能提交；02/03/04/09 未提交文档保留，05 文档同样留待本轮统一中文文档提交。无 push/deploy/merge，未开始 06a。
