<!-- Source URL: https://nyanpass.pages.dev/ | Capture date: 2026-11-10 -->

# NyanPass 官方文档本地镜像

抓取日期：**2026-11-10**。用途：为 nbpass 端口转发面板提供设计参考。

**共 30 个页面**：文档站首页 1 页、介绍与安装 6 页、参考 18 页（检查版本 1 页、面板端 7 页、节点端 5 页、杂项 5 页）、前置知识 5 页。本目录共 31 个 Markdown 文件（含本索引）。

从[官方入口](https://nyanpass.pages.dev/)出发，递归遍历全部页面的侧边栏、章节目录、正文链接及上一页 / 下一页，并与[官方搜索索引](https://nyanpass.pages.dev/search/search_index.json)核对。搜索索引共 241 条页面 / 章节记录，去除锚点后为 30 个页面；侧边栏、搜索索引及全站同域页面链接的集合完全一致，未发现额外页面。[官方 sitemap](https://nyanpass.pages.dev/sitemap.xml) 返回 HTTP 200，但 `urlset` 为空，因此以搜索索引与链接遍历共同核对覆盖范围。下表沿用原站侧边栏章节顺序，原始页面标题采用正文 H1（部分与导航名称不同）。

**正文获取方式：静态 HTML。** 站点使用 MkDocs 1.6.1 / Material for MkDocs 9.7.1，各页 HTML 的 `article.md-content__inner` 已包含完整正文；搜索索引仅用于发现和核对，无需源 Markdown 或无头浏览器。页面模板标记 `lang="en"`、搜索配置为 `en`，但实际正文为中文；未发现多语言切换、语言替代链接或需补充的英文文档版本。

移除导航、页脚、脚本、交互控件、代码行号及高亮标签；保留原始正文、标题层级、代码块、表格、配置和命令行示例。安装面板页的两种配置标签均完整展开；提示框和折叠块转为引用块，保留标题及全部内容。逐页对照原始 HTML 核对 **241 个标题、39 个代码块、16 张表格、11 个图片引用**，并核对正文文字、表格单元格及站内链接。

图片不下载，保留原始绝对 URL；正文中已收录的站内页面链接改为本地相对路径，并保留原页面锚点。其他链接保留原始 URL，不访问站外内容。所有页面请求和重定向均限制在 `nyanpass.pages.dev`；网页内容只作为不可信数据解析，未执行其中的脚本、命令或指令。

文件路径映射：`/` → `index.md`；`/<path>/` → `<path>/index.md`。保留原站目录层级，英文路径中的下划线改为连字符，例如 `/intro/install_backend/` → `intro/install-backend/index.md`。章节分组保留在下方索引中，不额外改变原 URL 目录结构。

## 抓取失败或缺失

**无。30 / 30 页面抓取成功；侧边栏、搜索索引和全站同域链接均已覆盖，未发现缺页。** 空 sitemap 属于站点自身情况，不影响页面抓取。

## 页面索引

### 文档站首页

| 原始页面标题 | 来源 URL | 本地相对路径 |
| --- | --- | --- |
| nyanpass | [https://nyanpass.pages.dev/](https://nyanpass.pages.dev/) | [index.md](index.md) |

### 介绍与安装

| 原始页面标题 | 来源 URL | 本地相对路径 |
| --- | --- | --- |
| 介绍 | [https://nyanpass.pages.dev/intro/](https://nyanpass.pages.dev/intro/) | [intro/index.md](intro/index.md) |
| 安装面板 | [https://nyanpass.pages.dev/intro/install_backend/](https://nyanpass.pages.dev/intro/install_backend/) | [intro/install-backend/index.md](intro/install-backend/index.md) |
| 云托管面板 | [https://nyanpass.pages.dev/intro/install_backend_cloud/](https://nyanpass.pages.dev/intro/install_backend_cloud/) | [intro/install-backend-cloud/index.md](intro/install-backend-cloud/index.md) |
| 升级面板端 | [https://nyanpass.pages.dev/intro/update_backend/](https://nyanpass.pages.dev/intro/update_backend/) | [intro/update-backend/index.md](intro/update-backend/index.md) |
| 对接节点端 | [https://nyanpass.pages.dev/intro/nodeclient/](https://nyanpass.pages.dev/intro/nodeclient/) | [intro/nodeclient/index.md](intro/nodeclient/index.md) |
| FAQ | [https://nyanpass.pages.dev/intro/faq/](https://nyanpass.pages.dev/intro/faq/) | [intro/faq/index.md](intro/faq/index.md) |

### 参考

| 原始页面标题 | 来源 URL | 本地相对路径 |
| --- | --- | --- |
| 检查版本 | [https://nyanpass.pages.dev/reference/check_version/](https://nyanpass.pages.dev/reference/check_version/) | [reference/check-version/index.md](reference/check-version/index.md) |

### 参考 / 面板端

| 原始页面标题 | 来源 URL | 本地相对路径 |
| --- | --- | --- |
| 面板端常用命令 | [https://nyanpass.pages.dev/reference/be_command/](https://nyanpass.pages.dev/reference/be_command/) | [reference/be-command/index.md](reference/be-command/index.md) |
| 面板端配置 | [https://nyanpass.pages.dev/reference/be_config/](https://nyanpass.pages.dev/reference/be_config/) | [reference/be-config/index.md](reference/be-config/index.md) |
| 转发规则 | [https://nyanpass.pages.dev/reference/forward_rule/](https://nyanpass.pages.dev/reference/forward_rule/) | [reference/forward-rule/index.md](reference/forward-rule/index.md) |
| 设备组设置 | [https://nyanpass.pages.dev/reference/be_dg/](https://nyanpass.pages.dev/reference/be_dg/) | [reference/be-dg/index.md](reference/be-dg/index.md) |
| 服务器状态 | [https://nyanpass.pages.dev/reference/node_status/](https://nyanpass.pages.dev/reference/node_status/) | [reference/node-status/index.md](reference/node-status/index.md) |
| 支付网关参考 | [https://nyanpass.pages.dev/reference/be_pay/](https://nyanpass.pages.dev/reference/be_pay/) | [reference/be-pay/index.md](reference/be-pay/index.md) |
| 个性定制 | [https://nyanpass.pages.dev/reference/personalization/](https://nyanpass.pages.dev/reference/personalization/) | [reference/personalization/index.md](reference/personalization/index.md) |

### 参考 / 节点端

| 原始页面标题 | 来源 URL | 本地相对路径 |
| --- | --- | --- |
| 常用命令 | [https://nyanpass.pages.dev/reference/nc_command/](https://nyanpass.pages.dev/reference/nc_command/) | [reference/nc-command/index.md](reference/nc-command/index.md) |
| 节点端环境变量 | [https://nyanpass.pages.dev/reference/nc_environment/](https://nyanpass.pages.dev/reference/nc_environment/) | [reference/nc-environment/index.md](reference/nc-environment/index.md) |
| 节点端常见疑难问题 | [https://nyanpass.pages.dev/reference/nc_wtf/](https://nyanpass.pages.dev/reference/nc_wtf/) | [reference/nc-wtf/index.md](reference/nc-wtf/index.md) |
| 有关资源限制的疑问 | [https://nyanpass.pages.dev/reference/nc_speed_limit/](https://nyanpass.pages.dev/reference/nc_speed_limit/) | [reference/nc-speed-limit/index.md](reference/nc-speed-limit/index.md) |
| 配置文件模式 | [https://nyanpass.pages.dev/reference/nc_config/](https://nyanpass.pages.dev/reference/nc_config/) | [reference/nc-config/index.md](reference/nc-config/index.md) |

### 参考 / 杂项

| 原始页面标题 | 来源 URL | 本地相对路径 |
| --- | --- | --- |
| 高频 DNS 缓存设置教程 | [https://nyanpass.pages.dev/reference/dns_cache/](https://nyanpass.pages.dev/reference/dns_cache/) | [reference/dns-cache/index.md](reference/dns-cache/index.md) |
| 连接信息与协议 | [https://nyanpass.pages.dev/reference/connect/](https://nyanpass.pages.dev/reference/connect/) | [reference/connect/index.md](reference/connect/index.md) |
| 基于 SNI 分流的转发 | [https://nyanpass.pages.dev/reference/sni_split/](https://nyanpass.pages.dev/reference/sni_split/) | [reference/sni-split/index.md](reference/sni-split/index.md) |
| CDN 隧道 | [https://nyanpass.pages.dev/reference/cdn/](https://nyanpass.pages.dev/reference/cdn/) | [reference/cdn/index.md](reference/cdn/index.md) |
| 专线怎么对接 | [https://nyanpass.pages.dev/reference/zx/](https://nyanpass.pages.dev/reference/zx/) | [reference/zx/index.md](reference/zx/index.md) |

### 前置知识

| 原始页面标题 | 来源 URL | 本地相对路径 |
| --- | --- | --- |
| 前置知识 | [https://nyanpass.pages.dev/qzzs/](https://nyanpass.pages.dev/qzzs/) | [qzzs/index.md](qzzs/index.md) |
| 反向墙 | [https://nyanpass.pages.dev/qzzs/fxq/](https://nyanpass.pages.dev/qzzs/fxq/) | [qzzs/fxq/index.md](qzzs/fxq/index.md) |
| 代理协议 | [https://nyanpass.pages.dev/qzzs/protocol/](https://nyanpass.pages.dev/qzzs/protocol/) | [qzzs/protocol/index.md](qzzs/protocol/index.md) |
| 原生 UDP 问题 | [https://nyanpass.pages.dev/qzzs/ysudp/](https://nyanpass.pages.dev/qzzs/ysudp/) | [qzzs/ysudp/index.md](qzzs/ysudp/index.md) |
| 国内跨网问题 | [https://nyanpass.pages.dev/qzzs/kw/](https://nyanpass.pages.dev/qzzs/kw/) | [qzzs/kw/index.md](qzzs/kw/index.md) |

## 目录树

```text
nyanpass-docs/
├── README.md
├── index.md
├── intro/
│   ├── index.md
│   ├── faq/
│   │   └── index.md
│   ├── install-backend/
│   │   └── index.md
│   ├── install-backend-cloud/
│   │   └── index.md
│   ├── nodeclient/
│   │   └── index.md
│   └── update-backend/
│       └── index.md
├── qzzs/
│   ├── index.md
│   ├── fxq/
│   │   └── index.md
│   ├── kw/
│   │   └── index.md
│   ├── protocol/
│   │   └── index.md
│   └── ysudp/
│       └── index.md
└── reference/
    ├── be-command/
    │   └── index.md
    ├── be-config/
    │   └── index.md
    ├── be-dg/
    │   └── index.md
    ├── be-pay/
    │   └── index.md
    ├── cdn/
    │   └── index.md
    ├── check-version/
    │   └── index.md
    ├── connect/
    │   └── index.md
    ├── dns-cache/
    │   └── index.md
    ├── forward-rule/
    │   └── index.md
    ├── nc-command/
    │   └── index.md
    ├── nc-config/
    │   └── index.md
    ├── nc-environment/
    │   └── index.md
    ├── nc-speed-limit/
    │   └── index.md
    ├── nc-wtf/
    │   └── index.md
    ├── node-status/
    │   └── index.md
    ├── personalization/
    │   └── index.md
    ├── sni-split/
    │   └── index.md
    └── zx/
        └── index.md
```
