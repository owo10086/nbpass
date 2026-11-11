<!-- Source URL: https://docs.relayx.cc/guide/getting-started/node-group | Capture date: 2026-11-10 -->

# 创建节点组

节点组是若干节点的集合，同一个节点组里面的节点共享相同的配置，包括监听地址、端口、协议等。

- **端口范围**：限制入口节点组监听的端口范围
- **允许隧道协议**：允许该节点组可以创建的隧道协议
- **允许入口监听协议**：是否允许在该入口节点组下的隧道自定义入口监听协议（HTTP / SOCKS5 / Shadowsocks / Relay / WireGuard 等），关闭后入口仅按 TCP/UDP 透明转发，详见 [入口监听协议与转发地址协议](../../advanced/protocol/index.md#%E5%85%A5%E5%8F%A3%E7%9B%91%E5%90%AC%E5%8D%8F%E8%AE%AE%E4%B8%8E%E8%BD%AC%E5%8F%91%E5%9C%B0%E5%9D%80%E5%8D%8F%E8%AE%AE)
- **流量倍率**：入口节点组的计费流量倍率

<a id="入口节点组"></a>

## 入口节点组

入口节点组是你客户端直接连接的节点组，节点组里面的节点通常会用 DNS 智能解析来进行 DNS 负载均衡。

![入口节点组](https://docs.relayx.cc/assets/images/in-node-group-d1129c206281a7473f7b17a35345db1c.png)

- **是否需要出口节点组**：指定该入口节点组是否需要出口节点组，如果不需要，则可以直接转发到目标地址而不是构建隧道
- **允许的出口节点组**：指定该入口节点组可以连接的出口节点组
- **连接 IP 或域名**：显示在隧道列表的连接 IP 或域名，替换节点组里面的原始 IP

<a id="出口节点组"></a>

## 出口节点组

![出口节点组](https://docs.relayx.cc/assets/images/out-node-group-cd4b839168bcc4c2da97d03a3f09542f.png)

- **允许的入口节点组**：指定该出口节点组可以被哪些入口节点组连接
- **负载均衡方式**：指定入口节点组连接到该出口节点组时，出口节点的负载均衡方式
