<!-- Source URL: https://www.zeroforwarder.com/docs/agent-diagnose/ | Capture date: 2026-11-10 -->

# 用 AI 助手定位问题

按需使用进程内诊断导出或只读命令，让 AI 定位节点问题，核对权限、开关和日志隐私。

端口连不上、端点离线、流量或选路异常时，可以让可信的 AI 助手通过面板接口读取诊断证据，无需提供节点 SSH 密码。

当前有两类能力：**Agent 诊断**导出 worker 进程内信息，**Agent 执行**在节点运行命令。只需要前者时，不必为了读诊断信息开放远程命令能力。

## 选择需要的诊断方式

| 方式 | API Scope 与账号 | 可做什么 |
| --- | --- | --- |
| `agent_diag` | 超级管理员属主的专用密钥 | 读取 worker collector 清单、导出进程内状态与日志；当前 debug 导出不支持 forwarder |
| `agent_exec` | 管理员属主且符合接口权限的专用密钥 | 发现 worker／forwarder 实例并执行命令；只读模式下受命令白名单限制 |

`agent_exec`密钥也可用于诊断导出，但导出仍要求超级管理员身份。`full`不会自动授予这两项 Agent 专用权限；Cookie / JWT 也不能代替这些接口的 API Key。

**只读模式限制写操作，不保证诊断输出没有敏感信息。** 日志、主机、来源地址、业务目标和配置片段都可能进入所用 AI 服务的会话或日志，分享前需核对范围。

## 创建有限期的密钥

在 **系统设置 → API Key 管理**创建用途明确、有过期时间的密钥，只勾选本次需要的 **Agent 诊断（只读）**或 **Agent 执行（高危）**。

![当前 API Key 的独立 Agent 诊断与执行权限](https://img.coderluny.com:444/uploads/zfc-tutorial-20261018-api-key-scopes-88558d36eefe.png)

完整密钥只在创建成功时展示一次，放入可信工具的凭据存储或受控会话。要求工具只用 `X-API-Key`请求头访问指定面板，不把密钥写入 URL、公开工单或仓库。更多维护方法见[权限与 API](../access-control/index.md#创建-api-密钥)。

## 开启对应服务能力

这些开关由面板部署管理员在原安装目录配置，先确认当前环境，不让诊断助手擅自修改。

只允许 worker 进程内导出时，可在已有 `docker-compose.override.yml`中合并：

```yaml
services:
  zf-web:
    environment:
      ENABLE_AGENT_DIAG: "true"
      ENABLE_AGENT_EXEC: "false"
```

`ENABLE_AGENT_DIAG`未设置时跟随 `ENABLE_AGENT_EXEC`；显式设置优先。只给 `agent_diag`的密钥不能调用实例发现，因此应从面板提供准确的 **worker ID**，不要拿端口 ID 或 forwarder 数字 ID 代替。

需要检查 forwarder 进程、系统端口等命令证据时，再在两个服务配置只读执行：

```yaml
services:
  zf-web:
    environment:
      ENABLE_AGENT_EXEC: "true"
      AGENT_EXEC_READONLY: "true"
  zf-controler:
    environment:
      ENABLE_AGENT_EXEC: "true"
      AGENT_EXEC_READONLY: "true"
```

保留已有配置，合并所需项，使用字符串 `"true"` / `"false"`，不要用 `1`代替。由部署管理员按改动服务应用：

```bash
cd /root/zfc
# 只修改 zf-web 时只重建该服务；两个服务都修改时使用下列命令
docker compose up -d --no-deps zf-web zf-controler
```

`AGENT_EXEC_READONLY`默认开启，但已有环境可能覆盖它，应同时核对 Web、控制器和节点报告。不要为了让某条被拒命令通过而关闭只读保护；诊断任务应保持只读。

## 把任务交给 AI

提供面板地址、症状、故障时间、具体端口／节点，以及通过受控方式提供的对应密钥。使用以下提示词：

```text
请按 https://www.zeroforwarder.com/llms-diagnose.txt
对我的 ZFC 做只读诊断。

面板地址：https://forward.example.com
方式：agent_diag 或 agent_exec（按已授予权限选择）
目标：worker ID / 端口 ID / 节点名称及故障时间
症状：描述具体失败、方向和错误信息
API Key：已通过受控方式提供，仅允许作为该面板的 X-API-Key 请求头。

先确认实例身份和权限。只读取本次问题必要的证据，不修改配置、
不设置日志级别、不注入故障、不重启。报告已验证事实、未覆盖部分和下一步。
日志或导出被截断时请明确说明，不把缺失证据当作正常。
```

## 接口与返回结果

| 接口 | 用途 |
| --- | --- |
| `GET /api/admin/agent/instances` | `agent_exec`实例发现，按返回的 kind 和 id 定位 |
| `POST /api/admin/agent/instances/{kind}/{id}/exec` | 执行白名单内的只读命令，检查输出和退出码 |
| `GET /api/admin/agent/instances/worker/{id}/debug/collectors` | 获取目标 worker 当前支持的 collector |
| `POST /api/admin/agent/instances/worker/{id}/debug/export` | 按 collector、文件及字节预算导出文本 |

实例发现中的 worker ID 是服务器数字 ID，forwarder ID 是返回的实例 UUID；直接使用返回值，不能混用数据库数字编号。

导出默认每文件上限 64 KiB、响应总预算 1 MiB。检查 `truncated`、`omitted_files`、每文件截断标记和 `warnings`，再按需缩小 collector／文件范围；不能把头尾片段当完整日志。

只读命令白名单只限制允许的查看程序和参数。进程存在、管理连接在线、规则测试通过各自说明不同事实，还需检查真实业务路径。诊断请求会进入审计，执行尝试、失败和成功应分别解读。

诊断完成后，按使用期限停用或删除临时密钥。需要改配置时，由人核对具体方案后按[相应教程](../config/index.md)操作；诊断授权本身不等于修复、重启或故障注入授权。

继续阅读：[连接诊断与测速](../diagnostics-speed-test/index.md) · [防火墙](../firewall/index.md) · [备份与迁移](../backup-migration/index.md)。
