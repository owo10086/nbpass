<!-- Source URL: https://docs.relayx.cc/deploy/deploy-dash | Capture date: 2026-11-10 -->

# 部署面板

我们目前只提供 `docker compose` 部署方式。当然如果你愿意，你也可以部署到 k8s 上。

<a id="安装-docker"></a>

## 安装 Docker

```bash
curl -fsSL https://get.docker.com | bash
```

如果你的机器在国内：

```bash
curl -fsSL https://get.docker.com | bash -s docker --mirror Aliyun
```

<a id="使用-docker-compose-部署"></a>

## 使用 Docker Compose 部署

复制以下内容到 `compose.yaml` 文件中：

**compose.yaml**

```yaml
services:
  caddy:
    image: caddy
    cap_add:
      - NET_ADMIN
    container_name: caddy
    restart: always
    volumes:
      - ./Caddyfile:/etc/caddy/Caddyfile
      - caddy_data:/data
      - caddy_config:/config
    ports:
      - 80:80
      - 443:443
  db-migrate:
    image: relayx/db-migrate
    container_name: db-migrate
    env_file:
      - .env
    depends_on:
      mysql:
        condition: service_healthy
  relayx:
    image: relayx/backend
    container_name: backend
    restart: always
    env_file:
      - .env
    depends_on:
      redis:
        condition: service_healthy
      mysql:
        condition: service_healthy
      db-migrate:
        condition: service_completed_successfully
  worker:
    image: relayx/worker
    container_name: worker
    restart: always
    env_file:
      - .env
    depends_on:
      redis:
        condition: service_healthy
      mysql:
        condition: service_healthy
      db-migrate:
        condition: service_completed_successfully
  web:
    image: relayx/web
    container_name: web
    restart: always
    env_file:
      - .env
    depends_on:
      - relayx
  redis:
    image: redis
    container_name: redis
    restart: always
    volumes:
      - redis:/data
    healthcheck:
      test: ["CMD", "redis-cli", "ping"]
  mysql:
    image: mysql
    container_name: mysql
    environment:
      MYSQL_ROOT_PASSWORD: changeme
      MYSQL_DATABASE: relayx
    restart: always
    command: --skip-log-bin
    volumes:
      - mysql:/var/lib/mysql
    healthcheck:
      test: ["CMD", "mysqladmin", "ping", "-h", "localhost"]
volumes:
  redis:
    name: relayx-redis
  mysql:
    name: relayx-mysql
  caddy_data:
    name: relayx-caddy-data
  caddy_config:
    name: relayx-caddy-config
```

准备环境变量文件 `.env`，放在和 `compose.yaml` 同级目录下，内容如下：

**.env**

```python
# 无特殊需求保持默认值
DATABASE_URL="mysql://root:changeme@mysql:3306/relayx"
# 无特殊需求保持默认值
REDIS_URL="redis://redis:6379"
# （必填）替换成你自己的域名
SITE_URL="https://www.relayx.cc"
# 无特殊需求保持默认值
STACK_API_URL="https://api.stack-auth.com"
# （必填）替换成你自己的 StackAuth 项目 ID
STACK_PROJECT_ID="xxx"
# （必填）替换成你自己的 StackAuth 服务密钥
STACK_SECRET_SERVER_KEY="xxx"
# （必填）替换成你自己的 StackAuth 客户端密钥
STACK_PUBLISHABLE_CLIENT_KEY="xxx"
# （可选）替换成你自己的 Telegram 机器人 token，不配置则不启用 Telegram 通知
# TELEGRAM_BOT_TOKEN="xxx"
# 无特殊需求保持默认值
API_URL="http://relayx:3000"
```

准备 Caddyfile 文件，放在和 `compose.yaml` 同级目录下，内容如下：

**Caddyfile**

```caddy
# 替换为你的域名
www.your-domain.com {
    reverse_proxy web:3000
}
```

启动服务：

```bash
docker compose up -d
```

查看所有服务正常启动：

```bash
docker compose ps
```

![docker-compose-ps](https://docs.relayx.cc/assets/images/docker-compose-ps-4d0eac472614e85866b2e97bb3811882.png)

查看日志无异常：

```bash
docker compose logs -f
```

![docker-compose-logs](https://docs.relayx.cc/assets/images/docker-compose-logs-d4ca6881eaea9d76b0bc3bf566b44764.png)

<a id="配置-https-和-websocket-支持"></a>

## 配置 HTTPS 和 websocket 支持

一般情况，你的服务器会通过域名访问，所以你需要配置 HTTP 反向代理并且配置 HTTPS。

目前常用的反向代理有 `nginx` 和 `caddy`，如果你使用我们提供的 `compose.yaml`，则不需要任何配置，因为里面自带的 `caddy` 会自动处理 HTTPS 证书和 websocket 支持，只需要你的域名已经解析到你的服务器 IP。

如果你使用外部的 `caddy` 或者 `nginx` 作为反向代理，你需要做一些调整。

1. `compose.yaml` 里面的 `caddy` 服务需要删除，因为端口 80 和 443 会被占用。
2. 暴露 `web` 服务的 3000 端口。

**compose.yaml**

```yaml
services:
  web:
    ports:
      - 3000:3000
```

<a id="配置-nginx-支持"></a>

### 配置 nginx 支持

首先需要在 `nginx.conf` 中添加 websocket 支持的映射配置：

**nginx.conf**

```nginx
http {
    # websocket 支持配置
    map $http_upgrade $connection_upgrade {
        default upgrade;
        '' close;
    }

    # ... 其他配置
}
```

然后在你的站点配置中添加 websocket 支持：

**site.conf**

```nginx
server {
    listen 80;
    listen 443 ssl http2;
    server_name your-domain.com;

    # SSL 配置
    ssl_certificate /path/to/your/cert.pem;
    ssl_certificate_key /path/to/your/key.pem;

    location / {
        proxy_pass http://127.0.0.1:3000;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;

        # websocket 支持
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection $connection_upgrade;
        proxy_read_timeout 3600s;
        proxy_send_timeout 3600s;
    }
}
```

恭喜你，面板部署成功！现在你可以通过 `https://your_domain.com` 访问面板了。

<a id="升级"></a>

## 升级

我们会频繁更新面板和修复 bug，请始终保持最新版。你的数据是安全的，不会丢失。

```bash
docker compose down && docker compose up -d --pull always
```

<a id="停止服务"></a>

## 停止服务

```bash
docker compose down
```

<a id="删除服务以及数据库数据"></a>

## 删除服务以及数据库数据

> **危险**
>
> 这个命令会删除所有数据，请谨慎执行。

```bash
docker compose down -v
```
