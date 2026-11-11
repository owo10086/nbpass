# 04 转发端点 + 端点组管理（拆分原出口组页）

Status: ready-for-agent
Blocked by: 01

把 `ExitGroups.tsx`（及 `ExitBlocked.tsx`、`TunnelRotation.tsx` 等）拆成两页：

- **转发端点** `#/endpoints`：原型 `pages/servers.js` 端点列表样式。数据来自 `GET /api/tunnel-exits`；新增/编辑走 `PUT /api/nodes/{id}/tunnel`；保留出口健康、凭据轮换、被墙相关等现有能力。列取能映射的：ID、名称/地址、协议（WSS）、状态、所属端点组、操作。共享端点、导入等不做。
- **端点组管理** `#/endpoint-groups`：原型端点组列表与表单样式。列：ID、名称、成员数、策略（主备）、操作。表单成员选择用原型左右两栏穿梭 + 已选排序（序号、上下移/拖拽），对应出口组有序成员。没有的字段（描述、自动添加）不显示。
- 文案统一新叫法。页面专属样式放独立 CSS；复用 01 积木；不重建 `web/dist`。
