# 06 其他分组页面换皮

Status: resolved
Blocked by: 01

`Overview`、`DNSWatch`、`Incidents`（含 `IncidentDetails`）、`Events`、`Telegram`、`Security`、`Retention`、登录/首次设置页，套用 01 的页头/筛选卡片/表格卡片/状态圆点/表单区块，视觉与原型一致。功能不变；页面内提到节点/规则/入口组/出口组的文案改为新叫法（服务器/端口/服务器组/端点组/转发端点）。

页面专属样式放独立 CSS；复用 01 积木；不重建 `web/dist`。

## Comments

- 改动页面：`web/src/Overview.tsx`、`DNSWatch.tsx`、`Incidents.tsx`、`IncidentDetails.tsx`、`Events.tsx`、`Telegram.tsx`、`Security.tsx`、`Retention.tsx`；`web/src/main.tsx` 仅修改登录/首次设置片段及其相关导入。
- 新增页面专属样式：`web/src/OtherPages.css`、`web/src/AuthPages.css`，选择器限定在本票页面或弹窗专属类内。
- 概览、监控列表与详情复用 `PageHeader`、`FilterCard`、`TableCard`、`StatusDot`、`ColorTag`，保留全部筛选、刷新、详情跳转、按天 IP 变化和历史加载。设置页与登录/首次设置复用 `FormSection`，保留表单校验、鉴权和原有操作。
- 界面名称改为服务器、端口、服务器组、端点组、转发端点；后端返回的已知系统文案只在展示时转换，保留用户资源名、历史名称、ID、原始错误详情和“抑制规则”“冻结规则”等策略语义。
- 未修改 Go、API、共用积木、`AppShell.tsx`、`route.ts`、`theme.ts` 或 `style.css`。`ConfigSync.tsx` 仅供其他票的端口/服务器组页面使用，本票保持原样。
- 验证结果：`npm --prefix web run build` 通过，路由测试 4 项通过，系统文案/事件展示的 38 项内存断言通过；只读审查发现的名称遗漏与名称误改已修复并复核。
- 完整测试复验：首轮 `make test` 达到 Go 默认 10 分钟整包时限，超时位置 `TestEntryOfflineNoCoverageOrNoCandidatePreservesRecordAndNotifies` 单独复跑通过。第二轮加长时限后完成，`TestAllPublishSetWatchAndPerRecordStatusAudit`、`TestObservationCancelledPrimaryRestoresCurrentExpectation`、`TestObservationCancelsPermitWhileEarlierControlMessageIsBlocked` 出现等待状态/消息失败，三项随后单独复验全部通过。第三轮使用 2 个 Go 调度线程和串行包测试，上述三项未再失败，但 `TestRotationHTTPTimeoutResumesSameActionAfterRestart/调用中重建` 未进入预期状态；该用例随后连续复跑 3 次、共 9 个子用例全部通过。最终 `GOMAXPROCS=4 GOFLAGS='-timeout=30m -p=1' make test` 全部通过（集成测试包 379.612 秒），包含前端构建、节点打包、`go vet ./...` 和 `go test ./...`；未改动测试内容或仓库测试配置。
- 构建后已执行 `git checkout -- web/dist && git clean -fdq web/dist`，不提交 dist；节点打包文件与依赖锁文件没有变更。构建仅有大于 500 kB 的分包提示和终端颜色环境提示。
- 浏览器验收按用户安排由用户完成。
