<!-- Source URL: https://www.zeroforwarder.com/docs/agent-install/ | Capture date: 2026-11-10 -->

# 用 AI 助手安装

让 Claude Code / Codex 非交互地安装、更新面板。

让 **Claude Code / Codex** 等 AI 助手帮你全自动安装 ZFC 面板 —— 你只需提供几个值，
其余（装 Docker、申请证书、建管理员、校验）都交给 AI。

Let an AI agent (**Claude Code / Codex**) install the ZFC panel end-to-end for you —
you provide a few values, the agent handles dependencies, certificates, the admin
account and verification.

---

## 第一步：准备 / Step 1 — Prepare

1. **一台 VPS**（推荐 Debian/Ubuntu，≥2G 内存，≥40G 磁盘），用 **root** 登录。
   A VPS (Debian/Ubuntu, ≥2 GB RAM, ≥40 GB disk), logged in as **root**.
2. **两个域名**都解析（A 记录）到该 VPS 的**公网 IP**。
   Point both domains' A records to the VPS **public IP**.
   - 用 Cloudflare 的话，两个域名都设 **灰云（仅 DNS）**，不要开橙云代理。
     On Cloudflare, set both to **DNS only (grey cloud)**, not proxied.
3. 准备好 4 个值 / Have these 4 values ready:
   - `WEB_DOMAIN` — 前端域名 / frontend domain, e.g. `forward.example.com`
   - `CONTROLER_DOMAIN` — 控制器域名 / controller domain, e.g. `zf-controler.example.com`
   - `ZFC_INSTANCE_ID` — 授权 ID / license instance id
   - `ZFC_API_KEY` — API 密钥 / license api key

---

## 第二步：把提示词交给 AI / Step 2 — Hand the prompt to your AI

在你的 AI 助手里（在 VPS 上运行，或让它 SSH 到 VPS），粘贴下面这段话，并填入你的 4 个值：

In your AI agent (running on the VPS, or letting it SSH into the VPS), paste the
following and fill in your 4 values:

```text
请帮我安装 ZFC 面板。先阅读 https://www.zeroforwarder.com/llms-install.txt
并严格按其步骤执行（先 --check 预检，再 --install 安装，最后校验，把登录地址和管理员 token 文件位置告诉我；不要把 token 打印到普通日志）。我的信息：
  WEB_DOMAIN=forward.example.com
  CONTROLER_DOMAIN=zf-controler.example.com
  ZFC_INSTANCE_ID=<你的授权ID>
  ZFC_API_KEY=<你的API密钥>
  CADDY_EMAIL=<你的邮箱，可选>

Please install the ZFC panel. First read
https://www.zeroforwarder.com/llms-install.txt and follow it exactly
(pre-flight with --check, then --install, then verify and report the login URL and admin-token file location; keep the token out of ordinary logs). My values are above.
```

AI 会自动读取联网说明 `llms-install.txt`，按契约执行。
The agent fetches the online instructions and follows the contract.

---

## 它在背后做什么 / What it does under the hood

AI 助手最终会运行这一条命令（你也可以手动运行）：
The agent ultimately runs this single command (you can also run it yourself):

```bash
WEB_DOMAIN=forward.example.com \
CONTROLER_DOMAIN=zf-controler.example.com \
ZFC_INSTANCE_ID=<instance-id> \
ZFC_API_KEY=<api-key> \
CADDY_EMAIL=you@example.com \
bash <(curl -sL https://get.zeroforwarder.com/install.sh) --install -y --json
```

非交互模式特性 / Non-interactive features:

- **自动装依赖**：缺 Docker / compose / openssl / curl 时自动安装（root 下）。
  Auto-installs missing Docker / compose / openssl / curl (as root).
- **预检 `--check`**：先校验域名解析、端口、授权、依赖，**不做任何更改**。
  `--check` validates DNS, ports, license and deps **without changing anything**.
- **结构化结果**：成功/失败都输出 JSON，并写入 `.zfc_install_result.json`（权限 600）。
  Emits JSON on success/failure and writes `.zfc_install_result.json` (mode 600).
- **明确退出码**：让 AI 能精准定位并自动修复常见问题。
  Explicit exit codes so the agent can pinpoint and auto-fix common issues.

---

## 必填与可选 / Required & optional

**必填 / Required (install):** `WEB_DOMAIN`, `CONTROLER_DOMAIN`, `ZFC_INSTANCE_ID`, `ZFC_API_KEY`

**常用可选 / Common optional:**

| 变量 Env | 默认 Default | 说明 |
|---|---|---|
| `CADDY_ENABLED` | `true` | 用 Caddy 自动 HTTPS + 反代；`false` 则自行配置反代 |
| `CADDY_EMAIL` | 空 / empty | ACME 邮箱，可选；留空仍自动签证（无到期通知） |
| `POSTGRES_TYPE` / `REDIS_TYPE` | `builtin` | `builtin` 或 `external`（外部库需 `EXTERNAL_*`） |
| `TDENGINE_TYPE` | `builtin` | `builtin` / `external` / `disabled`（禁用省 ~500MB 内存） |
| `DOCKER_REGISTRY` / `IMAGE_TAG` | `hub.covm.net` / `latest` | 镜像仓库与标签 |
| `ZFC_CLEAN_INSTALL` / `ZFC_DETACH` | `0` / `0` | 等价 `--clean-install`（清空重装）/ `--detach`（后台运行） |
| `ZFC_LOG_FILE` | `./zfc-install.log` | install/update 日志文件（`--detach` 时 `tail -f` 看进度） |
| `ZFC_ALLOW_SCHEMA_FINGERPRINT_MISMATCH` | `0` | 仅 additive schema delta 时放行二进制启动（见「更新失败」） |

---

## 退出码 / Exit codes

| 码 Code | 含义 Meaning | 处理 Remediation |
|---|---|---|
| 0 | 成功 success | — |
| 10 | 缺必填输入 missing input | 补上对应变量后重跑 |
| 11 | 端口被占 port conflict | 释放端口 / 用外部库 / 关 Caddy |
| 12 | 依赖不可用 dependency | 用 root 运行或手动装依赖 |
| 13 | 授权无效 license | 核对 `ZFC_INSTANCE_ID` / `ZFC_API_KEY` |
| 14 | 数据库连接失败 db connect | 修正 `EXTERNAL_*` 凭据/连通性 |
| 15 | 管理员创建失败 admin create | 查看控制器/数据库日志后重跑 |
| 16 | 已存在安装 already installed（`--force` 在疑似完整安装上也会被挡下） | 保数据用 `--update`；重装用 `--force`/`--clean-install` |
| 17 | 配置/参数非法 bad config | 修正配置文件行或命令行参数 |
| 18 | schema 迁移失败/未完成 schema migration（`--update`） | 跑 `--doctor`；按下方「更新失败」处理 |
| 19 | 更新后启动门禁失败 post-update health | `--doctor` 读失败容器日志 |
| 20 | 镜像拉取失败 image pull（`--update`） | 查 `DOCKER_REGISTRY`/网络/凭据后重试 |

错误 JSON 可能带 `"hint"` 字段给出可执行的修复建议。
The error JSON may carry a `"hint"` field with an actionable fix.

---

## 长安装 / 断连 / 重试 / Long install, disconnect & retry

安装可能要 **5–10 分钟**。怕 SSH 断开就加 `--detach`：脚本自我后台化并立即返回
`{"status":"detached","pid":N,"log":"./zfc-install.log","result_file":".zfc_install_result.json"}`，
之后轮询结果文件 / `tail -f` 日志即可。

An install can take **5–10 min**. Add `--detach` to survive an SSH drop; it
backgrounds itself and returns a `pid`+`log` at once. Poll the result file / tail the log.

中断后重试 / Retry after an interruption:

- 半成品安装（端口被占 → 11/16）：重跑 `--install --force`（拆旧容器、释放端口、**重建内置库**）。
- 想彻底清空：`--clean-install`（隐含 `--force`；**不动外部库**）。
- 其实已装好、只想换新镜像：`--update`（**保数据**）。`--force` 在完整安装上会被挡下（16）。

> `--force` 是**重建式重装**（不保留内置库数据）；保数据请用 `--update`。

## 更新失败 → 自愈 / Update failures → auto-fix

`--update` 失败先跑 `--doctor --json`（只读诊断）。按 `schema.delta_class` / `db_status` 决定：

- `additive` + `stable`（18/19）：设 `ZFC_ALLOW_SCHEMA_FINGERPRINT_MISMATCH=1` 重启容器，或重跑 `--update`。
- `destructive` / `unknown`：重跑 `--update`（迁移并重盖账本），**勿设** override。
- `db_status != stable`（18）：从 `schema_backup/db_backup_*.sql` 恢复后重跑 `--update`。
- 20（拉镜像失败）：查 `DOCKER_REGISTRY`/网络/凭据。docker 过旧：升级 Docker。

指纹 override 仅对 additive/index-only delta 安全（先用 `--doctor` 判别）。

---

## 安装后 / After install

- 登录地址 / Login: `https://WEB_DOMAIN`
- 管理员地址 / Admin address: `admin@zfc.local`
- 管理员 Token：从权限 600 的 `.admin_token`或 `.zfc_install_result.json`读取。长动作的 stdout JSON 只含 `admin_token_file` / `result_file`指针，人类可读输出不再打印明文 Token。
  Admin token: read the protected token/result file. Long-running commands return file pointers on stdout; human-readable output does not print the token.

> 完整契约见仓库 `AGENT_INSTALL.md` 与纯文本 <https://www.zeroforwarder.com/llms-install.txt>。
> Full contract: repo `AGENT_INSTALL.md` and the plain-text prompt above.

---

## 装好之后出问题？让 AI 帮你定位 / Something broke later? Let the AI diagnose it

面板装好后若遇到端口连不上、端点离线、流量不对等问题，同样可以让 AI 助手**远程只读定位**，
无需 SSH。见 [用 AI 助手定位问题](../agent-diagnose/index.md)。

After install, if a port won't connect, an endpoint goes offline, or traffic looks
wrong, the same AI agent can **diagnose it remotely (read-only, no SSH)** — see
[Diagnose with an AI agent](../agent-diagnose/index.md).
