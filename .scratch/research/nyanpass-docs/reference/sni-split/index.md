<!-- Source URL: https://nyanpass.pages.dev/reference/sni_split/ | Capture date: 2026-11-10 -->

<a id="sni"></a>
# 基于 SNI 分流的转发

**看不懂别玩，通报警告。**

版本要求：面板和节点端 > 20260302

可以尝试以下配置：

设备组

```text
端口范围: 443,10000-65535  // 实际只监听母规则的 443 端口，其他端口只用来区分规则，不实际监听
```

```json
{

    "tls_inbound_policy": 2,               // 禁止非 TLS 入站流量，且只允许管理员设置 TLS 监听端口（母规则），如果不嫌端口多也可以设置成 1
    "tls_reject_empty_sni": true,          // 防止空 SNI 扫描
    "allowed_host": [ ".bilivideo.cn" ],   // 设置合理的白名单后缀，防止用户恶意解析 HTTPS 网站

}
```

母规则（管理员创建）

```text
监听端口: 443
转发目标: 127.0.0.1:1               // 不会被使用

-TLS 入站-
模式: 母规则
Uid: 0                             // 写法和 IPv6Group 是一样的
```

子规则（用户创建）

```text
端口: 随意，不会实际监听

-TLS 入站-
模式: 子规则
共享 TLS 端口: 443                  // 母规则的端口，可重复使用
SNI: 1.bilivideo.cn                 // 必须是白名单内的域名
```

> [各项配置的说明](../connect/index.md)

其他说明：

1. 使用 TLS 就可以复用一个端口（例如 443），然后根据 SNI 分流到多个虚拟主机，子规则不再监听端口。
2. VLESS-Vision VLESS-Reality ShadowTLS 等协议不是正常的 TLS，此种模式下不兼容，无法使用。
3. 在入口终结 TLS 握手，回源安全性可选 明文或者 TLS。如果选择了明文，可减少握手延迟。
4. 相比普通的转发规则（嗅探屏蔽可能“会有遗漏”），强制 TLS 入站对 SNI 和未加密协议的控制更加彻底和有效。

整流选项目前有以下可选，可独立开启：

```text
Shape_RejectTLS12 = 1    // 阻断旧版 TLS
Shape_RejectTLS   = 2    // 阻断所有 TLS
Shape_RejectHttp1 = 3    // 阻断除了 101 以外的所有 HTTP 文本响应头
Shape_RejectWs    = 4    // 阻断 HTTP 101 Switching Protocols
Shape_SkipIPv6    = 2000 // 使用 IPv6 连接到入口时，跳过整流处理
```

利用整流选项，可识别 TLS 入站连接的源站 `响应头` `明文/Trojan/VLESS(部分组合)` 中的一些特殊内容（例如 TLS），并进行一些操作（例如阻断）。

以上选项开什么，开了之后还能转发什么协议，开了有什么效果？请自行测试。
