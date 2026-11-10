<h1 align="center">nbpass</h1>

<p align="center">管理员自用的 TCP 转发面板：统一管理节点与转发规则，节点被墙时自动换 IP 或切换入口。</p>

<p align="center">
  <a href="./go.mod"><img src="https://img.shields.io/badge/Go-1.26.3-3776AB?style=flat-square" alt="Go 1.26.3"></a> <a href="./docs/deploy-linux.md"><img src="https://img.shields.io/badge/Platform-Linux%20amd64-4B5563?style=flat-square" alt="平台 Linux amd64"></a> <a href="./docs/deploy-linux.md"><img src="https://img.shields.io/badge/Docs-%E9%83%A8%E7%BD%B2%E6%8C%87%E5%8D%97-4F46E5?style=flat-square" alt="部署指南"></a>
</p>

nbpass 由两个程序组成。面板 `nbpass` 是一个 Go 单文件程序，内嵌网页前端和 SQLite。节点 `nbpass-node` 运行在每台转发机器上，通过 gRPC 双向流主动连接面板，接收带版本号的配置。面板离线时，节点继续按缓存配置转发。v1 只支持 TCP/IPv4，运行平台为 Linux amd64 + systemd。

## 核心亮点

| 亮点 | 对使用者的价值 |
|---|---|
| 直连与隧道两种转发 | 入口节点可以直接连目标，也可以经 WSS + yamux 隧道交给出口组；出口组支持主备切换 |
| 被墙检测 | 节点每 2 分钟自测一轮，连续 3 轮异常判定被墙；自测结果未知时，由国内检测点补判 |
| 被墙自动处理 | 按入口组策略处理：仅换 IP、仅切换入口，或先切换入口、换 IP 复检通过后再切回。DNSPod 记录统一由面板写入 |
| 运维闭环 | 统一规则影响与冗余概览、可追查的故障时间线，保留探针监控、事件日志、Telegram 通知及带回滚的一键节点升级 |
| 安全的节点接入 | 节点用一次性票据安装（30 分钟有效、只能兑换一次），命令中不含长期 token |

概览分别展示明确转发异常、影响待确认、冗余下降和需要人工介入的故障。故障页可按对象、类型、状态和时间追查控制离线、入口切换、换 IP 复检、规则应用及 DNS 证据；已结束故障沿用事件保留天数。控制连接异常不等于业务中断，服务商写入成功也不等于解析传播完成。记录整理失败或延迟时，页面显示最后整理时间，原自动处理继续沿用既有模块。

## 架构

控制面：管理员和节点都经过反向代理访问面板。面板本身只提供明文 HTTP 和 gRPC，由代理负责终结 TLS。

```text
                       HTTPS                         gRPC / TLS
┌──────────────────┐          ┌──────────────────┐          ┌──────────────────┐
│  Admin browser   │─────────▶│  Reverse proxy   │◀─────────│   nbpass-node    │
│   管理员浏览器   │          │ 反向代理,终结TLS │          │ 节点 (入口/出口) │
└──────────────────┘          └────────┬─────────┘          └────────┬─────────┘
                       HTTP :8080      │                             │
                       gRPC :8081      ▼                             │ 换 IP
                              ┌──────────────────┐                   │ rotate IP
                              │   nbpass panel   │                   ▼
                              │ 面板: 网页+SQLite│          ┌──────────────────┐
                              └────────┬─────────┘          │   Cloud vendor   │
                                       │                    │  云厂商换 IP 接口│
                                       ▼                    └──────────────────┘
                              ┌──────────────────┐
                              │ DNSPod / Telegram│
                              │  解析与通知接口  │
                              └──────────────────┘
```

转发面：每条转发规则从入口组上的一个监听端口映射到一个目标。

```text
                  TCP                   直连 direct
┌────────────┐          ┌────────────┐            ┌────────────┐
│   Client   │─────────▶│ Entry group│───────────▶│   Target   │
│    用户    │          │   入口组   │            │    目标    │
└────────────┘          └─────┬──────┘            └────────────┘
                              │                          ▲
          隧道 WSS + yamux    │                          │
                              ▼                          │
                        ┌────────────┐                   │
                        │ Exit group │───────────────────┘
                        │   出口组   │
                        │  (主/备)   │
                        └────────────┘
```

## 快速安装

在可信构建机上准备 Go 1.26.3 和 Node.js 22.12+，然后在仓库根目录构建：

```sh
make linux-amd64
```

构建产物为 `bin/nbpass-linux-amd64`（面板）和 `bin/nbpass-node-linux-amd64`（节点）。注意：单独运行 `go build` 会沿用检出时的内嵌资源，不能用于发布新功能。

把面板程序和 `scripts/install-panel.sh` 复制到 Linux 服务器，安装命令如下：

```sh
sudo sh scripts/install-panel.sh -binary ./bin/nbpass-linux-amd64 -public-origin https://panel.example.com -control-url https://control.example.com
```

安装器会创建 `nbpass` 用户，并安装、启动 `nbpass.service`。服务监听两个地址：网页 `127.0.0.1:8080`，gRPC `127.0.0.1:8081`。

## 快速开始

1. 配置反向代理：网页域名转发到 `:8080`，控制域名用 `grpc_pass` 转发到 `:8081`。完整 Nginx 配置见 [部署文档第 2 节](./docs/deploy-linux.md)。
2. 访问 `https://panel.example.com`，首次设置管理员密码（12–72 字节）并登录。
3. 在网页中添加节点，复制面板生成的安装命令，以 root 身份到 Linux amd64 节点上执行（非 root 用户先执行 `sudo -i`）。命令形式如下：

```sh
curl --proto '=https' --proto-redir '=https' --tlsv1.2 -fsSL 'https://panel.example.com/install-node.sh' | sh -s -- -control-url 'https://control.example.com' -ticket '网页生成的43字符一次性票据'
```

4. 确认节点服务已运行。之后节点会在面板中显示为在线，即可创建转发规则：

```sh
sudo systemctl is-active nbpass-node.service nbpass-update.service
```

## 面板命令

| 命令 | 用途 |
|---|---|
| `nbpass reset-password -data-dir /var/lib/nbpass` | 重置管理员密码 |
| `nbpass backup -data-dir ... -backup-dir ... -program ...` | 一致性备份程序、数据库和密钥 |
| `nbpass upgrade -data-dir ... -backup-dir ... -program ...` | 先备份，再执行数据库迁移（需先停服务） |
| `nbpass restore -backup-dir ... -data-dir ...` | 从备份恢复到新的数据目录 |

完整参数和操作顺序见 [部署文档第 5 节](./docs/deploy-linux.md)。开发时可用 `make build` 构建本机产物，用 `make test` 运行 `go vet` 和全部测试。本机运行面板调试时不需要 HTTPS：`go run ./cmd/nbpass -data-dir /tmp/nbpass-dev -public-origin http://127.0.0.1:8080`，浏览器打开 `http://127.0.0.1:8080`。http 源只接受 `127.0.0.1`、`::1` 和 `localhost`；这种模式下节点无法接入，因为控制连接始终校验 TLS。

## 文档

| 主题 | 内容 | 链接 |
|---|---|---|
| 部署与运维 | 面板安装、Nginx、节点安装、权限、备份与升级、节点回滚 | [打开文档](./docs/deploy-linux.md) |
| 上线验收 | 在真实 Linux 环境中逐项人工检查的清单 | [打开文档](./docs/linux-acceptance.md) |
| 术语表 | 入口/出口节点、被墙、换 IP 等术语的准确定义 | [打开文档](./GLOSSARY.md) |
| 架构决策 | gRPC 双向流、单一协调者、配置版本等 ADR | [打开目录](./docs/adr/) |
