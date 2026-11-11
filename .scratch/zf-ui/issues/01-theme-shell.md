# 01 主题与外壳

Status: resolved

见 `../spec.md`。本票打地基，02–06 都依赖它。

## 要做

- 主题：从原型 `assets/app.css` 提取颜色/圆角/字号/间距，做成**唯一一处** token（CSS 变量 + AntD `ConfigProvider`，用 dark 算法），去掉 `style.css` 与 `main.tsx` 两处重复定义。
- 外壳：原型同款可折叠分组侧栏（Logo 写 nbpass）、顶栏（面包屑「分组 / 页面」+ 右侧账户入口），内容区。账户入口里保留现有退出等操作。窄屏侧栏改抽屉。
- 路由：`route.ts` 增加 spec 第 4 条全部路由与第 6 条旧路由跳转（保留查询参数）。暂时把 `#/endpoints` 指向现有 `ExitGroups` 页面即可，04 再拆。
- 占位页：`PlannedPage` 组件，用于转发链管理、智能 DNS、延迟监控总览。
- 共用积木（02–06 必须复用，放各自独立文件）：页头（改造 `PageHeader.tsx`）、筛选卡片、表格卡片（含右下角「共 N 条」分页样式）、状态圆点、彩色标签、表单区块（图标 + 标题 + 可折叠）。
- 页面显示名改为原型叫法（侧栏、页头、面包屑）；页面内部文案留给后续票。
- 文档：`DESIGN.md` 改写为新设计系统；`GLOSSARY.md` 加界面名称对照表（spec 第 3 条）。

## 不做

- 各业务页面的内部布局与文案（02–06）。不重建 `web/dist`（07 统一做）。

## 验收

- `npm --prefix web run build`、`make test` 通过。
- 跑面板逐个点侧栏，所有页面能打开、旧路由能跳转、深色主题下现有页面无明显不可读（白底残留、黑字黑底）。

## Comments

2026-11-11 收尾交付：

- 主题与共用积木见 `b7a11e3`，分组导航与旧路由兼容见 `9137010`。本轮仅修改外壳和文档，业务页面由 02–06 票继续迁移。
- `web/src/AppShell.tsx` 保留菜单点击关闭（包括点击当前页），新增独立的 `hashchange` 监听及卸载清理；同页查询参数变化、旧链接跳转、浏览器前进后退及直接修改 hash 均会关闭抽屉。
- `web/src/style.css` 中侧栏和抽屉共用的导航样式明确覆盖 AntD 链接状态：聚焦为灰色内描边与轻微背景，只有当前路由的 `aria-current="page"` 使用橙底白字；概览入口保留原缩进。
- 已核对三个 `PlannedPage`：转发链管理说明未来编排多个转发端点；智能 DNS 说明未来按线路与可用性管理解析策略；延迟监控总览说明未来汇总延迟与连通性。均有独立标题、用途说明、「规划中 / 尚未开放」，无假数据或操作按钮。
- `DESIGN.md` 已统一记录深色主题 token、导航状态、组件 API 与使用示例；`GLOSSARY.md` 已补齐规格中的界面名称对照，并澄清端口与端口号、端点与分组的含义。

共用积木均从 `web/src/` 下对应文件命名导入，02–06 票复用：

| 文件 / 导出 | 用法 |
|---|---|
| `theme.ts` / `tokens`、`themeConfig`、`applyTheme` | 唯一 token 来源；应用入口先生成 CSS 变量，再传入 AntD `ConfigProvider`，业务样式引用变量。 |
| `PageHeader.tsx` / `PageHeader` | `title`、`description`、`actions`；主操作用 `Button type="primary"`，兼容原有 `children` 操作区；同文件保留 `SettingsSection`。 |
| `FilterCard.tsx` / `FilterCard`、`FilterField` | 卡片传 `onRefresh`、`loading`；字段 `htmlFor` 与控件 `id` 对应，筛选状态由页面维护。 |
| `TableCard.tsx` / `TableCard<T>` | 透传 AntD 表格属性；字符串 `title` 与 `actions` 组成卡片头，默认右下角显示「共 N 条」，`pagination={false}` 关闭分页。 |
| `StatusDot.tsx` / `StatusDot` | `tone` 选 `ok / warn / danger / info / muted`，子元素提供状态文字，不单独依赖颜色。 |
| `ColorTag.tsx` / `ColorTag` | `tone` 选 `neutral / green / red / orange / gold / blue / purple`，用于转发模式等分类。 |
| `FormSection.tsx` / `FormSection` | `title`、`icon`、可选 `description`；`collapsible` 开启折叠，`defaultCollapsed` 设置初态，折叠不卸载字段。 |
| `PlannedPage.tsx` / `PlannedPage` | 必传 `title`、`description`，统一呈现规划中空状态。 |

验证：

- `npm --prefix web run build` 通过。
- `npm --prefix web test` 通过（`node --experimental-strip-types --test route.test.mjs`，4 项路由测试）。
- 一次性组件检查先复现 hash 变化不关闭，再验证菜单点击、同页 hash 变化、页面变化、屏幕宽度变化、唯一选中项与监听清理；三个占位页的服务端渲染检查通过。未新增测试依赖或辅助文件。
- 只读审阅未发现需要修复的问题。
- `make test` 首轮在多个工作树同时运行集成测试时失败：`TestIncidentsScaleKeepsControlAndAutomaticSwitchWorking`、`TestRotationReleaseWaitsForNodePersistence` 等待状态失败，随后 `clusterharness` 触发默认 10 分钟超时。上述两项及超时时正在运行的 `TestAutomaticRotationRejectsUnsafePreflight` 以 `GOMAXPROCS=2 go test -count=1 -timeout=3m ./internal/clusterharness -run '^(TestIncidentsScaleKeepsControlAndAutomaticSwitchWorking|TestRotationReleaseWaitsForNodePersistence|TestAutomaticRotationRejectsUnsafePreflight)$'` 单独复跑全部通过（24.075 秒）。
- `GOMAXPROCS=2 GOFLAGS='-timeout=30m' make test` 完整复跑通过，包含前端重新构建、节点包构建、`go vet ./...` 和 `go test ./...`；`clusterharness` 用时 1665.440 秒。本轮未修改 Go 代码或测试。
- 测试后执行 `git checkout -- web/dist && git clean -fdq web/dist` 还原构建产物，不提交 `dist`。

已知问题与后续：

- `#/endpoints` 暂时复用 `ExitGroups`，由 04 票拆分；各业务页内部布局和文案继续由 02–06 票负责。
- Vite 提示 AntD 产物大于 500 kB，构建成功，后续可单独优化分包。
- 按本轮约定未做浏览器验收；由用户复验约 809px 抽屉关闭、聚焦与选中态，以及宽屏侧栏。发布产物仍由 07 票统一更新。
