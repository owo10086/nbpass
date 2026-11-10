<!-- Source URL: https://www.zeroforwarder.com/docs/quickstart/ | Capture date: 2026-11-10 -->

# 安装面板

准备域名与授权，在专用目录安装、更新并检查 Zero Forwarder 面板。

## 安装前准备

- 一台可用的 Linux VPS，推荐 Debian / Ubuntu，至少 2 GB 内存、40 GB 可用磁盘。
- 两个已解析到面板公网 IP 的域名，例如 `forward.example.com` 与 `controller.example.com`。
- 授权信息：`ZFC_INSTANCE_ID`、`ZFC_API_KEY`。
- 使用 Caddy 自动 HTTPS 时，确保 80 / 443 未被其他服务占用；Cloudflare 域名设为仅 DNS。
- Docker 与可用的 Compose。优先使用 **Docker Compose v2 插件**；不要把安装名为 `docker-compose` 的包等同于已经获得 v2。

安装脚本可在相应模式下安装缺少的依赖。手动准备时，检查实际版本：

```bash
docker version
docker compose version
```

## 在专用目录运行安装

使用 root 登录，先进入将长期保留的安装目录：

```bash
mkdir -p /root/zfc
cd /root/zfc
curl -fsSL https://get.zeroforwarder.com/install.sh -o install.sh
bash install.sh
```

按菜单选择 **1. 全新安装**，输入前端域名、控制器域名和授权信息。按需选择内置 / 外部数据库、Caddy、镜像仓库与标签，核对后等待安装完成。

已有安装不要选择全新安装覆盖；更新请使用下文的更新流程。非交互预检、后台运行和错误码见[用 AI 助手安装](../agent-install/index.md)。

## 取得登录信息

当前安装过程把管理员 Token 保存到安装目录的 **`.admin_token`**，权限为 600；人类可读安装输出不再打印明文 Token。登录使用管理员地址 `admin@zfc.local` 和实际 Token。

```bash
# 仅在自己的终端查看，不把结果贴到公开日志或截图
cat .admin_token
```

非交互结果文件 `.zfc_install_result.json`也包含 Token。长动作的 stdout JSON 只提供 `admin_token_file` / `result_file`指针，不能再直接从 stdout 的 `admin_token`字段取值。

安装目录中的 `.env`、管理员 Token、数据库备份和迁移包包含私密信息，应妥善保管。

## 安装后检查

1. 打开 `https://forward.example.com`并登录。
2. 检查容器状态和控制器域名的实际可达性。
3. 添加自己的服务器与转发端点，按各记录安装指引部署。
4. 完成一条普通转发，确认配置同步与真实业务均正常。

```bash
cd /root/zfc
docker compose ps
```

未启用 Caddy 时，自行配置 HTTPS 反向代理：默认 Web 服务为本机 `8080`，控制器为 `3100`，以生成的 Compose 和端口映射为准。控制器连接需要代理支持对应长连接；不要仅以网页打开成功判断节点已能连接。

## 更新与常用维护

迁机、完整历史备份和恢复验证见[备份与迁移](../backup-migration/index.md)。安装后可按[用户与套餐](../users-packages/index.md)、[权限管理](../access-control/index.md)及[告警通知](../alerts-notifications/index.md)配置运营与维护入口。

每次都进入 **原安装目录**，重新取得脚本后运行：

```bash
cd /root/zfc
curl -fsSL https://get.zeroforwarder.com/install.sh -o install.sh
bash install.sh
```

| 操作 | 当前交互菜单 |
| --- | --- |
| 保留数据更新 | 2. 更新镜像（保留数据） |
| 卸载 | 3. 卸载系统；继续核对是否清理数据 |
| 查看管理员 Token | 4. 查看管理员密码 |
| 刷新管理员 Token | 5. 刷新管理员 Token；已有登录凭据需重新核对 |
| 打包迁移 | 6. 一键打包迁移文件 |
| 恢复迁移 | 7. 一键恢复迁移文件 |

更新失败先诊断，不要直接运行清空式重装：

```bash
bash install.sh --doctor --json
```

菜单会随脚本版本变化，执行时同时核对名称。迁移和恢复见[常见问题](../faq/index.md#如何备份和迁移)。
