# 05 服务器组管理页（原入口组）

Status: ready-for-agent
Blocked by: 01

把 `EntryGroups.tsx` 及入口组子组件（`EntryMembers`、`EntryReadiness`、`EntrySwitch`、`EntryBlockedPolicy`、`EntryDomainWatch`、`AllPublishDNS`、`ManagedDNS`、`DNSRecordPicker`）改成原型 `pages/servers.js` 服务器组列表/表单/「DNS 状态」弹窗的样子，路由 `#/server-groups`。

- 列取能映射的：ID、名称/入口（受管域名）、发布模式（主备 / 全部发布）、服务器数、DNS 自动切换状态、仅观测、操作。
- 表单：区块化；服务器成员用两栏穿梭。**发布模式建组时选、之后不可改（ADR 0007）**；受管记录/CNAME 行为按 ADR 0008，不删远端记录。
- 「DNS 状态」弹窗承载现有就绪度、切换、摘除/加回、最少保留、观测等。
- 文案统一新叫法。页面专属样式放独立 CSS；复用 01 积木；不重建 `web/dist`。
