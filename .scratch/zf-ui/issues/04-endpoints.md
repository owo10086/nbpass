# 04 转发端点 + 端点组管理（拆分原出口组页）

Status: resolved
Blocked by: 01

把 `ExitGroups.tsx`（及 `ExitBlocked.tsx`、`TunnelRotation.tsx` 等）拆成两页：

- **转发端点** `#/endpoints`：原型 `pages/servers.js` 端点列表样式。数据来自 `GET /api/tunnel-exits`；新增/编辑走 `PUT /api/nodes/{id}/tunnel`；保留出口健康、凭据轮换、被墙相关等现有能力。列取能映射的：ID、名称/地址、协议（WSS）、状态、所属端点组、操作。共享端点、导入等不做。
- **端点组管理** `#/endpoint-groups`：原型端点组列表与表单样式。列：ID、名称、成员数、策略（主备）、操作。表单成员选择用原型左右两栏穿梭 + 已选排序（序号、上下移/拖拽），对应出口组有序成员。没有的字段（描述、自动添加）不显示。
- 文案统一新叫法。页面专属样式放独立 CSS；复用 01 积木；不重建 `web/dist`。

## Comments

- 将原出口组页拆为 `Endpoints.tsx`（`#/endpoints`）和 `ExitGroups.tsx`（`#/endpoint-groups`）。两页复用 PageHeader、FilterCard、TableCard、StatusDot、ColorTag、FormSection；独立样式集中在 `Endpoints.css`，不改共用组件或主题。
- 转发端点列表显示真实服务器 ID、服务器名称与 IPv4/端口、WSS、服务器控制连接状态及全部端点组归属，支持名称/地址/ID、控制状态、端点组筛选与分页。新增/编辑继续调用现有节点隧道配置 API；名称沿用服务器名称，一个服务器对应一个端点。
- 端点组列表保留新增、编辑、删除、名称排序、恢复自定义顺序、拖动保存显示顺序。超过 10 组时提供“显示全部以排序”，可调整跨页位置。表单用左右两栏搜索、勾选、全选当前结果、添加/移除选择成员，已选端点显示实际主备序号，支持逐项及批量上下移；始终按成员 ID 数组原样提交主备顺序，并校验至少一项、不重复、已配置 WSS。
- 现有功能位置：地址与监听端口、可复制的证书身份（另提供折叠查看/复制 PEM）位于转发端点的新增/编辑弹窗；三种证书/凭据轮换及入口确认进度、被墙处理开关和地址应用/握手进度位于端点行操作弹窗；按入口观测的链路健康位于转发端点列表下方，保留连续失败、最近探测、故障归属、恢复起点和探测时间，每 5 秒刷新。健康观测按采样时间在 10 秒后自动降级为历史证据，请求超时后可自动或手动重试；控制离线不等同链路故障。
- 与原型的有意差异：只展示现有后端支持的 IPv4 + WSS 和主备策略；不显示共享/导入、多协议、域名、IPv6 开关、启停、配额/流量/延迟测试、独立端点名称、描述和自动添加等字段或操作；端点没有新增删除 API，因此不提供假删除按钮。成员排序采用序号、逐项及批量上下移，不实现原型的拖拽、点击序号跳转、Shift 多选或双击添加；窄屏穿梭选择上下排列。
- 共用/外壳改动仅 `web/src/main.tsx`：第 10 行引入 Endpoints，第 91、92 行分别映射 endpoints 和 endpoint-groups，并透传 focus；AppShell、route、theme、style 及其他页面不改。旧出口组路由继续使用既有别名跳转，端点组归属链接可定位对应组。
- 验证：`npm --prefix web run build` 通过；`npm --prefix web test -- src/EndpointData.test.mjs` 共 9 项通过，覆盖旧路由、成员去重/移除/顺序/边界、实时与历史健康状态及 10 秒过期边界。遵守本票范围，不修改共用 package.json，页面测试需通过上述追加参数命令运行。`git diff --check` 通过；只读代码审查及修复复核通过。
- 全量检查：首次 `make test` 在 clusterharness 包触及默认 10 分钟总时限，当时正在执行 `TestEntryBlockedRotationWritebackMustBeConfirmedBeforeRetry`；该用例单独复跑通过（约 50 秒）。`GOFLAGS=-timeout=30m make test` 完整通过（包含前端构建、节点包构建、`go vet ./...`、`go test ./...`），clusterharness 耗时 1419.984 秒，仅延长时限，不改仓库配置。随后原始 `make test` 未复用缓存，再次启动长测，因完整成功证据已取得而主动停止重复运行。
- 已知问题：未发现本票功能阻断问题；集成测试的默认 10 分钟整包时限不足，需要上述 30 分钟时限。Vite 仍提示 AntD 公共包体积超过 500 kB。按用户要求未做浏览器验收，视觉和实际操作留给用户复核。已恢复构建产物，不提交 `web/dist` 或节点包。
