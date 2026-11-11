<!-- Source URL: https://nyanpass.pages.dev/reference/nc_environment/ | Capture date: 2026-11-10 -->

<a id="_1"></a>
# 节点端环境变量

<a id="_2"></a>
## 快捷指定

您可以通过创建 `env.sh` 快捷指定节点程序的环境变量，示例：

```shell
export BIND_INBOUND="192.168.1.123"
export NYA_PROXY="socks5://username:password@127.0.0.1:7890/"
```

修改环境变量后，需要重启服务才能生效。

<a id="_3"></a>
## 环境变量说明

- `DISABLE_EXECUTE` 设置为 1 则禁止在此节点执行命令和 WebSSH，同时会导致面板无法下发升级。
- `BIND_INBOUND` 指定 TCP 与 UDP 入站绑定在哪个网卡 IP，可填多个，以 `,` 分割。
- `BIND_OUTBOUND_4` 指定 IPv4 TCP UDP 出站使用哪个网卡 IP (不推荐使用)
- `BIND_OUTBOUND_6` 指定 IPv6 TCP UDP 出站使用哪个网卡 IP (不推荐使用)
- `OUTBOUND_FWMARK` 指定出站的 fwmark，配合 Linux 策略路由使用，实现指定出站网卡 IP 的效果(自 nc20251018 起支持)
- `COUNT_INTERFACE` 指定统计探针流量的网卡，可填多个，以 `,` 分割。(自 nc20250326 起支持)
- `HEALTH_CHECK` 设置为 0 则在对端与落地的【故障转移】中禁用主动健康检查。默认开启，减少故障转移时的卡顿，会消耗一些资源。 (自 nc20250606 起支持)
- `UUID` 指定节点端的全局唯一 UUID，以代替机器上 ~/.config/.nodeclient\_uuid

<a id="uuid"></a>
### UUID 说明

用于实现设备组内同一台机器多开。大多数使用场景不需要多开，建议先确认您的需求。

多开时自己给每个实例生成一个 `UUID` 就行了，注意不要有重复的，否则会导致诡异的现象。

<a id="bind_outbound"></a>
### BIND\_OUTBOUND 说明

由于 Linux 的 bind 机制，如果设置 BIND\_OUTBOUND 选项，在高并发下会严重影响性能。如果您需要改变默认出口 IP，请尽量利用 OUTBOUND\_FWMARK + iproute2 工具设置策略路由。

<a id="outbound_fwmark"></a>
### OUTBOUND\_FWMARK 说明

- 建议根据自己的需求，自行编写简易脚本，完成设置策略路由的操作。
- 若您的网卡 / IP 发生了变化，或者重启了服务器，必须重新运行设置策略路由。
- 如果 FWMARK 设置失败（例如： FWMARK 在系统中不存在 / 程序无 root 网络权限），转发程序不会报错，而是会使用系统默认路由处理。

<a id="_4"></a>
### 代理设置

**何时需要设置代理：当节点端与面板通信有障碍时。**

`NYA_PROXY` 指定通信代理的链接，如 `socks5://127.0.0.1:7890/` （推荐使用，支持的类型有 `socks5` `http` `ss` `trojan` `tuic`）

`HTTPS_PROXY` 指定通信代理的链接，如 `socks5://127.0.0.1:7890/` (仅支持基本类型代理)

> **安装/更新/下载脚本的代理**
>
> 入口如果安装不了，更推荐使用离线部署功能，而不是设置代理。
>
> 如需设置代理，请在下载脚本前加上代理环境变量 `HTTPS_PROXY="socks5://username:password@127.0.0.1:7890/"` 仅支持基本类型代理(socks/http)。

> **关于 NYA\_PROXY 代理链接写法的提示**
>
> 1. 如果您不了解这些代理链接的格式写法，可以使用 v2rayN 编辑配置，然后导出“分享链接”，一般能识别。
> 2. 请不要尝试使用“经过 ny 中转的节点”作为代理链接，因为这样会导致「获取中转规则」与「连接代理」之间的循环依赖，导致无法联网。
> 3. 仅支持原协议，不支持某些 "Xray" 特有的特性，因此推荐您使用 `ss aes-128-gcm` `Trojan+TLS` `Tuic v5` 等基本的配置。
