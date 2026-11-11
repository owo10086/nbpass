<!-- Source URL: https://nyanpass.pages.dev/reference/be_command/ | Capture date: 2026-11-10 -->

<a id="_1"></a>
# 面板端常用命令

在 `/opt/backend` 目录下运行。

<a id="_2"></a>
## 故障排除

- 查看最近 100 条日志: `docker compose logs -n 100`
- 查看全部日志: `docker compose logs`
- 跟踪查看当前日志: `docker compose logs -n 1 -f`
- 启动: `docker compose up -d`
- 停止: `docker compose down -t1`
- 重启: `docker compose down -t1 ; docker compose up -d`

<a id="_3"></a>
## 向导命令

<a id="_4"></a>
### 强制重置密码

`./rel_backend -reset-password` 然后按提示操作。

<a id="_5"></a>
### 清理数据

清理失效规则: `./rel_backend -clean 1`

清理权重: `./rel_backend -clean 2`

<a id="_6"></a>
### 数据库拷贝

此命令可以将已有数据（由面板创建的 Table）迁移至目标数据库。借此可实现便利的跨类型数据库转换。

已有数据从 config 中指定的数据库读取。目标数据库将被覆盖，不可恢复。

`./rel_backend -copy-database` 然后按提示操作。
