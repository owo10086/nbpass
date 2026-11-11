# 05 服务器组管理页（原入口组）

Status: resolved
Blocked by: 01

把 `EntryGroups.tsx` 及入口组子组件（`EntryMembers`、`EntryReadiness`、`EntrySwitch`、`EntryBlockedPolicy`、`EntryDomainWatch`、`AllPublishDNS`、`ManagedDNS`、`DNSRecordPicker`）改成原型 `pages/servers.js` 服务器组列表/表单/「DNS 状态」弹窗的样子，路由 `#/server-groups`。

- 列取能映射的：ID、名称/入口（受管域名）、发布模式（主备 / 全部发布）、服务器数、DNS 自动切换状态、仅观测、操作。
- 表单：区块化；服务器成员用两栏穿梭。**发布模式建组时选、之后不可改（ADR 0007）**；受管记录/CNAME 行为按 ADR 0008，不删远端记录。
- 「DNS 状态」弹窗承载现有就绪度、切换、摘除/加回、最少保留、观测等。
- 文案统一新叫法。页面专属样式放独立 CSS；复用 01 积木；不重建 `web/dist`。

## Comments

- 交付内容：服务器组列表改用深色橙色主题与 `PageHeader`、`FilterCard`、`TableCard`、`StatusDot`、`ColorTag`；增加名称 / 域名 / 服务器 / ID 搜索、发布模式与仅观测筛选、分页，保留自定义拖动排序、列排序与旧链接 `focus` 定位。主备受管域名从现有绑定接口读取，读取失败单独显示。
- 编辑器采用 `FormSection` 分区与两栏成员穿梭，两侧可搜索，保留 1–64 个成员限制和上移 / 下移顺序；新成员追加到末尾，避免改变首选入口。全部发布的已绑定成员不能从穿梭框直接移出，仍须走暂停、确认与解绑流程。发布模式创建时选择，编辑时禁用且不提交该字段。
- 现有功能位置：
  - 名称、成员与优先级：行操作「编辑」。
  - 成员控制连接、就绪度、候选资格、国内证据、观测动作：行操作「DNS 状态 → 健康与观测」。
  - DNSPod 账号添加、域名 / 主机记录选择、新建暂停记录、已有记录接管、逐条确认改类型、首次发布：行操作「DNS 状态 → 受管 DNS 与发布」。
  - 主备的手动切换、暂停后恢复自动回切、被墙处理策略、发布动作与错误：同一「受管 DNS 与发布」页签。
  - 全部发布的记录绑定 / 更换、摘除 / 加回 / 移出：同一页签的成员记录行；最少保留与实际保留数在「自动摘除与加回」区块。
  - 域名解析、传播进度、路径与采样详情：同一页签的「域名解析与传播观察」。
  - 成员配置同步与逐端口生效版本、错误：行操作「DNS 状态 → 端口配置同步」。
  - 仅观测：列表开关与 DNS 状态弹窗顶部开关，保留自动动作抑制、手动操作照常执行和已下发换 IP 继续核对的语义。页签切换保留未提交表单。
- 与原型的有意差异：不显示后端没有的端口区间、描述、自动加入模板、入站策略、互测阈值、脚本链接及演示健康数据；DNS 自动切换列使用真实发布阶段，不增加无对应 API 的启停开关。创建组后再绑定 DNS / 首次发布，避免把本地表单保存混同外部 DNS 写入。DNS 状态按真实功能分为三个页签，保留实际就绪条件和错误原因。成员顺序用上移 / 下移明确调整；DNS 发布 TTL 沿用后端 60 秒。主备支持 A / CNAME，全部发布只支持 A；CNAME 目标必须是活动服务器已发布、启用且地址一致的受管记录。更换、解绑与删除组均不删除远端记录。
- 共享范围：未改外壳、路由、主题或 01 共用积木；仅调整票内页面及子组件。`ManagedDNS` 新增可选嵌入与刷新参数，保留原调用方式；`DNSRecordPicker` 的 props 与 `useRecordBinding` 返回接口不变，节点页仍可复用。专属样式集中在 `web/src/EntryGroups.css`。
- 验证：前端生产构建通过；路由与成员顺序测试共 7 项通过；只读代码审查未发现严重或重要问题。首轮 `make test` 中 `TestAllPublishSetWatchAndPerRecordStatusAudit` 未及时确认，`clusterharness` 随后达到 10 分钟默认超时；该用例单独复测通过。第二轮完整测试中 `TestAllPublishRejectedRecordStillReadsBackManualRepair` 与 `TestEntryBlockedRotationNoImprovementRetriesAndPauses` 失败，两项随后单独复测均通过（合计约 37 秒）；其余 Go 包通过。两次扩展时限的完整复测均使用 `GOMAXPROCS=2 GOFLAGS='-timeout=30m -p=1' make test`。第三轮完整测试仅 `TestRotationSharedQuotaBlocksAutomaticUntilRollingDayExpires` 等待节点连接状态超时（`clusterharness` 耗时约 756 秒），该用例随后单独复测通过（约 3 秒），其余 Go 包通过。**三轮完整 `make test` 均未全绿，不能宣称全量验收通过。** 各轮失败点不同且都能单独复测通过，存在时序不稳定迹象；根因尚未确定，按本票范围未修改 Go 代码或测试断言。
- 已知问题 / 验收边界：全量 Go 集成测试未通过是当前验证阻碍，详见上项；前端实现按本票交付，仍需后续处理集成测试稳定性。构建保留 AntD 依赖块超过 500 kB 的体积提示；浏览器视觉与交互验收按用户安排由用户完成。未改 Go 后端或 API。

- 构建产物：已执行 `git checkout -- web/dist && git clean -fdq web/dist` 还原，未提交 `web/dist`；节点打包文件无待提交变化。
