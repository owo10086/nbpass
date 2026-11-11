<!-- Source URL: https://nyanpass.pages.dev/intro/update_backend/ | Capture date: 2026-11-10 -->

<a id="_1"></a>
# 升级面板端

此教程面向自建用户，云托管的面板会统一升级，无需您手动升级。

<a id="0"></a>
## 0 备份数据

如果您选择 SQLite 作为数据库，那么您将需要负责 `/opt/backend/*.db` 的备份。

<a id="1"></a>
## 1 停止服务

```shell
cd /opt/backend # 切换到您安装面板的目录
docker compose down -t1
```

<a id="2"></a>
## 2 下载新资源

请确保当前目录为 `/opt/backend` 或者您安装面板的目录。

```shell
rm -rf ./public ./rel_backend
bash <(curl -fLSs https://dl.nyafw.com/download/nyanpass-install.sh) rel_backend
```

<a id="3"></a>
## 3 更新上线

请确保当前目录为 `/opt/backend` 或者您安装面板的目录。

```shell
MIGRATE=1 ./rel_backend # 自动升级数据库
docker compose up -d # 上线
```
