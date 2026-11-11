# 界面改为 Zero Forwarder 原型风格（端口管理分组）

Status: ready-for-agent

视觉与交互参考：`/Users/macmini/Documents/Aixiaoji/nbpass/.scratch/research/zeroforwarder-prototype/`（纯静态原型，`python3 -m http.server` 起在该目录即可浏览；主题在 `assets/app.css`，组件在 `assets/ui.js`，页面在 `pages/*.js`）。原型是演示数据，**不能当作后端行为依据**；原型代码是原生 JS，只参考视觉和交互，在 React + Ant Design 里重写。

## 已定决策（2026-11-11 用户确认）

1. **只改前端**。不改后端、不改 API、不改数据库。全站换成原型视觉：深色底、唯一橙色强调色、可折叠分组侧栏、顶栏面包屑。
2. **功能按现有后端映射**。原型里后端没有的字段和操作一律不显示；现有功能一项都不能丢（可以挪到行操作、弹窗、抽屉里，但必须可达）。
3. **界面名称改用原型叫法**，GLOSSARY 增加界面名称对照：

   | 界面名称 | 现有概念 | 现有页面 |
   |---|---|---|
   | 端口 / 端口管理 | 转发规则 | `Rules.tsx` |
   | 服务器 / 服务器管理 | 节点 | `Nodes.tsx` |
   | 转发端点 | 出口（节点上的隧道出口配置，`/api/tunnel-exits`） | 现在嵌在 `ExitGroups.tsx` |
   | 端点组 / 端点组管理 | 出口组 | `ExitGroups.tsx` |
   | 服务器组 / 服务器组管理 | 入口组 | `EntryGroups.tsx` |
   | 线路 | 规则的入口（入口节点或入口组） | — |
   | 转发模式：直连 / 单跳转发 | 直连 / 隧道 | — |

4. **侧栏结构**（顺序固定）：
   - `概览` `#/overview`（顶层单项，占原型「功能总览」的位置）
   - 分组 **端口管理**：端口管理 `#/ports`、服务器管理 `#/servers`、转发端点 `#/endpoints`、端点组管理 `#/endpoint-groups`、转发链管理 `#/chains`、服务器组管理 `#/server-groups`、智能 DNS `#/smart-dns`、延迟监控总览 `#/latency`
   - 分组 **监控告警**：解析监控 `#/dns-watch`、故障 `#/incidents`、事件日志 `#/events`
   - 分组 **系统设置**：Telegram 通知 `#/telegram`、安全设置 `#/security`、保留期 `#/retention`
   - 不加原型的共享服务器、WireGuard 组网、NAT 穿透、路由配置、订阅、商城等。
5. **转发链、智能 DNS、延迟监控总览**：菜单里占位，页面是「规划中」空状态（标题 + 一句话说明将来做什么 + 「尚未开放」），没有任何假数据或假按钮。
6. **旧路由跳转**：`#/rules→#/ports`、`#/nodes→#/servers`、`#/entry-groups→#/server-groups`、`#/exit-groups→#/endpoint-groups`，保留 `?focus=` 等查询参数（后端或通知里可能有旧链接）。
7. **页面布局模式**照原型：页头（大标题 + 右侧操作按钮，主操作橙色）→ 筛选卡片（标签 + 控件 + 刷新）→ 表格卡片（有批量操作时带勾选列；右下角「共 N 条」分页）。状态用「圆点 + 文字」，转发模式等用原型风格的彩色小标签，数字等宽。表单用弹窗/抽屉，按区块分组（图标 + 区块标题），字段下灰色说明。
8. **窄屏**：侧栏收为抽屉，保留现有可用性。
9. **DESIGN.md** 改写为新的深色橙色设计系统（token、组件约定），替换旧的白底蓝色描述。

## 不做

- 后端新能力：多目标、代理入站、限速、续费/配额、权限、转发链、智能 DNS、延迟探测与测速、端点共享等。
- 原型「功能总览」卡片墙。

## 验收

- `npm --prefix web run build` 通过；`make test` 通过。
- 本机用 http 回环源跑面板（`go run ./cmd/nbpass -data-dir /tmp/<新目录> -public-origin http://localhost:<空闲端口>`，先 `lsof -iTCP -sTCP:LISTEN | grep nbpass` 避开已占用端口；不要配 HTTPS），逐页对照原型截图检查。
- 现有每项功能在新界面都能找到并能用。
