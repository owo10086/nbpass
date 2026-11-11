<!-- Source URL: https://nyanpass.pages.dev/reference/dns_cache/ | Capture date: 2026-11-10 -->

<a id="dns"></a>
# 高频 DNS 缓存设置教程

<a id="_1"></a>
## 引言

> **版本 nc20260302 更新**
>
> 节点端已经实现了 DNS 缓存，但是仍然不建议在「转发目标」或者「连接地址」中使用域名，理由如下：
>
> - DNS 依赖外部服务器，可能存在可靠性问题
> - 一个域名同时解析 IPv6 和 IPv4 时，无法控制优先使用哪种地址，部分情况甚至可能无法由 v6 回落到 v4
> - 一个域名解析多个地址时，无法控制权重/负载均衡/故障转移策略

NY 节点端并不自带 DNS 缓存功能，如果您使用了域名作为目标地址，每当有新的用户连接发起时，出口服务器就会向上游 DNS 服务器请求解析一次域名。

这种高频率的查询会让很多公共 DNS 服务将你的请求视为滥用行为，从而拒绝响应或限速处理，导致你的转发服务出现不稳定、延迟增大甚至中断的情况。

为了解决这一问题，你可以搭建一个带有缓存功能的 DNS 服务器，让所有的域名解析请求先经过它进行缓存，再转发到上游 DNS。这样不仅能大幅降低上游查询负载，也能显著提升响应速度和服务稳定性。

> **什么情况会出现这种问题**
>
> - [+] 使用公共 DNS (例如 `1.1.1.1` `8.8.8.8` 以及某些运营商 DNS) 的机器。
> - [+] 使用了域名作为目标地址。
> - [-] AWS 默认模板使用了自己的内网 DNS 服务器，一般不会有这种问题。
> - [-] 隧道入口到出口之间的通信，默认不依赖 DNS，一般不会有这种问题。

> **免责声明**
>
> 本教程并非对所有环境都适用。由于各用户系统配置和网络环境的差异，按照本文步骤进行操作可能无法达到预期效果，甚至可能导致系统 DNS 服务异常。**如果在实践过程中遇到任何问题，请不要联系作者寻求帮助，而应自行排查并解决。**

<a id="_2"></a>
## 步骤

以 Debian 12 系统为例，使用 dnsmasq 作为本地 DNS 服务器。

<a id="_3"></a>
### 安装

- 与 `systemd-resolved` 冲突，请确保您没有安装 `systemd-resolved`
- 下方脚本会直接覆盖已有的 dnsmasq 和 `resolv.conf` 配置，请确保当前系统没有对它们进行特殊配置
- 下方脚本中的 `server` 可以自行修改

```shell
apt-get update
apt-get install -y dnsmasq dnsutils

rm -f /etc/dnsmasq.d/*

echo "
listen-address=127.0.53.53
bind-interfaces

no-resolv

strict-order
cache-size=1000

server=1.1.1.1
server=8.8.8.8
server=2606:4700:4700::1111
server=2001:4860:4860::8888
" > /etc/dnsmasq.d/my.conf

systemctl restart dnsmasq

chattr -i /etc/resolv.conf
rm -f /etc/resolv.conf
echo "nameserver 127.0.53.53" > /etc/resolv.conf
chattr +i /etc/resolv.conf
```

<a id="_4"></a>
### 验证

`nslookup www.google.com`

如果显示类似下方的查询结果，并且查询的服务器为 `127.0.53.53` 即安装成功。

```text
Server:         127.0.53.53
Address:        127.0.53.53#53

Non-authoritative answer:
Name:   www.google.com
Address: 142.250.199.68
Name:   www.google.com
Address: 2404:6800:4005:815::2004
```

<a id="_5"></a>
### 卸载

- 不再使用 dnsmasq，卸载该软件和配置
- 下方脚本中的 `nameserver` 可以自行修改
- 重启系统或者网卡后，`resolv.conf` 可能会被某些系统组件自动修改，如果您不希望这种情况发生，可以取消注释下方命令的最后一行。

```shell
rm -f /etc/dnsmasq.d/*

apt-get purge -y dnsmasq
apt-get autoremove -y

chattr -i /etc/resolv.conf
rm -f /etc/resolv.conf
echo "nameserver 1.1.1.1" > /etc/resolv.conf
# chattr +i /etc/resolv.conf
```
