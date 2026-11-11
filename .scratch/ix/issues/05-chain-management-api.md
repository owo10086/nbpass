# 05 转发链持久化、管理 API 与全路径静态校验

Status: ready-for-agent
Blocked by: 03, 04
Migration: 50（`forwardChainsMigration`；注册前必须已有 04 的 49）
Parallel: 领域模型、新 API 及测试可与 04 并行；唯一重叠的迁移注册按 04→05 串行交接。完成本票后 06 和 07 可并行。

## 目标

通过 HTTP 保存可复用的有序端点组路径，完成新建/编辑/删除、路径预览与静态防环。独立交付可持久化的链管理能力，不提前下发尚未实现的多跳业务配置。

## 具体改动点

- 新建 `internal/forwarding/chains.go` 定义 `ForwardChain{ID,Name,ExitGroupIDs}` 和有序路径展开/重复节点校验；新建 `internal/panel/forward_chains_migration.go` 定义迁移 50：`forward_chains(id TEXT PRIMARY KEY,name TEXT NOT NULL)`；`forward_chain_hops(chain_id REFERENCES forward_chains(id) ON DELETE CASCADE,position INTEGER,exit_group_id REFERENCES exit_groups(id))`，主键 `(chain_id,position)`，组引用默认 RESTRICT。不允许组删除级联截短链。
- 新建 `internal/panel/forward_chains_api.go`，在 `tunnelRoutes` 注册：`GET /api/forward-chains` 返回 `{chains:[...]}`；`POST /api/forward-chains`、`PUT /api/forward-chains/{id}` 输入 `{name,exit_group_ids:[id1,id2,...]}`，返回保存对象（201/200）；`DELETE` 成功 204，不存在 404。写入复用 `mutateForwarding`，认证、同源限制、严格 JSON 与现有 API 一致。
- 链对象公开字段固定为 `id,name,exit_group_ids,rule_count,hops`；`hops` 每项 `{position,exit_group_id,name,members,observations}`，position 从 1 起，members 为 `[{node_id,node_name,address_id,address,port}]`，port 为已解析的有效拨号端口；observations 为 `[{from_node_id,to_node_id,address_id,state,fresh,sampled_at}]`。本票尚无链式规则，因此 rule_count 为真实 0，observations 为空并由消费者显示待观测；06 接入真实引用及证据，不造健康样本。
- 名称去空白且 1–200 字节；至少两组，组必须存在并非空；展开每组的所有成员，同节点跨任何两跳重复均拒绝，不能只校验主成员；重复组自然也拒绝。依赖 03 的地址引用与有效端口，不按 IP 相同就认作同节点。
- `internal/panel/config_validation.go` / 新链校验文件接入所有链的结构校验：端点组成员变更即使链尚未绑定规则，也不得造成跨跳重复节点。06 将加入实际入口集合与所有引用规则的校验；不要在本票编写会被当作可用多跳的数据面占位实现。
- `tunnel_api.go:deleteExitGroup` 增加来自 forward_chain_hops 的引用保护，409 指出引用链；`node_references.go` 在现有端点组引用基础上补充间接链名称，避免用户看不到删节点的原因。链删除在本票只有无规则引用的情形，06 再加入规则引用检查，任何接口不能级联删业务规则。
- API 路径展开统一函数供 06 的编译/校验和 07/08 的 UI 查询复用；稳定顺序，读取不推进 revision，不把凭据或私钥放入路径预览。

## 迁移与兼容要求

- 本票独占 50，04 独占 49；仅在 49 注册后追加，不插空迁移、不重排 1–49。并行阶段可以完成新文件与模型测试，注册和最终升级测试必须等待 04。
- 新表为空，旧规则/组/节点配置内容不变；保存尚未被引用的链不应给节点制造配置版本变化。保持规则、流量、授权数据原状。
- 不新增 protobuf 字段、不提前声明 forward_chains 能力；本票 API 能管理链不等于节点已经能运行链。

## 测试要求

- 单元：迁移 49→50、新库、FK/级联边界；名称/跳数/未知组/重复组/备用成员重复/顺序；失败整笔回滚，组编辑导致已有链失效被拒；两个不同节点使用相同地址不误判重复。
- clusterharness 新建 `forward_chains_api_test.go`：经 HTTP 创建、重排、重命名、回读、面板重启、删除链；删除引用组被拒；修改组备用成员造成环路被拒且链/组保持原值；未引用链不改变现有直连/单跳业务和 revision。无权/跨源管理请求拒绝。数据面仍使用真实现有节点与旧规则证明隔离。
- 运行 `go test ./internal/forwarding ./internal/panel -timeout=10m`、`go test ./internal/clusterharness -run 'Test.*ForwardChainsAPI' -timeout=10m`。

## 验收标准

- [ ] 有序链可独立管理并持久化，预览显示所有候选与所选地址。
- [ ] 所有成员参与静态防环，引用组不可删除；失败操作不留下半条链。
- [ ] 旧规则运行不变，未引用链不下发任何多跳配置。
- [ ] 06/07 使用本票固定的 API 及路径展开形状，无需各自重复建模。

## 不做的事

不把链关联到端口、不实现节点多跳或 UI（分别由 06/07/08 负责）；不做智能选择、固定跳数、服务器级默认传输、组间 fallback 或更改已有主备策略。
