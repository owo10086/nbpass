---
name: nbpass 管理面板
description: 单管理员的转发与节点运维控制台——状态先于操作，按 Vercel / Linear 控制台精度执行
colors:
  bg: "#ffffff"
  bg-subtle: "#fafafa"
  bg-sidebar: "#f7f7f8"
  bg-hover: "#f0f0f2"
  line: "#e8e8eb"
  text: "#18181b"
  text-2: "#5f5f67"
  text-3: "#75757d"
  muted: "#a1a1aa"
  accent: "#2563eb"
  accent-soft: "#eef3fe"
  ok: "#16a34a"
  warn: "#d97706"
  danger: "#dc2626"
typography:
  headline:
    fontFamily: "-apple-system, BlinkMacSystemFont, 'SF Pro Text', 'PingFang SC', 'Segoe UI', 'Microsoft YaHei', sans-serif"
    fontSize: "20px"
    fontWeight: 600
    letterSpacing: "-0.01em"
  title:
    fontFamily: "-apple-system, BlinkMacSystemFont, 'SF Pro Text', 'PingFang SC', 'Segoe UI', 'Microsoft YaHei', sans-serif"
    fontSize: "14px"
    fontWeight: 600
  body:
    fontFamily: "-apple-system, BlinkMacSystemFont, 'SF Pro Text', 'PingFang SC', 'Segoe UI', 'Microsoft YaHei', sans-serif"
    fontSize: "14px"
    fontWeight: 400
  label:
    fontFamily: "'SF Mono', 'JetBrains Mono', ui-monospace, Menlo, Consolas, monospace"
    fontSize: "12.5px"
rounded:
  sm: "6px"
  md: "8px"
spacing:
  xs: "8px"
  sm: "12px"
  md: "16px"
  lg: "24px"
  xl: "32px"
components:
  button-primary:
    backgroundColor: "{colors.accent}"
    textColor: "#ffffff"
    rounded: "{rounded.sm}"
    height: "32px"
  nav-item:
    backgroundColor: "transparent"
    textColor: "{colors.text-2}"
    rounded: "{rounded.sm}"
    height: "32px"
  nav-item-active:
    backgroundColor: "{colors.bg}"
    textColor: "{colors.text}"
    rounded: "{rounded.sm}"
    height: "32px"
  panel:
    backgroundColor: "{colors.bg}"
    rounded: "{rounded.md}"
    padding: "12px 16px"
  stat-tile:
    backgroundColor: "{colors.bg}"
    textColor: "{colors.text}"
    padding: "14px 18px"
  tag-neutral:
    backgroundColor: "#f4f4f5"
    textColor: "{colors.text-2}"
    rounded: "4px"
---

# Design System: nbpass 管理面板

## Overview

**Creative North Star: "控制台精度（Console Precision）"**

nbpass 是单个管理员用来巡检与配置转发节点的后台，方向合同（`.impeccable/surfaces/web-src-main-tsx.md`）明确拒绝 AntD 默认的大圆角卡片堆叠、深色侧栏菜单和空欢迎页，转而按 Vercel / Linear 控制台的精度执行：白底工作区、1px 细线分区、克制的字号阶梯、状态先于操作。整个系统只有一种强调色（蓝 `#2563eb`），其余全部是中性灰阶和四种语义状态色；这种单色克制与方向合同中"唯一强调色"的要求完全一致。

版式是致密的数据工作台，而不是营销式的留白页面：表格行高约 40px，数字一律 tabular-nums 对齐，IP、端口、版本号等标识符改用等宽字体，是一套面向"天天盯着看"的专业管理员、而非新用户引导的视觉语言。阴影被压缩到几乎为零——卡片、面板在静止状态下完全扁平，仅登录卡片和浮层（Modal/Drawer）两处使用同一量级的环境阴影，层级关系主要靠 1px 线和背景色深浅表达，而不是靠投影堆叠。

**Key Characteristics:**
- 单一强调色（`#2563eb`）只用于主按钮、链接、选中态和聚焦环，其余界面全部中性灰阶
- 状态一律用"圆点 + 文字"表达，从不单独依赖颜色
- 圆角只有两档（6px / 8px），表格与侧栏导航几乎零阴影
- 数字等宽对齐（tabular-nums），IP/端口/版本号等标识符用等宽字体
- 窄屏（≤860px）侧栏导航折叠为页面顶部的横向导航条

## Colors

调色板以白底工作区为主，侧栏比工作区略冷一级灰，强调色与四种语义色点到为止。

### Primary
- **Accent Blue** (`#2563eb`)：唯一的强调色，用于主按钮背景、链接色（`colorLink`）、选中/聚焦环（`:focus-visible` 2px 描边）、侧栏数量角标里的"info"圆点，以及表格行跳转高亮淡出前的瞬时底色。
- **Accent Soft** (`#eef3fe`)：强调色的 10% 左右浅色版本，仅用于表格行从概览跳转后的瞬时高亮背景（`.row-focus`），随后在 150ms 内淡出。

### Neutral
- **White** (`#ffffff`)：主工作区背景、面板（`.panel`）、登录卡片、表格行背景。
- **Subtle Gray** (`#fafafa`)：次级背景——登录页整体背景、表格表头背景（`Table.headerBg`）、统计方格/关注列表项的 hover 态背景。
- **Sidebar Gray** (`#f7f7f8`)：侧栏背景，比工作区白略冷一级，与方向合同"侧栏是比工作区略冷的浅灰"一致。
- **Hover Gray** (`#f0f0f2`)：导航项 hover 背景。
- **Hairline** (`#e8e8eb`)：系统内几乎所有分区线——侧栏右边线、面板/卡片边框、表格行分隔线、设置分节的顶部分隔线、输入框边框（`colorBorder`/`colorBorderSecondary`）。
- **Ink Text** (`#18181b`)：主文字色，同时是品牌符号「n」方块的背景色（`.brand-mark`）。
- **Secondary Text** (`#5f5f67`)：次级文字——描述文案、导航未选中态文字、表格表头文字。
- **Tertiary Text** (`#75757d`)：三级文字，用于更弱的说明性文字（如 `page-header p` 的部分场景与 `muted` 类）。
- **Muted** (`#a1a1aa`)：中性状态点（离线/未知之外的默认圆点色）与滚动条颜色。

### 语义状态色
- **Ok Green** (`#16a34a`)：正常/在线状态点、成功提示（`Alert type="success"`）。
- **Warn Amber** (`#d97706`)：处理中/待定状态点、警告提示。
- **Danger Red** (`#dc2626`)：需要人工介入的状态点（离线态圆点额外带 `0 0 0 3px rgb(220 38 38 / 14%)` 的光晕）、错误提示、危险操作按钮文字。

探针曲线图（`ProbeDetails.tsx` 内联 SVG）同样只用系统色：平均值线与时间滑块用强调蓝，最大值点用 `warn`，网格线用 `--line`，游标竖线用 `--muted`。

### Named Rules
**The One Accent Rule.** 整个面板只有一种强调色（`#2563eb`），且只出现在主操作、链接与选中/聚焦态上；其余一切——包括全部文字、边框、背景——都是中性灰阶或四种语义状态色之一，不存在第二种品牌色。

**The Dot-Plus-Text Rule.** 任何状态（节点在线/离线/维护、规则启停、同步/就绪情况）都渲染为"小圆点 + 文字"，颜色永远是辅助而非唯一载体；连 AntD 彩色 `Tag` 也被全局样式改写为"透明背景 + 圆点前缀"，不再使用色块标签（`style.css` 第 129–136 行）。

## Typography

**界面字体：** `-apple-system, BlinkMacSystemFont, 'SF Pro Text', 'PingFang SC', 'Segoe UI', 'Microsoft YaHei', sans-serif`
**等宽字体：** `'SF Mono', 'JetBrains Mono', ui-monospace, Menlo, Consolas, monospace`

**字符：** 系统字体优先、克制的字号阶梯（18–22px 之间只有三档），没有超大标题或装饰性字重，是一套为长时间盯着数据表格而设计的工作台字体系统。

### Hierarchy
- **Headline**（600, 20px, 字距 -0.01em）：页面标题（`PageHeader` 的 `<h1>`）与设置页标题（`.ant-typography h2`），两者字号完全一致，是整个面板唯一的"大字号"。
- **Title**（600, 14–15px）：面板/分节小标题——`.panel-head h2`、`.settings-section h2` 为 14px，`.section-title h2` 为 15px（见下方"未归并的漂移"）。
- **Body**（400, 14px）：AntD 全局基准字号（`token.fontSize: 14`），覆盖表单、表格单元格默认文字。
- **Body · Secondary**（400, 13px，色 `--text-2`）：页头描述、设置分节说明、表单 `extra` 提示等次级说明文字，统一比正文小一档、颜色更浅。
- **Label / Mono**（400, 12–12.5px）：等宽场景——IP、端口、版本号（`.mono` 12.5px）、表格次级小字（`.cell-sub` 12px）、脚注（`.footnote` 12.5px），颜色多为 `--text-3`。

### Named Rules
**The Tabular Numerals Rule.** 所有数字（表格、`Statistic` 风格数值、时间戳）通过 `.num`/`.ant-table`/`.ant-statistic` 统一应用 `font-variant-numeric: tabular-nums`，保证列对齐；IP、端口、版本号等标识符额外切换到等宽字体（`.mono`），两者是两条分开但配套的规则。

## Layout

工作区是左侧固定宽度侧栏 + 右侧内容区的两栏网格（`.shell { grid-template-columns: 232px minmax(0,1fr) }`），侧栏 `position: sticky` 贴顶、占满视口高度。内容区 `.page` 居中，`max-width: 1440px`，内边距 `28px 32px 48px`（桌面）。

方向合同里原计划的"约 56px 顶栏"在实现中被省掉，改为 Linear 式做法：页面标题、描述和右上角主操作都收进每页内容顶部的 `PageHeader`（`.page-header`），账户入口常驻侧栏底部（`.account`），不设独立的常驻横条——这是方向合同自己记录的"已采用改编"，不是未对齐的漂移。

设置类页面用「左侧 260px 说明、右侧表单」的两栏分节（`.settings-section { grid-template-columns: 260px minmax(0,1fr); gap: 32px }`），分节之间以 1px 顶部细线分隔（紧随页头或提示条的分节会取消这条线，避免双线）。

响应式断点：
- **≤1100px**：概览页的左右两栏（关注列表／最近事件）收为单栏纵向堆叠。
- **≤860px**：侧栏从竖直固定栏变为页面顶部的横向导航条（`display: flex; overflow-x: auto`），分组标题隐藏，账户入口随之右移到导航行尾；内容区内边距收紧为 `20px 16px 40px`；概览的四格统计从 4 列变 2×2。
- **≤640px**：表格的固定右侧操作列改为非固定（`position: static`），横向滚动时把宽度优先让给数据列，而不是常驻一列操作按钮。

间距节奏以 8 的倍数为主（8 / 12 / 16 / 24 / 32px 最常见），但多处组件内边距是未归并的一次性值（14px 18px 的统计格、10px 16px 的面板头、20px/28px 的登录卡片），这些值没有被收进一套命名的间距刻度，详见下方漂移记录。

## Elevation & Depth

系统几乎是纯平面的：AntD 按钮全局关闭阴影（`primaryShadow/defaultShadow/dangerShadow: 'none'`），`.ant-card { box-shadow: none }` 把卡片的默认投影也去掉。层级靠三样东西表达：背景深浅（白 vs 浅灰）、1px 分隔线，以及被选中/聚焦的描边。

阴影只保留在两处克制的场景里，且两处的具体数值并不完全统一（见下方漂移记录）：

### Shadow Vocabulary
- **登录卡片环境阴影**（`.auth-card`：`box-shadow: 0 1px 2px rgb(0 0 0 / 4%), 0 8px 24px -12px rgb(0 0 0 / 8%)`）：唯一在静态卡片上使用阴影的地方，用于把登录卡片从背景中托起。
- **浮层次级阴影**（AntD 主题 `token.boxShadowSecondary: '0 1px 2px rgb(0 0 0 / 4%), 0 8px 24px -8px rgb(0 0 0 / 14%)'`）：供 Modal、Drawer 等浮层使用。
- **选中导航项描边**（`.nav-item[aria-current='page']`：`box-shadow: 0 0 0 1px var(--line), 0 1px 2px rgb(0 0 0 / 4%)`）：当前页侧栏项用极轻的描边+阴影和同级项区分，而不是改变背景色块的圆角卡片。
- **离线圆点光晕**（`.dot.danger`：`box-shadow: 0 0 0 3px rgb(220 38 38 / 14%)`）：唯一附带光晕的状态点，用于让"需要人工介入"的红点比其余圆点更醒目。

### Named Rules
**The Near-Flat Rule.** 阴影不是默认状态的一部分；只有登录卡片、浮层和两个状态强调点四处使用阴影，其余所有卡片、面板、按钮在静止状态下都是零阴影、靠边框和背景色分层。

## Shapes

圆角只有两档：`--radius: 6px`（按钮、输入框、导航项、安装命令代码块）与 AntD 的 `borderRadiusLG: 8px`（面板 `.panel`、统计方格容器 `.stats`、抽屉内的卡片、登录卡片）。状态点和未读角标统一用全圆角（`border-radius: 50%` 或胶囊形），品牌符号方块是 5px 圆角的小方块。边框统一是 1px 细线（`--line`），不使用粗边框或虚线作为常规语言。

## Components

### Buttons
- **Shape：** 6px 圆角（`borderRadius: 6`），高度 32px（`controlHeight: 32`）。
- **Primary：** 背景 `#2563eb`，白字，字重 500（`fontWeight: 500`），零阴影。
- **Default / Text / Danger：** 沿用 AntD 默认描边与文字色，同样零阴影；表格行内操作按钮统一降权为 `type="text"`，默认文字色 `--text-2`，hover 变 `--text`，危险操作（吊销 token、删除）文字变 `--danger`。

### Tags / 状态标签
- **语义色标签**（green/orange·gold/red/blue 等）：全局改写为透明背景、零边框、前置 7px 圆点 + 文字（`color: var(--text)`），不再是色块标签。
- **无语义标签**（默认 default）：保留为中性浅灰标签（背景 `#f4f4f5`，透明边框，文字 `--text-2`），不加圆点。

### 状态点（Status Dot）
系统里最高频的信号单元：7px 圆点，默认灰（`--muted`），按状态切换为绿（`ok`）/ 琥珀（`warn`）/ 红（`danger`，带光晕）/ 蓝（`info`）。表格、概览统计格、关注列表、节点列表的"状态"列全部复用同一套点色，不单独发明新状态色。

### Panels / Cards
- **Corner Style：** 8px 圆角。
- **Background：** 白底，1px `--line` 边框，`overflow: hidden`。
- **Shadow Strategy：** 零阴影（见 Elevation）。
- **结构：** 统一的 `panel-head`（标题 + 右侧次级操作）／内容／可选 `panel-foot`（居中，常用于"加载更多"）三段式，面板头与内容之间用 1px 线分隔。

### Inputs / Fields
- **Style：** 1px 描边，6px 圆角，高度 32px，与按钮同一控件高度基准。
- **Focus：** 全局 `:focus-visible` 用 2px 强调蓝描边、2px 偏移，不使用发光阴影。

### Table
- 表头浅灰背景（`#fafafa`）、次级文字色、表头分隔线透明（`headerSplitColor: transparent`），行内边距 10px/12px；hover 背景与表头同色；窄屏下操作列不再固定在右侧。
- 行高亮是状态而非动效：从概览跳转后目标行立即套用 `--accent-soft` 背景，保持约 2.4 秒，移除类名后随单元格默认的 150ms 过渡自然淡出（代码注释原话：「高亮是状态：立即着色并保持」）。

### Navigation
- 侧栏导航项：32px 高、6px 圆角、13.5px 字号，默认 `--text-2`，hover 变浅灰背景（`--bg-hover`）+ `--text`；当前页用白底 + 细描边阴影区分（见 Elevation），而非换成强调色背景。
- 导航计数角标：次级灰色数字；"概览"分组的告警计数单独用红底白字的胶囊角标（`.nav-count.alert`），是全局唯一"色块角标"的例外。
- 窄屏（≤860px）导航横向滚动，分组标题隐藏，视觉上退化为一条扁平的横向标签条。

### Overview Stat Tile（签名组件）
概览页顶部的四格统计条（`.stats`）：无圆角分隔的四等分网格，格间用 1px 竖线分隔，整体 8px 外圆角；每格可点击跳转（`cursor: pointer` + hover 背景），内容是"状态点 + 标签"在上、22px 粗体 tabular-nums 数值在下，次要分母用 14px 弱化小字（如 `1/7`）。这是整个面板里唯一把"状态点"和"大号数字"组合在一起的位置。

## Do's and Don'ts

### Do:
- **Do** 状态永远用圆点 + 文字表达，不单独依赖颜色或仅靠色块标签。
- **Do** 数字用 tabular-nums 对齐；IP、端口、版本号等标识符用等宽字体。
- **Do** 保持零阴影的默认状态，层级靠 1px 线和背景深浅表达。
- **Do** 圆角只用 6px（控件）或 8px（面板/卡片）两档。
- **Do** 强调色只用于主操作、链接、选中态和聚焦环。

### Don't:
- **Don't** 引入 AntD 默认的大圆角卡片堆叠、深色侧栏菜单，或空的营销式欢迎页——方向合同已明确拒绝。
- **Don't** 给静态卡片或按钮加投影；阴影仅保留给登录卡片、Modal/Drawer 浮层和离线状态点光晕这几个既有场景。
- **Don't** 为语义状态新增颜色；四种语义色（ok/warn/danger + 强调蓝兼 info）已覆盖所有场景。
- **Don't** 把行高亮当动效做：当前实现是"立即着色、保持、再随单元格过渡淡出"，不是渐入渐出的动画。
