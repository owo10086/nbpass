<!-- Source URL: https://nyanpass.pages.dev/reference/nc_command/ | Capture date: 2026-11-10 -->

<a id="_1"></a>
# 常用命令

如果同一机器安装了多个节点端， `nyanpass` 可替换为你的安装名。

<a id="_2"></a>
## 卸载

```shell
bash /opt/nyanpass.uninstall.sh
```

<a id="_3"></a>
## 故障排除

- 查看状态: `systemctl status nyanpass`
- 跟踪查看当前日志: `journalctl -fu nyanpass`
- 查看全部日志: `journalctl -u nyanpass`
- 启动: `systemctl start nyanpass`
- 停止: `systemctl stop nyanpass`
- 重启: `systemctl restart nyanpass`
- 开机启动: `systemctl enable nyanpass`
- 取消开机启动: `systemctl disable nyanpass`

<a id="_4"></a>
## 运行参数

运行 `/opt/nyanpass/rel_nodeclient -h` 可查看这些参数的说明。

运行参数可以【安装前】在对接命令中指定。【安装后】如需修改，可卸载重装，或直接修改 `start.sh` 然后重启服务。

修改对接命令，例如： `bash <(curl -fLSs xxx) rel_nodeclient "-t xxx -u xxx --ws-port 1145"`

<a id="_5"></a>
### 常用参数说明

`--connect-host` 指定本机器的连接 IP，如果不指定，则默认使用公网 IP。

`--ws-port` `--tls-port` 指定 ws / tls 类协议的监听端口。如果不指定，则随机监听。

全部参数请运行节点端程序 `-h` 查看，或者使用[节点端配置文件](../nc-config/index.md)。

<a id="_6"></a>
### 环境变量

有些参数是通过环境变量指定的，请看[对应说明](../nc-environment/index.md)。
