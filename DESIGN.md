# nbpass 设计系统

本面板沿用 Zero Forwarder 原型的橙色视觉，支持日间、夜间和跟随系统，用 React + Ant Design 6 实现。基础需求来源是 `.scratch/zf-ui/spec.md`，主题扩展见 `.scratch/day-mode/spec.md`；原型仅供视觉与交互参考，不代表后端能力。现有功能继续保留，各业务页面复用以下共用组件。

## Token 与主题

**颜色值的唯一可执行来源是 `web/src/theme.ts`。** 日间与夜间配色共用相同的 token 名称；初次挂载 React 前应用生效主题，切换时同步更新根节点 CSS 变量和 `ConfigProvider`，日间采用默认算法、夜间采用深色算法。新增或调整 token 只改该文件，业务 CSS 和 SVG 通过 `var(--变量名)` 引用，禁止另建一套颜色常量。以下表格是夜间配色的阅读用说明。

日间采用浅灰页面底色、白色卡片与侧栏、深色文字，保留橙色强调色，语义色需适配浅色背景。主题偏好为「日间 / 夜间 / 跟随系统」，首次默认跟随系统；跟随系统实时响应设备外观，不按时间切换。偏好按浏览器保存，刷新、重新打开及退出登录后保留，同一站点的多个标签页同步。主题切换不重新挂载业务页面，不清空表单或关闭业务弹窗。原生控件的 `color-scheme` 和浏览器 `theme-color` 随生效主题更新。

| Token / CSS 变量 | 值 | 用途 |
|---|---|---|
| `bg` | `#04060a` | 页面底色 |
| `bg-elev` | `#0b0f17` | 卡片、表格、输入框、浮层 |
| `bg-sunken` | `#070a10` | 侧栏、内嵌区 |
| `bg-hover` / `bg-head` | `#161c27` / `#1a2130` | 悬停 / 表头 |
| `line` / `line-strong` | `#252c39` / `#343c4b` | 分隔线 / 控件边框 |
| `text` / `text-2` / `text-3` | `#e7e9ee` / `#b5bcc8` / `#7d8696` | 正文 / 次级说明 / 弱化信息 |
| `accent` / `accent-hover` | `#ff6a13` / `#ff8540` | 唯一橙色强调色及悬停色 |
| `accent-soft` | `rgba(255,106,19,.14)` | 行定位、批量操作底色 |
| `ok` / `warn` / `danger` / `info` | `#52c41a` / `#f0b44c` / `#ff4d4f` / `#4c8dff` | 成功 / 警告 / 错误 / 信息 |
| `radius` / `radius-lg` / `radius-sm` | 6 / 8 / 4px | 控件 / 卡片 / 标签 |
| `side` / `topbar-height` | 224 / 56px | 侧栏宽 / 顶栏高 |
| `space-xs` / `space-sm` / `space-md` / `space-lg` / `space-xl` | 8 / 12 / 16 / 24 / 32px | 间距刻度 |
| `font-title` / `font-section` / `font-size` / `font-description` / `font-small` | 24 / 16 / 14 / 13 / 12px | 页标题 / 表单区块 / 正文 / 说明 / 标签 |
| `control-height` | 32px | 按钮与输入框高度 |

界面使用系统中文无衬线字体；IP、端口、版本、命令使用 `--mono`。数字使用 `font-variant-numeric: tabular-nums`（`.num`、表格和统计值已统一设置）。`--bg-subtle`、`--bg-sidebar` 是旧页面兼容别名，都引用 `--bg-sunken`。

## 外壳与导航

`AppShell.tsx` 持有导航、账户菜单和响应式布局，接收 `page`、`counts`、`busy`、`onLogout(all?)` 和页面 `children`。侧栏品牌为 nbpass，概览是顶层单项，其余按规格固定顺序分为「端口管理」「监控告警」「系统设置」，组标题可用鼠标或键盘折叠。进入其他页面时展开其所在组，当前项用橙底白字标识，数量来自现有概览接口。

顶栏显示「分组 / 页面」面包屑，右侧提供独立主题菜单，账户菜单保留退出登录、退出所有会话和会话期限说明。登录与首次设置页右上角也提供同一主题入口，桌面和手机均可操作。内容区桌面内边距为 `20px 24px 48px`。≤860px 时侧栏变成左侧抽屉，由顶栏菜单按钮打开；点击菜单项（包括当前页）、任意 hash 变化（包括同页查询参数变化、浏览器前进后退和直接修改地址）、切换屏幕宽度时关闭，支持关闭按钮、遮罩和 Escape。≤700px 时表格卡片左右固定列均解除固定，保留横向滚动空间。

侧栏和抽屉共用同一套导航：仅 `aria-current="page"` 使用橙底白字，选中态跟随当前路由。导航链接聚焦时使用 2px 次级文字色描边，未选中项可加轻微灰色背景；抽屉自动聚焦、鼠标悬停或键盘聚焦均不能产生橙色选中底色。聚焦描边向内偏移 2px，避免被导航滚动区裁切。

`route.ts` 管理页面标题、新 hash 路由与旧链接兼容。旧的 rules、nodes、entry-groups、exit-groups 分别替换为 ports、servers、server-groups、endpoint-groups；通过 `history.replaceState` 原地替换地址，保留原查询串、重复参数与编码。`go(page, focus?)` 兼容旧调用名，生成新地址；`useFocusRow` 继续保留定位高亮。

## 共用组件 API

组件均位于扁平目录 `web/src/`，命名导入：

| 文件 / 导出 | 参数 | 用法约定 |
|---|---|---|
| `PageHeader.tsx` / `PageHeader` | `title`、`description?`、`actions?`、`children?` | 大标题与右侧操作；兼容旧的 children 写法，主操作用 AntD `Button type="primary"` |
| `FilterCard.tsx` / `FilterCard` | `children`、`onRefresh?`、`loading?` | 筛选卡片，提供回调时显示刷新按钮；筛选值由页面管理 |
| 同上 / `FilterField` | `label`、`htmlFor`、`children` | 横排标签与控件，控件 `id` 必须匹配 `htmlFor` |
| `TableCard.tsx` / `TableCard<T>` | AntD `TableProps<T>`（`title` 改为字符串卡片标题），另有 `actions?` | 默认 `tableLayout="fixed"`，允许显式覆盖；透传列、选择、排序、滚动、加载与分页；默认右下角分页显示「共 N 条」，可用 `pagination` 配置覆盖，`false` 关闭 |
| `StatusDot.tsx` / `StatusDot` | `tone?: ok / warn / danger / info / muted`、`children` | 7px 圆点 + 状态文字，默认 muted；颜色必须配合文字 |
| `ColorTag.tsx` / `ColorTag` | `tone?: neutral / green / red / orange / gold / blue / purple`、`children` | 表示转发模式等分类的彩色小标签，默认 neutral；不用于替代状态圆点 |
| `FormSection.tsx` / `FormSection` | `title`、`icon`、`description?`、`collapsible?`、`defaultCollapsed?`、`children` | 弹窗或抽屉内的图标 + 标题区块；折叠隐藏但不卸载表单字段，默认展开 |
| `PlannedPage.tsx` / `PlannedPage` | `title`、`description` | 规划中页面：标题、未来用途、「规划中 / 尚未开放」，没有演示数据或操作按钮 |

示例：

```tsx
<PageHeader title="端口管理" actions={<Button type="primary" onClick={openCreate}>添加端口</Button>} />
<FilterCard onRefresh={reload} loading={loading}>
  <FilterField label="名称" htmlFor="port-query">
    <Input id="port-query" value={query} onChange={e => setQuery(e.target.value)} />
  </FilterField>
</FilterCard>
<TableCard<Port> rowKey="id" columns={columns} dataSource={rows} scroll={{ x: 900 }} />
<FormSection title="基本信息" icon={<SettingOutlined />} collapsible>
  <Form.Item name="name" label="名称" extra="填写便于辨认的名称。"><Input /></Form.Item>
</FormSection>
<StatusDot tone="ok">运行中</StatusDot>
<ColorTag tone="blue">单跳转发</ColorTag>
```

`SettingsSection` 暂保留在 `PageHeader.tsx`，兼容现有设置页两栏说明与表单布局。

## 页面与交互约定

- 页面按「页头 → 筛选卡片 → 表格卡片」排列；批量操作放入 `TableCard.actions`，配合 AntD `rowSelection`；分页保持右下角。
- 橙色用于主操作、链接、当前导航与普通控件的聚焦环；导航聚焦使用灰色描边。语义色只用于对应状态或分类。业务静态卡片无阴影，登录卡片和浮层使用统一 `shadow-popup`。
- 表单使用 AntD Modal / Drawer + Form，区块复用 `FormSection`；字段说明放入 `Form.Item extra`，用次级文字。
- 弹窗与业务抽屉不可点击遮罩关闭，只能通过 ×、取消/完成按钮和 Esc 关闭；保存中禁止关闭；移动端主导航抽屉除外。
- 表格头为 `bg-head`、行底为 `bg-elev`、悬停为 `bg-hover`；行定位使用 `accent-soft`，保持约 2.4 秒后移除。
- 既有 AntD 状态 Tag 通过兼容 CSS 转为圆点 + 文字；新增状态展示使用 `StatusDot`。分类标签使用 `ColorTag`，避免被兼容样式影响。
- 智能 DNS、延迟监控总览使用 `PlannedPage`。转发链管理使用实际 `Chains` 页面；转发端点与端点组分别使用 `Endpoints` 和 `ExitGroups`，保留原路由兼容跳转。
- 不引入后端没有的字段、假数据、假操作，也不改变 API、数据库或业务判定。

## 表格与表单收尾约定

- 在 1366–1440px 桌面视口，主列表应一屏显示全部列。列宽总和与 `scroll.x` 必须一起核算，并留出侧栏、页面内边距和卡片边框；仅更窄的内容区横向滚动。
- 表格水平内边距为 8px；长名称、地址、版本和标签允许换行。次要信息用 `.cell-sub`，不得用裁切隐藏现有字段。完整 ID 必须可查看，已有复制入口保留。
- 左固定列从最左边连续，包括选择、展开、拖动、ID 和名称中实际存在的列；只允许操作列右固定。端口名称下方显示 ID，状态下方显示同步状态；ID 与同步状态的双向排序位于表格顶部「更多排序」。
- 全站 `Segmented` 的选中项使用 `accent` 橙底和白字，未选中项使用当前主题的内嵌背景与次级文字；统一由主题组件 token 配置。
- 故障和事件筛选的原生日期时间输入继承生效主题的 `color-scheme`，底色与边框沿用主题变量；保留原有本地时间输入和 ISO 时间请求格式。
- 后端文案只按已知系统模板在展示时转换。`IncidentDisplay.ts` 集中处理证据正文和故障标题；标题按故障类型识别固定后缀，资源名称、原始错误和策略语义中的“规则”保持原文。

## IX 地址、链与端口交互

- 每台服务器的转发端点仅配置一个连接地址（IPv4 或域名）和可选对外端口；空对外端口使用本机隧道监听端口。端口映射由管理员配置，证书查看、轮换和健康入口保留。端点组直接选择服务器，组内不重复，按顺序主备。专线地址功能已移除，升级删除专线记录并将旧引用统一指向原主地址，保留成员顺序；启动服务前重编译旧快照，失败保留标记供下次启动重试。
- `Chains.tsx` 的真实列表显示名称、有序路径、引用端口数及逐边观测。新建/编辑至少两个端点组，可添加、上下移和移除；保存说明共享影响，引用删除失败保留弹窗与具体原因。列表支持搜索、分页和 `chains?focus=` 定位；空态和读取错误有明确反馈，401/428 进入既有会话流程。
- `Rules.tsx` 用直连 / 单跳转发 / 链式转发三选一，切换清除不适用引用。编辑、复制和导入保留定义；找不到已有链时显示待确认，不自动改直连。空链选项提供创建入口，已有链可跳转编辑。列表、展开详情显示全部入口、每跳全部有序候选及地址/有效端口、最终目标；这是路径定义，不冒充实时唯一选路。导出格式 3 保留共享 `chain_id`，导入不自动创建链、组或服务器。
- 同步详情分开呈现入口监听应用结果、每个入口成员就绪、各跳服务器整体配置错误、逐边 grant 应用与确认。relay 不显示虚构业务监听。服务器整体版本相等不代表该端口授权已确认；能力不足区分整条阻塞与可运行但备用不足，保留具体服务器、地址、能力和原因。末跳出站错误同时保留服务器具体 IP 错误与该端口授权原因，不混同二者范围。
- 链管理与端口逐边显示真实前驱/后继、地址身份、摘要、规则代次、链指纹和采样时间，区分本边失败、后续失败和授权应用失败。「就绪」明确未探测最终目标。每秒本地老化，读取失败期间也继续计时；10 秒到期或控制离线后只保留未知/历史语义；不会按控制在线臆造健康。端口轮询在请求进行时跳过，保存等显式刷新合并后等待最新读取，迟到成功不覆盖新状态，401/428 仍交给会话处理。
- 概览从中间或末跳服务器反查受影响端口，事故详情保留实际失败边及历史原始证据，并能跳转链。配置变更、新版本替代和新鲜证据异常解除分开展示；不把历史失败或控制离线当作新的业务中断结论。
- 服务器编辑的出站 IP 使用独立保存按钮，与名称保存互不夹带；说明仅最终目标拨号绑定、必须是末跳本机 IPv4、不填使用系统路由。所有页面复用现有主题 token、PageHeader、TableCard、FormSection、StatusDot 与 ColorTag，沿用桌面换行及窄屏横向滚动规则。
