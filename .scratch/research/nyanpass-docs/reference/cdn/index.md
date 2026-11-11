<!-- Source URL: https://nyanpass.pages.dev/reference/cdn/ | Capture date: 2026-11-10 -->

<a id="cdn"></a>
# CDN 隧道

添加一条经过 CDN 的 ws 隧道，由于 CDN 限制，不支持 UDP (可自行开启 udp over tcp)。

测试过可用的 CDN 有： Cloudflare CloudFront

> **警告**
>
> 1. 这是滥用 CDN 服务的行为，强烈建议：仅将这个作为最后的备用选项。
> 2. 上面提到的两个 CDN 到中国的线路现在已经几乎无法使用了，并且 GFW 对高并发的 WS 已进行限制。如果您希望架设跨越中国 GFW 的 CDN 隧道，可以直接关闭这个页面，这个方法已经废了。

<a id="_1"></a>
## 添加入口服务器

按常规方法 添加设备组 -> 复制命令 -> 一键安装即可。

<a id="_2"></a>
## 添加出口服务器

出口服务器是位于国外的服务器，使用的 CDN 以 Cloudflare 为例。

1. 首先按常规方法 添加设备组 -> 复制命令 -> 一键安装
2. 编辑 `/opt/nyanpass/start.sh`
   > 随便选一个端口用于 Cloudflare 回源，例如 28888
   >
   > 则将 `./rel_nodeclient -o` 改为 `./rel_nodeclient -ws-port 28888 -o`
3. 重启节点 `systemctl restart nyanpass`
4. 在 CloudFlare 面板设置回源的 IP 和 端口(比如 28888)。
5. 访问你的连接域名，若看到 Cloudflare 返回 `Web server is returning an unknown error Error code 520`，则说明之前的步骤成功。
6. 点击出口设备组的 `高级编辑` 输入以下内容

```json
{
    "protocol": "ws",
    "connect_type": "static",
    "connect_address": "104.16.123.45", // 填写最优的 CloudFlare IP
    "connect_port": 80,
    "ws": {
        "host": "your.domain.com", // 填写连接域名
        "path": "/"
    }
}
```
