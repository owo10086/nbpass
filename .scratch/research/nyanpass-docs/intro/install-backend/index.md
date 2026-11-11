<!-- Source URL: https://nyanpass.pages.dev/intro/install_backend/ | Capture date: 2026-11-10 -->

<a id="_1"></a>
# 安装面板

面板端又称后端/主控端，指 nyanpass 的 `backend` 程序。

> **需要注意**
>
> 1. 请勿在中国服务器安装面板端，否则一定会遇到网络问题。
> 2. 面板与 "宝塔/aapanel/1panel" 默认情况下无法共存，除非你精通 nginx 配置，否则请换一台服务器搭建。
> 3. 教程中如果遇到任何目录或者配置文件不存在，请自行创建。

> **再次提醒**
>
> 1. 如果你不知道自己在做什么，请紧跟教程走，不要乱搞（特别是不要想着用宝塔/aapanel/1panel）。
> 2. 如果你既没有技术顾问，又实在无法离开宝塔/aapanel/1panel，如果你找到了某些第三方教程宣称能使用这些面板搭建ny，请关闭本页面，紧跟你的第三方教程走，搭建和使用出问题请找写教程的人。

<a id="0"></a>
## 0 安装依赖

安装 docker & compose

> 适用于所有系统：

```shell
apt-get update
apt-get install -y chrony
curl -fsSL https://get.docker.com | sh
```

> Debian 13 系统软件源的 docker 已满足版本要求，无须安装 docker-ce 源。可以使用以下命令：

```shell
apt-get update
apt-get install -y chrony docker.io docker-compose
```

> 服务器时间校准

面板服务器的时间非常重要（时区不重要），时间不准确会造成很多问题。

以上命令安装了 `chrony` 自动对时，网络能正常连接到 NTP 服务器的情况下无须手动校正时间。

<a id="1"></a>
## 1 获取程序

通过以下命令快速获取 `backend` 程序。

```shell
mkdir -p /opt/backend
cd /opt/backend
bash <(curl -fLSs https://dl.nyafw.com/download/nyanpass-install.sh) rel_backend
```

<a id="2"></a>
## 2 编写配置文件

以下内容编辑为 `/opt/backend/config.yml`

这是最小配置，完整配置请看[这里](../../reference/be-config/index.md)。

```yaml
# 数据库使用 sqlite3
database-path: "sqlite3://data.db"
# 或者使用 mysql (请看完整配置)

# 监听地址
# 使 backend 监听在 127.0.0.1:18888 ，本文后续默认这个地址为 backend 程序监听的地址
listen: 127.0.0.1:18888

# 授权码
# 建议在配置文件中指定，也可以在命令中使用 -key 指定
key: xxxxxx
```

> **？**
>
> 1. 如果您还没有授权码，请看频道 @nyanpass\_tz 内的开户流程，自助开户。
> 2. 授权码绑定的是授权域名、到期时间等信息，请妥善保存您的授权码，不要泄漏。
> 3. 若您寻找第三方代搭建，对方可能赚取差价甚至劫持您的授权，请提前知晓风险。

<a id="3"></a>
## 3 配置服务与反代

**使用 caddy 反代**

以下内容保存为 `/opt/backend/docker-compose.yaml`

```yaml
services:
  nya:
    image: alpine
    network_mode: host
    restart: always
    volumes:
      - .:/opt/backend
    working_dir: /opt/backend
    command: ./rel_backend
    logging:
      driver: "json-file"
      options:
        max-size: "50m"
        max-file: "3"
  caddy:
    image: caddy:2-alpine
    network_mode: host
    restart: always
    volumes:
      - .:/opt/backend
      - ./caddy:/etc/caddy
      - ./caddy_data:/data
    logging:
      driver: "json-file"
      options:
        max-size: "50m"
        max-file: "3"
```

以下内容保存为 `/opt/backend/caddy/Caddyfile`

```text
{
    servers {
        protocols h1 h2
    }
}

改成你的域名 {
    ## TLS 必须配置，请在下面选择一种配置方法

    # TLS 本地证书
    # tls /opt/backend/caddy/your.crt /opt/backend/caddy/your.key

    # TLS 自动申请（适合 直连面板服务器 的用户）
    # tls your@email.com

    # TLS 自签（适合套 CDN 加速的用户）
    tls internal

    # 前端资源
    file_server {
        root /opt/backend/public
    }

    # /api/* 反代到 backend
    reverse_proxy /api/* http://127.0.0.1:18888 {
        trusted_proxies 127.0.0.0/8 173.245.48.0/20 103.21.244.0/22 103.22.200.0/22 103.31.4.0/22 141.101.64.0/18 108.162.192.0/18 190.93.240.0/20 188.114.96.0/20 197.234.240.0/22 198.41.128.0/17 162.158.0.0/15 104.16.0.0/13 104.24.0.0/14 172.64.0.0/13 131.0.72.0/22 2400:cb00::/32 2606:4700::/32 2803:f800::/32 2405:b500::/32  2405:8100::/32 2a06:98c0::/29 2c0f:f248::/32
        header_up CF-Connecting-IP {http.request.header.CF-Connecting-IP}
    }
}
```

**不反代**

此选项面向高级用户！

只配置后端本体的服务，自行使用其他程序反代，或者 CDN 直接回源到后端。

以下内容保存为 `/opt/backend/docker-compose.yaml`

```yaml
services:
  nya:
    image: alpine
    network_mode: host
    restart: always
    volumes:
      - .:/opt/backend
    working_dir: /opt/backend
    command: ./rel_backend
    logging:
      driver: "json-file"
      options:
        max-size: "50m"
        max-file: "3"
```

> **nginx 用户提示**
>
> 如果你使用 nginx 反代，请自行搜索开启 Websocket 的配置，否则探针无法使用。

> **Cloudflare 用户提示**
>
> 如果你按照本教程的 caddy 配置，则需要把 Cloudflare 的 SSL/TLS 模式修改为 `Full / 完全`。

<a id="4"></a>
## 4 创建数据库

第一次运行，请初始化数据库：

```shell
MIGRATE=1 ADMIN="admin" ./rel_backend
```

其中 `admin` 是默认管理员账户名，可自行修改。

如果无误，运行此命令将输出管理员的帐号与密码。

<a id="5"></a>
## 5 启动

```shell
docker compose up -d
docker compose logs # 查看日志
```

<a id="6-dns"></a>
## 6 设置 DNS 解析

您的域名设置 DNS 解析到您的服务器 IP，视情况可以开启 CDN 代理。

<a id="7"></a>
## 7 使用

使用浏览器访问 `你的域名` ，直接登录管理员帐号即可。

由于网站使用了某些前端资源，“墙内”的访问质量可能不佳。我们强烈建议“墙内用户”在**代理环境下**打开您的网站。

**如果您使用浏览器打开时提示「不安全的证书」，那么您的节点端可能无法正常上线！请返回反代步骤，重新配置有效的证书。**
