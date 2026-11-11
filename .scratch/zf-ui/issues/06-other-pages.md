# 06 其他分组页面换皮

Status: ready-for-agent
Blocked by: 01

`Overview`、`DNSWatch`、`Incidents`（含 `IncidentDetails`）、`Events`、`Telegram`、`Security`、`Retention`、登录/首次设置页，套用 01 的页头/筛选卡片/表格卡片/状态圆点/表单区块，视觉与原型一致。功能不变；页面内提到节点/规则/入口组/出口组的文案改为新叫法（服务器/端口/服务器组/端点组/转发端点）。

页面专属样式放独立 CSS；复用 01 积木；不重建 `web/dist`。
