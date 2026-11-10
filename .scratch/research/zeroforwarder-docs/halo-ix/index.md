<!-- Source URL: https://www.zeroforwarder.com/docs/halo-ix/ | Capture date: 2026-11-10 -->

# Halo IX 机器

为受限网络的 IX 节点准备可达管理路径，并核对离线部署所需实例文件。

部分 IX 机器只能访问指定的互联端，无法直接下载或连接面板。先确认机器实际的地址、路由、可访问对端与 CPU 架构，再选择部署方式；不能把某一家线路的网络限制当成所有 IX 的固定条件。

## 第一步：准备可达的香港对端

在香港端部署 forwarder，填写 IX 机器可访问的对端地址和端口。先从 IX 测试该地址，再从香港端测试控制器及业务目标。

![转发端点的对外地址、服务端口与本机绑定设置](https://img.coderluny.com:444/uploads/zfc-tutorial-20261018-endpoint-basic-277ef4dcc27c.png)

## 第二步：为 IX 创建服务器记录

在 **服务器管理**创建代表 IX 机器的 worker，展开 **转发配置**，配置经香港端到控制器的路径。

![服务器的转发配置与业务传输开关](https://img.coderluny.com:444/uploads/zfc-tutorial-20261018-server-forward-be5426fded2c.png)

- 只解决管理连接时，关闭 **使用转发端点作为传输**。
- 业务也必须经香港端时，开启默认业务传输，或在具体端口配置同样的路径。

核对 IX 到香港端的网络地址，不要把香港公网入口误当成 IX 可以访问的专线地址。

## 第三步：准备安装文件

优先使用该服务器记录提供的安装 / 离线部署方式。无法在线下载时，可在能联网的临时环境为 **IX 这条记录**准备匹配架构的程序和配置，再安全传到 IX。

手动搬运前核对实际 systemd 单元：

```bash
# 默认 worker 服务名示例；自定义服务名需替换
systemctl cat zf.service
systemctl show zf.service -p FragmentPath -p WorkingDirectory -p ExecStart
```

按单元引用的实际路径准备程序、配置、环境文件、证书和 drop-in；保持目录与权限一致。默认 worker 目录通常是 `/root/zf`，多实例、共享实例或自定义安装目录会不同。

不要沿用“复制 `/root/zf/*` 就一定完整”的做法，也不要复制另一条正在使用的生产 worker 身份。配置和安装命令含认证信息，传输与截图都需保密。

## 第四步：在 IX 启动并核对身份

完成文件和单元准备后，在 IX 执行实际服务名对应的命令：

```bash
systemctl daemon-reload
systemctl enable --now zf.service
systemctl status zf.service --no-pager
journalctl -u zf.service -n 100 --no-pager
```

确认面板在线的是 IX 对应记录，检查本机地址、版本、同步状态。若临时机曾启动同一份实例配置，确认 IX 正常后停止临时副本，避免两台机器争用一个身份；清理时核对实际服务和目录，保留香港端仍需运行的 forwarder。

## 第五步：配置业务与必经路径

在端口配置需要的中转或多跳路径。前海等其他受限节点也应分别创建身份并验证网络，不要直接克隆同一配置。

![链式转发的顺序、智能选择与固定跳数](https://img.coderluny.com:444/uploads/zfc-tutorial-20261018-port-chain-316e0f9fa2d4.png)

业务必须经过专线层时固定对应前 / 后跳数，或关闭智能选择。最后从客户端验证实际 TCP / UDP 业务，不能仅凭面板“在线”判断部署完成。
