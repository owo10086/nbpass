<!-- Source URL: https://docs.relayx.cc/guide/getting-started/deploy-node | Capture date: 2026-11-10 -->

# 部署节点

当创建好节点组后，需要部署节点。点击节点组列表的「部署节点」按钮。

![部署节点](https://docs.relayx.cc/assets/images/deploy-node-3cb758f47eeebb61b87e2c1dfccf9fcf.png)

- **服务名称**：`systemctl` 服务的名称，当一台机器部署多个节点时，需要确保服务名称不同
- **节点ID**：节点唯一 ID，需要确保整个系统唯一，默认会自动生成 UUID，你也可以手动输入
- **连接 IP 或域名**：建立隧道时的连接 IP 或域名，默认自动获取公网 IPv4 和 IPv6，如果有多个 IP 需要手动指定
- **调试模式**：开启调试模式后，会打印更多日志信息
- **出口网络**：指定节点的出口网络，一般适用于多 IP 机器

当配置完成后，会自动生成一条命令，你可以直接复制该命令到你的服务器上执行。

<a id="升级节点"></a>

## 升级节点

点击右上角「升级节点」按钮，执行升级命令。

![升级节点](https://docs.relayx.cc/assets/images/upgrade-node-cbf807123aeadd2d4bbad41fc344a284.png)

<a id="卸载节点"></a>

## 卸载节点

点击右上角「卸载节点」按钮，执行卸载命令。当你的机器部署了多个服务时，需要指定服务名称，否则会卸载所有服务。

![卸载节点](https://docs.relayx.cc/assets/images/uninstall-node-face657d1adaacb2216d8062a37d4f18.png)

<a id="节点管理"></a>

## 节点管理

当节点部署完成后，可以点击节点组列表的「节点管理」按钮，进入节点管理页面。

![节点管理](https://docs.relayx.cc/assets/images/node-management-a5c4f1c4a30d0df0c8b4ea68fc5ebc9a.png)

<a id="节点状态"></a>

## 节点状态

节点状态界面可以查看节点状态，包括 CPU 使用率、内存使用率、磁盘使用率、网络使用率等。

![节点状态](https://docs.relayx.cc/assets/images/node-stats-cb08cb91d85639e9f3e541aaead76987.png)

<a id="使用-docker-compose-部署"></a>

## 使用 docker compose 部署

你也可以使用 docker compose 部署节点。

**compose.yaml**

```yaml
services:
  relayx-agent:
    image: relayx/agent
    container_name: relayx-agent
    restart: always
    network_mode: host
    command:
      - /app/relayx-agent
      - -s
      - https://www.relayx.cc
      - -t
      - AGENT_TOKEN
      - -n
      - AGENT_NODE_ID
```

<a id="启动"></a>

### 启动

替换 `AGENT_TOKEN` 和 `AGENT_NODE_ID` 为你的节点 ID 和 Token。

```bash
docker compose up -d
```

<a id="升级"></a>

### 升级

```bash
docker compose up -d --pull always
```

<a id="卸载"></a>

### 卸载

```bash
docker compose down
```

<a id="网络代理"></a>

## 网络代理

某些情况下，你的节点端可能无法直接访问到面板，需要配置代理访问。注意此时需要指定 `-i` 参数设置节点 IP，否则会获取到代理的 IP。

安装节点时，可以先在当前 shell 中导出代理环境变量，再执行面板生成的部署命令：

```bash
export http_proxy="http://127.0.0.1:7890"
export https_proxy="http://127.0.0.1:7890"
export HTTP_PROXY="http://127.0.0.1:7890"
export HTTPS_PROXY="http://127.0.0.1:7890"

curl -sSL https://dl.relayx.cc/install.sh | sh -s -- -s "https://你的面板域名" -t "你的节点组 Token" -i "YOUR_IP"
```

如果你的代理需要账号密码，可以使用 `http://用户名:密码@代理地址:端口` 的格式。

安装完成后，如果节点运行时仍然需要通过代理访问面板，修改 `/etc/systemd/system/relayx-agent.service` 文件，改为以下内容：

**relayx-agent.service**

```bash
[Unit]
Description=RelayX Agent
After=network.target

[Service]
Environment="HTTP_PROXY=http://127.0.0.1:7890"
Environment="HTTPS_PROXY=http://127.0.0.1:7890"
ExecStart=/usr/local/bin/relayx-agent -t "xxx" -s "https://xxx" -i "YOUR_IP"
User=root
Group=root
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
Alias=relayx-agent.service
```

然后执行：

```bash
systemctl daemon-reload && systemctl restart relayx-agent
```

<a id="出口节点指定监听端口"></a>

## 出口节点指定监听端口

> **注意**
>
> 不要在入口机器上指定监听端口，否则会导致入口机器无法正常工作，入口机器在面板上指定监听端口即可。

默认情况下，出口节点对于每种类型隧道会监听一个端口，最多监听 7 个端口，分别为：`tcp`、`mtcp`、`udp`、`wss`、`mwss`、`tls`、`mtls`。对于某些情况，你可能需要手动指定监听端口，如：

```bash
# 修改 systemd 服务文件添加监听端口
relayx-agent --wss-port 8080 --mwss-port 8081
```

格式为：`--<协议>-port <端口>`

这对于一些 NAT 机器来说比较有用。

<a id="节点强制指定监听-ip"></a>

## 节点强制指定监听 IP

默认情况下，节点会从隧道配置中读取监听 IP，但有些情况下，你可能需要强制所有隧道都监听某个 IP，这种情况可以使用 `--listen-ip` 参数：

```bash
# 修改 systemd 服务文件添加监听 IP
relayx-agent -l 127.0.0.1
```

格式为：`--listen-ip <IP>` 或者 `-l <IP>`

<a id="流量统计"></a>

## 流量统计

默认节点段会从当前系统读取总流量，但无法统计月流量，并且这种当系统重启后会清空流量统计。

可以安装 vnstat 来统计流量，并可以持久化。

<a id="安装-vnstat"></a>

### 安装 vnstat

vnstat 是一个网络流量统计工具，可以统计月流量等数据。在节点上安装 vnstat 可以更好地监控网络使用情况。

<a id="ubuntudebian-系统"></a>

#### Ubuntu/Debian 系统

```bash
sudo apt update
sudo apt install vnstat
```

<a id="centosrhelrocky-linux-系统"></a>

#### CentOS/RHEL/Rocky Linux 系统

```bash
sudo yum install vnstat
# 或者使用 dnf
sudo dnf install vnstat
```

<a id="其他系统"></a>

#### 其他系统

```bash
# Alpine Linux
apk add vnstat

# Arch Linux
pacman -S vnstat
```

<a id="monthrotate-参数配置"></a>

### MonthRotate 参数配置

vnstat 默认在每月 1 号进行数据轮转。如果需要修改轮转日期，可以编辑配置文件：

```bash
sudo nano /etc/vnstat.conf
```

在配置文件中找到 `MonthRotate` 参数，可以设置为 1-28 之间的数字：

```bash
# 设置为每月 15 号轮转
MonthRotate 15
```

修改配置后需要重启 vnstat 服务：

```bash
sudo systemctl restart vnstat
```

> **注意**
>
> MonthRotate 参数不能设置为 29、30 或 31，因为不是所有月份都有这些日期。
