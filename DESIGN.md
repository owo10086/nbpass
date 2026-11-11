# nbpass 设计系统

本面板采用 Zero Forwarder 原型的深色橙色视觉，用 React + Ant Design 6 实现。需求来源是 `.scratch/zf-ui/spec.md`；原型仅供视觉与交互参考，不代表后端能力。现有功能继续保留，后续 02–06 票逐页接入共用组件。

## Token 与主题

**唯一可执行来源是 `web/src/theme.ts` 的 `tokens`。** `applyTheme()` 在挂载 React 前生成根节点 CSS 变量；`themeConfig` 将同一组值交给 `ConfigProvider`，并启用 `theme.darkAlgorithm`。新增或调整 token 只改该文件，业务 CSS 和 SVG 通过 `var(--变量名)` 引用，禁止另建一套颜色常量。以下表格是阅读用说明。

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

顶栏显示「分组 / 页面」面包屑，右侧账户菜单保留退出登录、退出所有会话和会话期限说明。内容区桌面内边距为 `20px 24px 48px`。≤860px 时侧栏变成左侧抽屉，由顶栏菜单按钮打开；点击菜单项（包括当前页）、任意 hash 变化（包括同页查询参数变化、浏览器前进后退和直接修改地址）、切换屏幕宽度时关闭，支持关闭按钮、遮罩和 Escape。≤640px 时表格右侧操作列解除固定，保留横向滚动空间。

侧栏和抽屉共用同一套导航：仅 `aria-current="page"` 使用橙底白字，选中态跟随当前路由。导航链接聚焦时使用 2px 次级文字色描边，未选中项可加轻微灰色背景；抽屉自动聚焦、鼠标悬停或键盘聚焦均不能产生橙色选中底色。聚焦描边向内偏移 2px，避免被导航滚动区裁切。

`route.ts` 管理页面标题、新 hash 路由与旧链接兼容。旧的 rules、nodes、entry-groups、exit-groups 分别替换为 ports、servers、server-groups、endpoint-groups；通过 `history.replaceState` 原地替换地址，保留原查询串、重复参数与编码。`go(page, focus?)` 兼容旧调用名，生成新地址；`useFocusRow` 继续保留定位高亮。

## 共用组件 API（02–06 票复用）

组件均位于扁平目录 `web/src/`，命名导入：

| 文件 / 导出 | 参数 | 用法约定 |
|---|---|---|
| `PageHeader.tsx` / `PageHeader` | `title`、`description?`、`actions?`、`children?` | 大标题与右侧操作；兼容旧的 children 写法，主操作用 AntD `Button type="primary"` |
| `FilterCard.tsx` / `FilterCard` | `children`、`onRefresh?`、`loading?` | 筛选卡片，提供回调时显示刷新按钮；筛选值由页面管理 |
| 同上 / `FilterField` | `label`、`htmlFor`、`children` | 横排标签与控件，控件 `id` 必须匹配 `htmlFor` |
| `TableCard.tsx` / `TableCard<T>` | AntD `TableProps<T>`（`title` 改为字符串卡片标题），另有 `actions?` | 透传列、选择、排序、滚动、加载与分页；默认右下角分页显示「共 N 条」，可用 `pagination` 配置覆盖，`false` 关闭 |
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
- 表格头为 `bg-head`、行底为 `bg-elev`、悬停为 `bg-hover`；行定位使用 `accent-soft`，保持约 2.4 秒后移除。
- 既有 AntD 状态 Tag 暂以 CSS 转为圆点 + 文字；后续业务票迁移到 `StatusDot`。分类标签使用 `ColorTag`，避免被兼容样式影响。
- 转发链、智能 DNS、延迟监控总览使用 `PlannedPage`。转发端点本票暂复用 `ExitGroups`，04 票再拆分。其余业务内部文案和布局按 02–06 票迁移。
- 不引入后端没有的字段、假数据、假操作，也不改变 API、数据库或业务判定。
