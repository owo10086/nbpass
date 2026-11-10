<!-- Source URL: https://www.zeroforwarder.com/docs/ix-guide/ | Capture date: 2026-11-10 -->

# IX 使用说明

区分 IX 的业务中转与控制通信，配置可达地址、端口及必经层。

IX / 专线通常用于入口到落地之间的一段路径。下面以 `入口 A → IX 端点 B → 目标`为例配置。

## 第一步：部署 IX 转发端点

进入 **端口管理 → 转发端点 → 添加转发端点**，填写前一跳可访问的 IP / 域名与服务端口，再按该记录的安装指引部署 forwarder。

![转发端点的对外服务端口与本机绑定设置](https://img.coderluny.com:444/uploads/zfc-tutorial-20261018-endpoint-basic-277ef4dcc27c.png)

**服务端口**是前一跳连接的对外端口，**绑定端口 / IP**是本机监听。NAT 或专线映射场景中两者可以不同，需由实际网络映射对应。确认 A 能访问 B，B 能访问目标。

## 第二步：选择业务配置层次

| 需求 | 配置 |
| --- | --- |
| 只有某个端口经过 IX | 在端口 **转发配置 → 单跳转发**选择 B |
| 该服务器的业务默认都经过 IX | 在服务器配置 B，并开启 **使用转发端点作为传输** |
| 只让 worker 经 IX 连接控制器 | 在服务器配置 B，关闭上述传输开关 |

![服务器默认转发与业务传输开关](https://img.coderluny.com:444/uploads/zfc-tutorial-20261018-server-forward-be5426fded2c.png)

端口覆盖和分流仍可能改变出站路径，保存后核对最终配置。

## 第三步：添加和验证端口

在 **端口管理 → 添加端口转发**选择入口 A，目标填写 `web.example.com:443`，服务端口例如 `32000`，配置实际需要的 TCP / UDP 监听。

![端口目标、入口服务端口与转发模式](https://img.coderluny.com:444/uploads/zfc-tutorial-20261018-port-basic-33afbae1bbab.png)

保存并确认同步后，客户端连接入口地址与端口，用实际服务检查目标访问和预期出口。

## 多跳 IX / 专线

有多段中转时，改用链式转发并按实际可达顺序组织。必须经过的 IX 或内网层应固定前 / 后 N 跳，或关闭智能选择，避免选到不存在的跨层直达路径。

![链式路径的固定前后跳数](https://img.coderluny.com:444/uploads/zfc-tutorial-20261018-port-chain-316e0f9fa2d4.png)

创建端点或链不会自动配置运营商专线、内网路由、NAT 映射和防火墙。节点在线只说明管理连接存在，还需逐跳验证业务路径。

更复杂示例见[沪日美专线组网](../jp-us-lan/index.md)；入口无法直接联网的机器见[Halo IX 机器](../halo-ix/index.md)。
