# 07 转发链管理页与逐跳路径预览

Status: ready-for-agent
Blocked by: 05
Migration: 无（只消费 05 的 50；不新增迁移）
Parallel: 可与 06 并行；本票独占新 Chains 前端、main.tsx 的 chains 路由替换，06 独占后端。真实健康端到端验收在 06 完成后补齐，不用假数据占位。

## 目标

把 `#/chains` 的规划中页面替换为可操作的链管理页，按有序端点组表达每跳路径，支持新增/编辑/重排/删除和引用、健康信息展示。

## 具体改动点

- 新建 `web/src/Chains.tsx`、`ChainsData.ts`、`Chains.css`，按 05 的 `/api/forward-chains` API 读写，复用现有 `/exit-groups`、`/nodes`、`/tunnel-exits` 的公开信息；优先用服务端 hops 预览，不能根据服务器在线状态自己推算链健康。
- `main.tsx` 引入 Chains 并替换 `page === 'chains'` 的 PlannedPage 分支；`route.ts` 已有 chains/pageTitles，不新建第二个 hash，也不改变旧 rules/nodes/exit-groups 别名和 focus 语义。支持 `#/chains?focus=<id>` 定位。
- 列表显示名称、有序组路径、引用端口数、各跳候选与所选地址、各跳真实观测；新建/编辑为名称 + 按顺序添加端点组，可上下移动、移除，至少两组；展示组内主备顺序。409 原因保留在当前表单，不能清空输入或把重复节点错误吞成「保存失败」。
- 引用数为真实 rule_count，删除 409 显示引用原因；编辑说明影响所有引用端口，保存后重新读取。健康 observations 为空显示待观测，fresh=false 显示历史；复用 `EndpointData.healthStatus/healthIsFresh` 的 10 秒到期规则，展示观测发起者和目标地址，不把「链上有一个节点在线」当整链健康。
- 使用 DESIGN.md 的 PageHeader、FilterCard、TableCard、FormSection、StatusDot、ColorTag、深色 token；搜索/分页、长地址/域名、窄屏、空列表、加载错误、401/428 处理与既有页面一致。页面专属 CSS 不重建主题。

## 迁移与兼容要求

- 无迁移、不改后端协议。依赖 05 即可完成 CRUD 与无观测状态；06 填充真实引用/观测后，既定字段可直接显示。并行时只按已写定的 API 合约开发，不向后端塞演示字段。
- 不提前替换端口模式表单；此页链可以尚未被端口引用。旧书签、其他规划中页面和现有端点组管理不受影响。

## 测试要求

- 前端单元新建 `ChainsData.test.mjs`：顺序增删/上下移边界、至少两组、重复选择、防丢 ID、请求序列化、路径标签、未知/历史健康和 10 秒到期；补 `web/route.test.mjs` 的 chains/focus。运行 `npm --prefix web test -- src/ChainsData.test.mjs`、`npm --prefix web run build`。
- clusterharness：消费 05 的 `Test.*ForwardChainsAPI` 场景证明页面使用的 API 有真实存储与拒绝语义；06 完成后运行其真实链和主备场景，核对 GET 响应能提供引用数与逐跳观测（必要时补 API 契约断言到相关测试）。不为只改前端复制一套假的 Go 数据面测试。
- 页面人工验收交由编排者在最终收尾核对：新增/编辑/移动/删除、被引用删除、长名称/地址、窄屏、会话失效、实时转历史；本次拆票任务不打开浏览器。

## 验收标准

- [ ] chains 路由有真实 CRUD，路径顺序和服务器地址清楚可见。
- [ ] 至少两组、错误反馈、编辑影响范围、引用删除保护在页面可理解。
- [ ] 无证据显示待观测，有证据区分各边及历史，不生成假健康。
- [ ] 构建与前端测试通过；06 后的真实 API 数据可直接显示。

## 不做的事

不实现后端/多跳数据面或端口链式模式（08），不添加智能跳数、拖拽拓扑画布、默认传输开关；不改 DESIGN/GLOSSARY，不提交 web/dist。
