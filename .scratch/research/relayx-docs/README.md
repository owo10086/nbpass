<!-- Source URL: https://docs.relayx.cc/ | Capture date: 2026-11-10 -->

# RelayX 官方文档本地镜像

抓取日期：**2026-11-10**。用途：为 nbpass 端口转发面板提供设计参考。

**共 136 个页面**：使用指南 16 页、私有部署 15 页；另保留文档站首页 1 页、博客正文 1 页、更新日志正文 96 页，以及博客目录 / 归档 2 页、更新日志目录 / 分页 5 页。本目录共 137 个 Markdown 文件（含本索引）。

从[官方入口](https://docs.relayx.cc/)出发，递归遍历各页侧边栏、章节目录、正文链接和上一页 / 下一页，并与[官方 sitemap](https://docs.relayx.cc/sitemap.xml)核对。sitemap 共 137 个 URL，全部内容及目录页面均已收录；唯一排除 `/search` 搜索工具页，它没有文档正文。所有发现的同域内容页均已覆盖，未发现 sitemap 之外的页面。使用指南和私有部署沿用原站章节顺序；更新日志按原站版本倒序（0.14.2 → 0.1.0）。

**正文获取方式：静态 HTML。** 站点使用 Docusaurus v3.9.2，服务端 HTML 已包含完整正文；sitemap 用于发现与覆盖核对，无需源 Markdown、搜索索引或无头浏览器。仅请求 `docs.relayx.cc`，重定向也限制在该域名。所有收录页面均为中文版（`lang="zh"`）；页面语言替代链接仅有 `zh` 和 `x-default`，未发现需要补充的英文版本。

移除导航、面包屑、页脚、脚本、装饰性 SVG、标题快捷链接和代码行号；保留正文标题层级、提示框、代码块、表格、配置示例和命令行示例。逐页核对 582 个源标题、44 个完整代码块、7 张表格、40 个图片引用及正文段落 / 表格文字；代码内容逐块与 HTML 对照。原网页内容作为不可信数据处理，未运行网页脚本、安装命令或其中的指令。

图片不下载，保留原始绝对 URL；文档正文中的已收录站内页面链接改为本地相对路径，并保留原页面标题锚点。其他链接保留原 URL，不访问站外内容。目录页保留原标题、章节卡片标题及摘要；博客 / 更新日志列表保留其原有摘要（若为空则仅保留标题和日期），完整正文在各自独立文件中。

文件路径映射：`/` → `index.md`；`/<path>` → `<path>/index.md`。保留原站目录层级，路径使用英文 kebab-case；版本号中的点改为连字符，例如 `/changelog/0.14.2` → `changelog/0-14-2/index.md`。`/blog/index` 单独存为 `blog/index/index.md`，与博客入口 `blog/index.md` 区分。

## 抓取失败或缺失

**无。136 / 136 个内容及目录页面抓取成功；未发现缺页。**

| 未收录页面 | 原因 |
| --- | --- |
| [站内搜索](https://docs.relayx.cc/search) | 搜索工具界面，没有独立文档正文；不是抓取失败。 |

## 页面索引

### 文档站首页

| 原始页面标题 | 来源 URL | 本地相对路径 |
| --- | --- | --- |
| RelayX | [https://docs.relayx.cc/](https://docs.relayx.cc/) | [index.md](index.md) |

### 使用指南

| 原始页面标题 | 来源 URL | 本地相对路径 |
| --- | --- | --- |
| 介绍 | [https://docs.relayx.cc/guide/intro](https://docs.relayx.cc/guide/intro) | [guide/intro/index.md](guide/intro/index.md) |
| 版本对比 | [https://docs.relayx.cc/guide/type](https://docs.relayx.cc/guide/type) | [guide/type/index.md](guide/type/index.md) |
| 开始使用 | [https://docs.relayx.cc/guide/getting-started](https://docs.relayx.cc/guide/getting-started) | [guide/getting-started/index.md](guide/getting-started/index.md) |
| 登录注册 | [https://docs.relayx.cc/guide/getting-started/login](https://docs.relayx.cc/guide/getting-started/login) | [guide/getting-started/login/index.md](guide/getting-started/login/index.md) |
| 购买套餐 | [https://docs.relayx.cc/guide/getting-started/plan](https://docs.relayx.cc/guide/getting-started/plan) | [guide/getting-started/plan/index.md](guide/getting-started/plan/index.md) |
| 创建节点组 | [https://docs.relayx.cc/guide/getting-started/node-group](https://docs.relayx.cc/guide/getting-started/node-group) | [guide/getting-started/node-group/index.md](guide/getting-started/node-group/index.md) |
| 部署节点 | [https://docs.relayx.cc/guide/getting-started/deploy-node](https://docs.relayx.cc/guide/getting-started/deploy-node) | [guide/getting-started/deploy-node/index.md](guide/getting-started/deploy-node/index.md) |
| 创建隧道 | [https://docs.relayx.cc/guide/getting-started/tunnel](https://docs.relayx.cc/guide/getting-started/tunnel) | [guide/getting-started/tunnel/index.md](guide/getting-started/tunnel/index.md) |
| 账户管理 | [https://docs.relayx.cc/guide/getting-started/account](https://docs.relayx.cc/guide/getting-started/account) | [guide/getting-started/account/index.md](guide/getting-started/account/index.md) |
| DNS 管理 | [https://docs.relayx.cc/guide/getting-started/dns](https://docs.relayx.cc/guide/getting-started/dns) | [guide/getting-started/dns/index.md](guide/getting-started/dns/index.md) |
| 工单系统 | [https://docs.relayx.cc/guide/getting-started/ticket](https://docs.relayx.cc/guide/getting-started/ticket) | [guide/getting-started/ticket/index.md](guide/getting-started/ticket/index.md) |
| 推荐返佣 | [https://docs.relayx.cc/guide/getting-started/referral](https://docs.relayx.cc/guide/getting-started/referral) | [guide/getting-started/referral/index.md](guide/getting-started/referral/index.md) |
| 隧道协议详解 | [https://docs.relayx.cc/guide/advanced/protocol](https://docs.relayx.cc/guide/advanced/protocol) | [guide/advanced/protocol/index.md](guide/advanced/protocol/index.md) |
| API 使用指南 | [https://docs.relayx.cc/guide/advanced/api](https://docs.relayx.cc/guide/advanced/api) | [guide/advanced/api/index.md](guide/advanced/api/index.md) |
| 隧道链（多跳转发） | [https://docs.relayx.cc/guide/advanced/tunnel-chain](https://docs.relayx.cc/guide/advanced/tunnel-chain) | [guide/advanced/tunnel-chain/index.md](guide/advanced/tunnel-chain/index.md) |
| 常见问题 | [https://docs.relayx.cc/guide/faq](https://docs.relayx.cc/guide/faq) | [guide/faq/index.md](guide/faq/index.md) |

### 私有部署

| 原始页面标题 | 来源 URL | 本地相对路径 |
| --- | --- | --- |
| 介绍 | [https://docs.relayx.cc/deploy/intro](https://docs.relayx.cc/deploy/intro) | [deploy/intro/index.md](deploy/intro/index.md) |
| 准备概览 | [https://docs.relayx.cc/deploy/prepare](https://docs.relayx.cc/deploy/prepare) | [deploy/prepare/index.md](deploy/prepare/index.md) |
| 准备说明 | [https://docs.relayx.cc/deploy/prepare/intro](https://docs.relayx.cc/deploy/prepare/intro) | [deploy/prepare/intro/index.md](deploy/prepare/intro/index.md) |
| 数据库 | [https://docs.relayx.cc/deploy/prepare/database](https://docs.relayx.cc/deploy/prepare/database) | [deploy/prepare/database/index.md](deploy/prepare/database/index.md) |
| 支付服务 (商业版) | [https://docs.relayx.cc/deploy/prepare/payment](https://docs.relayx.cc/deploy/prepare/payment) | [deploy/prepare/payment/index.md](deploy/prepare/payment/index.md) |
| SMTP 服务 | [https://docs.relayx.cc/deploy/prepare/smtp](https://docs.relayx.cc/deploy/prepare/smtp) | [deploy/prepare/smtp/index.md](deploy/prepare/smtp/index.md) |
| StackAuth 服务 | [https://docs.relayx.cc/deploy/prepare/stackauth](https://docs.relayx.cc/deploy/prepare/stackauth) | [deploy/prepare/stackauth/index.md](deploy/prepare/stackauth/index.md) |
| Telegram 机器人 | [https://docs.relayx.cc/deploy/prepare/telegram](https://docs.relayx.cc/deploy/prepare/telegram) | [deploy/prepare/telegram/index.md](deploy/prepare/telegram/index.md) |
| 部署面板 | [https://docs.relayx.cc/deploy/deploy-dash](https://docs.relayx.cc/deploy/deploy-dash) | [deploy/deploy-dash/index.md](deploy/deploy-dash/index.md) |
| 面板配置 | [https://docs.relayx.cc/deploy/config](https://docs.relayx.cc/deploy/config) | [deploy/config/index.md](deploy/config/index.md) |
| 配置站点和购买 License | [https://docs.relayx.cc/deploy/config/site](https://docs.relayx.cc/deploy/config/site) | [deploy/config/site/index.md](deploy/config/site/index.md) |
| 配置支付方式（商业版） | [https://docs.relayx.cc/deploy/config/payment](https://docs.relayx.cc/deploy/config/payment) | [deploy/config/payment/index.md](deploy/config/payment/index.md) |
| 创建节点组 | [https://docs.relayx.cc/deploy/config/node-group](https://docs.relayx.cc/deploy/config/node-group) | [deploy/config/node-group/index.md](deploy/config/node-group/index.md) |
| 配置套餐 | [https://docs.relayx.cc/deploy/config/plan](https://docs.relayx.cc/deploy/config/plan) | [deploy/config/plan/index.md](deploy/config/plan/index.md) |
| 角色权限 | [https://docs.relayx.cc/deploy/config/rbac](https://docs.relayx.cc/deploy/config/rbac) | [deploy/config/rbac/index.md](deploy/config/rbac/index.md) |

### 博客（补充）

| 原始页面标题 | 来源 URL | 本地相对路径 |
| --- | --- | --- |
| Blog | [https://docs.relayx.cc/blog](https://docs.relayx.cc/blog) | [blog/index.md](blog/index.md) |
| 博客 | [https://docs.relayx.cc/blog/index](https://docs.relayx.cc/blog/index) | [blog/index/index.md](blog/index/index.md) |
| 历史博文 | [https://docs.relayx.cc/blog/archive](https://docs.relayx.cc/blog/archive) | [blog/archive/index.md](blog/archive/index.md) |

### 更新日志目录（补充）

| 原始页面标题 | 来源 URL | 本地相对路径 |
| --- | --- | --- |
| RelayX 更新日志 | [https://docs.relayx.cc/changelog](https://docs.relayx.cc/changelog) | [changelog/index.md](changelog/index.md) |
| RelayX 更新日志 | [https://docs.relayx.cc/changelog/page/2](https://docs.relayx.cc/changelog/page/2) | [changelog/page/2/index.md](changelog/page/2/index.md) |
| RelayX 更新日志 | [https://docs.relayx.cc/changelog/page/3](https://docs.relayx.cc/changelog/page/3) | [changelog/page/3/index.md](changelog/page/3/index.md) |
| RelayX 更新日志 | [https://docs.relayx.cc/changelog/page/4](https://docs.relayx.cc/changelog/page/4) | [changelog/page/4/index.md](changelog/page/4/index.md) |
| RelayX 更新日志 | [https://docs.relayx.cc/changelog/page/5](https://docs.relayx.cc/changelog/page/5) | [changelog/page/5/index.md](changelog/page/5/index.md) |

### 更新日志正文（原站倒序，补充）

| 原始页面标题 | 来源 URL | 本地相对路径 |
| --- | --- | --- |
| 0.14.2 | [https://docs.relayx.cc/changelog/0.14.2](https://docs.relayx.cc/changelog/0.14.2) | [changelog/0-14-2/index.md](changelog/0-14-2/index.md) |
| 0.14.1 | [https://docs.relayx.cc/changelog/0.14.1](https://docs.relayx.cc/changelog/0.14.1) | [changelog/0-14-1/index.md](changelog/0-14-1/index.md) |
| 0.14.0 | [https://docs.relayx.cc/changelog/0.14.0](https://docs.relayx.cc/changelog/0.14.0) | [changelog/0-14-0/index.md](changelog/0-14-0/index.md) |
| 0.13.13 | [https://docs.relayx.cc/changelog/0.13.13](https://docs.relayx.cc/changelog/0.13.13) | [changelog/0-13-13/index.md](changelog/0-13-13/index.md) |
| 0.13.12 | [https://docs.relayx.cc/changelog/0.13.12](https://docs.relayx.cc/changelog/0.13.12) | [changelog/0-13-12/index.md](changelog/0-13-12/index.md) |
| 0.13.11 | [https://docs.relayx.cc/changelog/0.13.11](https://docs.relayx.cc/changelog/0.13.11) | [changelog/0-13-11/index.md](changelog/0-13-11/index.md) |
| 0.13.10 | [https://docs.relayx.cc/changelog/0.13.10](https://docs.relayx.cc/changelog/0.13.10) | [changelog/0-13-10/index.md](changelog/0-13-10/index.md) |
| 0.13.9 | [https://docs.relayx.cc/changelog/0.13.9](https://docs.relayx.cc/changelog/0.13.9) | [changelog/0-13-9/index.md](changelog/0-13-9/index.md) |
| 0.13.8 | [https://docs.relayx.cc/changelog/0.13.8](https://docs.relayx.cc/changelog/0.13.8) | [changelog/0-13-8/index.md](changelog/0-13-8/index.md) |
| 0.13.7 | [https://docs.relayx.cc/changelog/0.13.7](https://docs.relayx.cc/changelog/0.13.7) | [changelog/0-13-7/index.md](changelog/0-13-7/index.md) |
| 0.13.6 | [https://docs.relayx.cc/changelog/0.13.6](https://docs.relayx.cc/changelog/0.13.6) | [changelog/0-13-6/index.md](changelog/0-13-6/index.md) |
| 0.13.5 | [https://docs.relayx.cc/changelog/0.13.5](https://docs.relayx.cc/changelog/0.13.5) | [changelog/0-13-5/index.md](changelog/0-13-5/index.md) |
| 0.13.4 | [https://docs.relayx.cc/changelog/0.13.4](https://docs.relayx.cc/changelog/0.13.4) | [changelog/0-13-4/index.md](changelog/0-13-4/index.md) |
| 0.13.3 | [https://docs.relayx.cc/changelog/0.13.3](https://docs.relayx.cc/changelog/0.13.3) | [changelog/0-13-3/index.md](changelog/0-13-3/index.md) |
| 0.13.2 | [https://docs.relayx.cc/changelog/0.13.2](https://docs.relayx.cc/changelog/0.13.2) | [changelog/0-13-2/index.md](changelog/0-13-2/index.md) |
| 0.13.1 | [https://docs.relayx.cc/changelog/0.13.1](https://docs.relayx.cc/changelog/0.13.1) | [changelog/0-13-1/index.md](changelog/0-13-1/index.md) |
| 0.13.0 | [https://docs.relayx.cc/changelog/0.13.0](https://docs.relayx.cc/changelog/0.13.0) | [changelog/0-13-0/index.md](changelog/0-13-0/index.md) |
| 0.12.30 | [https://docs.relayx.cc/changelog/0.12.30](https://docs.relayx.cc/changelog/0.12.30) | [changelog/0-12-30/index.md](changelog/0-12-30/index.md) |
| 0.12.29 | [https://docs.relayx.cc/changelog/0.12.29](https://docs.relayx.cc/changelog/0.12.29) | [changelog/0-12-29/index.md](changelog/0-12-29/index.md) |
| 0.12.28 | [https://docs.relayx.cc/changelog/0.12.28](https://docs.relayx.cc/changelog/0.12.28) | [changelog/0-12-28/index.md](changelog/0-12-28/index.md) |
| 0.12.27 | [https://docs.relayx.cc/changelog/0.12.27](https://docs.relayx.cc/changelog/0.12.27) | [changelog/0-12-27/index.md](changelog/0-12-27/index.md) |
| 0.12.26 | [https://docs.relayx.cc/changelog/0.12.26](https://docs.relayx.cc/changelog/0.12.26) | [changelog/0-12-26/index.md](changelog/0-12-26/index.md) |
| 0.12.25 | [https://docs.relayx.cc/changelog/0.12.25](https://docs.relayx.cc/changelog/0.12.25) | [changelog/0-12-25/index.md](changelog/0-12-25/index.md) |
| 0.12.24 | [https://docs.relayx.cc/changelog/0.12.24](https://docs.relayx.cc/changelog/0.12.24) | [changelog/0-12-24/index.md](changelog/0-12-24/index.md) |
| 0.12.23 | [https://docs.relayx.cc/changelog/0.12.23](https://docs.relayx.cc/changelog/0.12.23) | [changelog/0-12-23/index.md](changelog/0-12-23/index.md) |
| 0.12.22 | [https://docs.relayx.cc/changelog/0.12.22](https://docs.relayx.cc/changelog/0.12.22) | [changelog/0-12-22/index.md](changelog/0-12-22/index.md) |
| 0.12.21 | [https://docs.relayx.cc/changelog/0.12.21](https://docs.relayx.cc/changelog/0.12.21) | [changelog/0-12-21/index.md](changelog/0-12-21/index.md) |
| 0.12.20 | [https://docs.relayx.cc/changelog/0.12.20](https://docs.relayx.cc/changelog/0.12.20) | [changelog/0-12-20/index.md](changelog/0-12-20/index.md) |
| 0.12.19 | [https://docs.relayx.cc/changelog/0.12.19](https://docs.relayx.cc/changelog/0.12.19) | [changelog/0-12-19/index.md](changelog/0-12-19/index.md) |
| 0.12.18 | [https://docs.relayx.cc/changelog/0.12.18](https://docs.relayx.cc/changelog/0.12.18) | [changelog/0-12-18/index.md](changelog/0-12-18/index.md) |
| 0.12.17 | [https://docs.relayx.cc/changelog/0.12.17](https://docs.relayx.cc/changelog/0.12.17) | [changelog/0-12-17/index.md](changelog/0-12-17/index.md) |
| 0.12.16 | [https://docs.relayx.cc/changelog/0.12.16](https://docs.relayx.cc/changelog/0.12.16) | [changelog/0-12-16/index.md](changelog/0-12-16/index.md) |
| 0.12.15 | [https://docs.relayx.cc/changelog/0.12.15](https://docs.relayx.cc/changelog/0.12.15) | [changelog/0-12-15/index.md](changelog/0-12-15/index.md) |
| 0.12.14 | [https://docs.relayx.cc/changelog/0.12.14](https://docs.relayx.cc/changelog/0.12.14) | [changelog/0-12-14/index.md](changelog/0-12-14/index.md) |
| 0.12.13 | [https://docs.relayx.cc/changelog/0.12.13](https://docs.relayx.cc/changelog/0.12.13) | [changelog/0-12-13/index.md](changelog/0-12-13/index.md) |
| 0.12.12 | [https://docs.relayx.cc/changelog/0.12.12](https://docs.relayx.cc/changelog/0.12.12) | [changelog/0-12-12/index.md](changelog/0-12-12/index.md) |
| 0.12.11 | [https://docs.relayx.cc/changelog/0.12.11](https://docs.relayx.cc/changelog/0.12.11) | [changelog/0-12-11/index.md](changelog/0-12-11/index.md) |
| 0.12.10 | [https://docs.relayx.cc/changelog/0.12.10](https://docs.relayx.cc/changelog/0.12.10) | [changelog/0-12-10/index.md](changelog/0-12-10/index.md) |
| 0.12.9 | [https://docs.relayx.cc/changelog/0.12.9](https://docs.relayx.cc/changelog/0.12.9) | [changelog/0-12-9/index.md](changelog/0-12-9/index.md) |
| 0.12.8 | [https://docs.relayx.cc/changelog/0.12.8](https://docs.relayx.cc/changelog/0.12.8) | [changelog/0-12-8/index.md](changelog/0-12-8/index.md) |
| 0.12.7 | [https://docs.relayx.cc/changelog/0.12.7](https://docs.relayx.cc/changelog/0.12.7) | [changelog/0-12-7/index.md](changelog/0-12-7/index.md) |
| 0.12.6 | [https://docs.relayx.cc/changelog/0.12.6](https://docs.relayx.cc/changelog/0.12.6) | [changelog/0-12-6/index.md](changelog/0-12-6/index.md) |
| 0.12.5 | [https://docs.relayx.cc/changelog/0.12.5](https://docs.relayx.cc/changelog/0.12.5) | [changelog/0-12-5/index.md](changelog/0-12-5/index.md) |
| 0.12.4 | [https://docs.relayx.cc/changelog/0.12.4](https://docs.relayx.cc/changelog/0.12.4) | [changelog/0-12-4/index.md](changelog/0-12-4/index.md) |
| 0.12.3 | [https://docs.relayx.cc/changelog/0.12.3](https://docs.relayx.cc/changelog/0.12.3) | [changelog/0-12-3/index.md](changelog/0-12-3/index.md) |
| 0.12.2 | [https://docs.relayx.cc/changelog/0.12.2](https://docs.relayx.cc/changelog/0.12.2) | [changelog/0-12-2/index.md](changelog/0-12-2/index.md) |
| 0.12.1 | [https://docs.relayx.cc/changelog/0.12.1](https://docs.relayx.cc/changelog/0.12.1) | [changelog/0-12-1/index.md](changelog/0-12-1/index.md) |
| 0.12.0 | [https://docs.relayx.cc/changelog/0.12.0](https://docs.relayx.cc/changelog/0.12.0) | [changelog/0-12-0/index.md](changelog/0-12-0/index.md) |
| 0.11.14 | [https://docs.relayx.cc/changelog/0.11.14](https://docs.relayx.cc/changelog/0.11.14) | [changelog/0-11-14/index.md](changelog/0-11-14/index.md) |
| 0.11.13 | [https://docs.relayx.cc/changelog/0.11.13](https://docs.relayx.cc/changelog/0.11.13) | [changelog/0-11-13/index.md](changelog/0-11-13/index.md) |
| 0.11.12 | [https://docs.relayx.cc/changelog/0.11.12](https://docs.relayx.cc/changelog/0.11.12) | [changelog/0-11-12/index.md](changelog/0-11-12/index.md) |
| 0.11.11 | [https://docs.relayx.cc/changelog/0.11.11](https://docs.relayx.cc/changelog/0.11.11) | [changelog/0-11-11/index.md](changelog/0-11-11/index.md) |
| 0.11.10 | [https://docs.relayx.cc/changelog/0.11.10](https://docs.relayx.cc/changelog/0.11.10) | [changelog/0-11-10/index.md](changelog/0-11-10/index.md) |
| 0.11.9 | [https://docs.relayx.cc/changelog/0.11.9](https://docs.relayx.cc/changelog/0.11.9) | [changelog/0-11-9/index.md](changelog/0-11-9/index.md) |
| 0.11.8 | [https://docs.relayx.cc/changelog/0.11.8](https://docs.relayx.cc/changelog/0.11.8) | [changelog/0-11-8/index.md](changelog/0-11-8/index.md) |
| 0.11.7 | [https://docs.relayx.cc/changelog/0.11.7](https://docs.relayx.cc/changelog/0.11.7) | [changelog/0-11-7/index.md](changelog/0-11-7/index.md) |
| 0.11.6 | [https://docs.relayx.cc/changelog/0.11.6](https://docs.relayx.cc/changelog/0.11.6) | [changelog/0-11-6/index.md](changelog/0-11-6/index.md) |
| 0.11.5 | [https://docs.relayx.cc/changelog/0.11.5](https://docs.relayx.cc/changelog/0.11.5) | [changelog/0-11-5/index.md](changelog/0-11-5/index.md) |
| 0.11.4 | [https://docs.relayx.cc/changelog/0.11.4](https://docs.relayx.cc/changelog/0.11.4) | [changelog/0-11-4/index.md](changelog/0-11-4/index.md) |
| 0.11.3 | [https://docs.relayx.cc/changelog/0.11.3](https://docs.relayx.cc/changelog/0.11.3) | [changelog/0-11-3/index.md](changelog/0-11-3/index.md) |
| 0.11.2 | [https://docs.relayx.cc/changelog/0.11.2](https://docs.relayx.cc/changelog/0.11.2) | [changelog/0-11-2/index.md](changelog/0-11-2/index.md) |
| 0.11.1 | [https://docs.relayx.cc/changelog/0.11.1](https://docs.relayx.cc/changelog/0.11.1) | [changelog/0-11-1/index.md](changelog/0-11-1/index.md) |
| 0.11.0 | [https://docs.relayx.cc/changelog/0.11.0](https://docs.relayx.cc/changelog/0.11.0) | [changelog/0-11-0/index.md](changelog/0-11-0/index.md) |
| 0.10.9 | [https://docs.relayx.cc/changelog/0.10.9](https://docs.relayx.cc/changelog/0.10.9) | [changelog/0-10-9/index.md](changelog/0-10-9/index.md) |
| 0.10.8 | [https://docs.relayx.cc/changelog/0.10.8](https://docs.relayx.cc/changelog/0.10.8) | [changelog/0-10-8/index.md](changelog/0-10-8/index.md) |
| 0.10.7 | [https://docs.relayx.cc/changelog/0.10.7](https://docs.relayx.cc/changelog/0.10.7) | [changelog/0-10-7/index.md](changelog/0-10-7/index.md) |
| 0.10.6 | [https://docs.relayx.cc/changelog/0.10.6](https://docs.relayx.cc/changelog/0.10.6) | [changelog/0-10-6/index.md](changelog/0-10-6/index.md) |
| 0.10.5 | [https://docs.relayx.cc/changelog/0.10.5](https://docs.relayx.cc/changelog/0.10.5) | [changelog/0-10-5/index.md](changelog/0-10-5/index.md) |
| 0.10.4 | [https://docs.relayx.cc/changelog/0.10.4](https://docs.relayx.cc/changelog/0.10.4) | [changelog/0-10-4/index.md](changelog/0-10-4/index.md) |
| 0.10.3 | [https://docs.relayx.cc/changelog/0.10.3](https://docs.relayx.cc/changelog/0.10.3) | [changelog/0-10-3/index.md](changelog/0-10-3/index.md) |
| 0.10.2 | [https://docs.relayx.cc/changelog/0.10.2](https://docs.relayx.cc/changelog/0.10.2) | [changelog/0-10-2/index.md](changelog/0-10-2/index.md) |
| 0.10.1 | [https://docs.relayx.cc/changelog/0.10.1](https://docs.relayx.cc/changelog/0.10.1) | [changelog/0-10-1/index.md](changelog/0-10-1/index.md) |
| 0.10.0 | [https://docs.relayx.cc/changelog/0.10.0](https://docs.relayx.cc/changelog/0.10.0) | [changelog/0-10-0/index.md](changelog/0-10-0/index.md) |
| 0.9.6 | [https://docs.relayx.cc/changelog/0.9.6](https://docs.relayx.cc/changelog/0.9.6) | [changelog/0-9-6/index.md](changelog/0-9-6/index.md) |
| 0.9.5 | [https://docs.relayx.cc/changelog/0.9.5](https://docs.relayx.cc/changelog/0.9.5) | [changelog/0-9-5/index.md](changelog/0-9-5/index.md) |
| 0.9.4 | [https://docs.relayx.cc/changelog/0.9.4](https://docs.relayx.cc/changelog/0.9.4) | [changelog/0-9-4/index.md](changelog/0-9-4/index.md) |
| 0.9.3 | [https://docs.relayx.cc/changelog/0.9.3](https://docs.relayx.cc/changelog/0.9.3) | [changelog/0-9-3/index.md](changelog/0-9-3/index.md) |
| 0.9.2 | [https://docs.relayx.cc/changelog/0.9.2](https://docs.relayx.cc/changelog/0.9.2) | [changelog/0-9-2/index.md](changelog/0-9-2/index.md) |
| 0.9.1 | [https://docs.relayx.cc/changelog/0.9.1](https://docs.relayx.cc/changelog/0.9.1) | [changelog/0-9-1/index.md](changelog/0-9-1/index.md) |
| 0.9.0 | [https://docs.relayx.cc/changelog/0.9.0](https://docs.relayx.cc/changelog/0.9.0) | [changelog/0-9-0/index.md](changelog/0-9-0/index.md) |
| 0.8.2 | [https://docs.relayx.cc/changelog/0.8.2](https://docs.relayx.cc/changelog/0.8.2) | [changelog/0-8-2/index.md](changelog/0-8-2/index.md) |
| 0.8.1 | [https://docs.relayx.cc/changelog/0.8.1](https://docs.relayx.cc/changelog/0.8.1) | [changelog/0-8-1/index.md](changelog/0-8-1/index.md) |
| 0.8.0 | [https://docs.relayx.cc/changelog/0.8.0](https://docs.relayx.cc/changelog/0.8.0) | [changelog/0-8-0/index.md](changelog/0-8-0/index.md) |
| 0.7.3 | [https://docs.relayx.cc/changelog/0.7.3](https://docs.relayx.cc/changelog/0.7.3) | [changelog/0-7-3/index.md](changelog/0-7-3/index.md) |
| 0.7.2 | [https://docs.relayx.cc/changelog/0.7.2](https://docs.relayx.cc/changelog/0.7.2) | [changelog/0-7-2/index.md](changelog/0-7-2/index.md) |
| 0.7.1 | [https://docs.relayx.cc/changelog/0.7.1](https://docs.relayx.cc/changelog/0.7.1) | [changelog/0-7-1/index.md](changelog/0-7-1/index.md) |
| 0.7.0 | [https://docs.relayx.cc/changelog/0.7.0](https://docs.relayx.cc/changelog/0.7.0) | [changelog/0-7-0/index.md](changelog/0-7-0/index.md) |
| 0.6.1 | [https://docs.relayx.cc/changelog/0.6.1](https://docs.relayx.cc/changelog/0.6.1) | [changelog/0-6-1/index.md](changelog/0-6-1/index.md) |
| 0.6.0 | [https://docs.relayx.cc/changelog/0.6.0](https://docs.relayx.cc/changelog/0.6.0) | [changelog/0-6-0/index.md](changelog/0-6-0/index.md) |
| 0.5.0 | [https://docs.relayx.cc/changelog/0.5.0](https://docs.relayx.cc/changelog/0.5.0) | [changelog/0-5-0/index.md](changelog/0-5-0/index.md) |
| 0.4.0 | [https://docs.relayx.cc/changelog/0.4.0](https://docs.relayx.cc/changelog/0.4.0) | [changelog/0-4-0/index.md](changelog/0-4-0/index.md) |
| 0.3.1 | [https://docs.relayx.cc/changelog/0.3.1](https://docs.relayx.cc/changelog/0.3.1) | [changelog/0-3-1/index.md](changelog/0-3-1/index.md) |
| 0.3.0 | [https://docs.relayx.cc/changelog/0.3.0](https://docs.relayx.cc/changelog/0.3.0) | [changelog/0-3-0/index.md](changelog/0-3-0/index.md) |
| 0.2.1 | [https://docs.relayx.cc/changelog/0.2.1](https://docs.relayx.cc/changelog/0.2.1) | [changelog/0-2-1/index.md](changelog/0-2-1/index.md) |
| 0.2.0 | [https://docs.relayx.cc/changelog/0.2.0](https://docs.relayx.cc/changelog/0.2.0) | [changelog/0-2-0/index.md](changelog/0-2-0/index.md) |
| 0.1.0 | [https://docs.relayx.cc/changelog/0.1.0](https://docs.relayx.cc/changelog/0.1.0) | [changelog/0-1-0/index.md](changelog/0-1-0/index.md) |

## 目录树

```text
relayx-docs/
├── README.md
├── index.md
├── blog/
│   ├── index.md
│   ├── archive/
│   │   └── index.md
│   └── index/
│       └── index.md
├── changelog/
│   ├── index.md
│   ├── 0-1-0/
│   │   └── index.md
│   ├── 0-10-0/
│   │   └── index.md
│   ├── 0-10-1/
│   │   └── index.md
│   ├── 0-10-2/
│   │   └── index.md
│   ├── 0-10-3/
│   │   └── index.md
│   ├── 0-10-4/
│   │   └── index.md
│   ├── 0-10-5/
│   │   └── index.md
│   ├── 0-10-6/
│   │   └── index.md
│   ├── 0-10-7/
│   │   └── index.md
│   ├── 0-10-8/
│   │   └── index.md
│   ├── 0-10-9/
│   │   └── index.md
│   ├── 0-11-0/
│   │   └── index.md
│   ├── 0-11-1/
│   │   └── index.md
│   ├── 0-11-10/
│   │   └── index.md
│   ├── 0-11-11/
│   │   └── index.md
│   ├── 0-11-12/
│   │   └── index.md
│   ├── 0-11-13/
│   │   └── index.md
│   ├── 0-11-14/
│   │   └── index.md
│   ├── 0-11-2/
│   │   └── index.md
│   ├── 0-11-3/
│   │   └── index.md
│   ├── 0-11-4/
│   │   └── index.md
│   ├── 0-11-5/
│   │   └── index.md
│   ├── 0-11-6/
│   │   └── index.md
│   ├── 0-11-7/
│   │   └── index.md
│   ├── 0-11-8/
│   │   └── index.md
│   ├── 0-11-9/
│   │   └── index.md
│   ├── 0-12-0/
│   │   └── index.md
│   ├── 0-12-1/
│   │   └── index.md
│   ├── 0-12-10/
│   │   └── index.md
│   ├── 0-12-11/
│   │   └── index.md
│   ├── 0-12-12/
│   │   └── index.md
│   ├── 0-12-13/
│   │   └── index.md
│   ├── 0-12-14/
│   │   └── index.md
│   ├── 0-12-15/
│   │   └── index.md
│   ├── 0-12-16/
│   │   └── index.md
│   ├── 0-12-17/
│   │   └── index.md
│   ├── 0-12-18/
│   │   └── index.md
│   ├── 0-12-19/
│   │   └── index.md
│   ├── 0-12-2/
│   │   └── index.md
│   ├── 0-12-20/
│   │   └── index.md
│   ├── 0-12-21/
│   │   └── index.md
│   ├── 0-12-22/
│   │   └── index.md
│   ├── 0-12-23/
│   │   └── index.md
│   ├── 0-12-24/
│   │   └── index.md
│   ├── 0-12-25/
│   │   └── index.md
│   ├── 0-12-26/
│   │   └── index.md
│   ├── 0-12-27/
│   │   └── index.md
│   ├── 0-12-28/
│   │   └── index.md
│   ├── 0-12-29/
│   │   └── index.md
│   ├── 0-12-3/
│   │   └── index.md
│   ├── 0-12-30/
│   │   └── index.md
│   ├── 0-12-4/
│   │   └── index.md
│   ├── 0-12-5/
│   │   └── index.md
│   ├── 0-12-6/
│   │   └── index.md
│   ├── 0-12-7/
│   │   └── index.md
│   ├── 0-12-8/
│   │   └── index.md
│   ├── 0-12-9/
│   │   └── index.md
│   ├── 0-13-0/
│   │   └── index.md
│   ├── 0-13-1/
│   │   └── index.md
│   ├── 0-13-10/
│   │   └── index.md
│   ├── 0-13-11/
│   │   └── index.md
│   ├── 0-13-12/
│   │   └── index.md
│   ├── 0-13-13/
│   │   └── index.md
│   ├── 0-13-2/
│   │   └── index.md
│   ├── 0-13-3/
│   │   └── index.md
│   ├── 0-13-4/
│   │   └── index.md
│   ├── 0-13-5/
│   │   └── index.md
│   ├── 0-13-6/
│   │   └── index.md
│   ├── 0-13-7/
│   │   └── index.md
│   ├── 0-13-8/
│   │   └── index.md
│   ├── 0-13-9/
│   │   └── index.md
│   ├── 0-14-0/
│   │   └── index.md
│   ├── 0-14-1/
│   │   └── index.md
│   ├── 0-14-2/
│   │   └── index.md
│   ├── 0-2-0/
│   │   └── index.md
│   ├── 0-2-1/
│   │   └── index.md
│   ├── 0-3-0/
│   │   └── index.md
│   ├── 0-3-1/
│   │   └── index.md
│   ├── 0-4-0/
│   │   └── index.md
│   ├── 0-5-0/
│   │   └── index.md
│   ├── 0-6-0/
│   │   └── index.md
│   ├── 0-6-1/
│   │   └── index.md
│   ├── 0-7-0/
│   │   └── index.md
│   ├── 0-7-1/
│   │   └── index.md
│   ├── 0-7-2/
│   │   └── index.md
│   ├── 0-7-3/
│   │   └── index.md
│   ├── 0-8-0/
│   │   └── index.md
│   ├── 0-8-1/
│   │   └── index.md
│   ├── 0-8-2/
│   │   └── index.md
│   ├── 0-9-0/
│   │   └── index.md
│   ├── 0-9-1/
│   │   └── index.md
│   ├── 0-9-2/
│   │   └── index.md
│   ├── 0-9-3/
│   │   └── index.md
│   ├── 0-9-4/
│   │   └── index.md
│   ├── 0-9-5/
│   │   └── index.md
│   ├── 0-9-6/
│   │   └── index.md
│   └── page/
│       ├── 2/
│       │   └── index.md
│       ├── 3/
│       │   └── index.md
│       ├── 4/
│       │   └── index.md
│       └── 5/
│           └── index.md
├── deploy/
│   ├── config/
│   │   ├── index.md
│   │   ├── node-group/
│   │   │   └── index.md
│   │   ├── payment/
│   │   │   └── index.md
│   │   ├── plan/
│   │   │   └── index.md
│   │   ├── rbac/
│   │   │   └── index.md
│   │   └── site/
│   │       └── index.md
│   ├── deploy-dash/
│   │   └── index.md
│   ├── intro/
│   │   └── index.md
│   └── prepare/
│       ├── index.md
│       ├── database/
│       │   └── index.md
│       ├── intro/
│       │   └── index.md
│       ├── payment/
│       │   └── index.md
│       ├── smtp/
│       │   └── index.md
│       ├── stackauth/
│       │   └── index.md
│       └── telegram/
│           └── index.md
└── guide/
    ├── advanced/
    │   ├── api/
    │   │   └── index.md
    │   ├── protocol/
    │   │   └── index.md
    │   └── tunnel-chain/
    │       └── index.md
    ├── faq/
    │   └── index.md
    ├── getting-started/
    │   ├── index.md
    │   ├── account/
    │   │   └── index.md
    │   ├── deploy-node/
    │   │   └── index.md
    │   ├── dns/
    │   │   └── index.md
    │   ├── login/
    │   │   └── index.md
    │   ├── node-group/
    │   │   └── index.md
    │   ├── plan/
    │   │   └── index.md
    │   ├── referral/
    │   │   └── index.md
    │   ├── ticket/
    │   │   └── index.md
    │   └── tunnel/
    │       └── index.md
    ├── intro/
    │   └── index.md
    └── type/
        └── index.md
```
