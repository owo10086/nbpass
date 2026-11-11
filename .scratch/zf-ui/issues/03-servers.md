# 03 服务器管理页（原节点）

Status: ready-for-agent
Blocked by: 01

把 `Nodes.tsx` 及节点子组件（`NodeMetrics`、`NodeRegion`、`NodeUpgrade`、`NodeSelfCheck`、`NodeRotation`、`NodeManagedRecord`、`NodeValidationActions`、`ProbeDetails` 等）改成原型 `pages/servers.js` 服务器列表的样子，路由 `#/servers`。

- 列照原型取能映射的：ID、名称/入口地址、状态、所属服务器组、CPU/内存、端口数、流量、版本、操作。没有的（网卡/端口区间、续费等）不显示。
- 筛选：名称/地址搜索、状态、服务器组 + 刷新。
- 页头：添加服务器（主按钮，沿用现有新增节点 + 安装票据流程，安装命令弹窗做成原型「安装脚本」样式）。
- 行操作：编辑、安装脚本、升级、维护、自检、换 IP、受管记录、删除等现有能力，次要的进「更多」或详情抽屉。
- 文案统一新叫法。页面专属样式放独立 CSS；复用 01 积木；不重建 `web/dist`。
