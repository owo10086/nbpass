<!-- Source URL: https://docs.relayx.cc/deploy/prepare/stackauth | Capture date: 2026-11-10 -->

# StackAuth 服务

我们使用了 [StackAuth](https://stack-auth.com) 服务来管理用户登录注册。

<a id="使用-saas-服务"></a>

## 使用 SaaS 服务

StackAuth 提供了免费的 SaaS 服务，月活用户 1W 以下免费。推荐优先使用 SaaS 服务，省去维护成本。

使用步骤：

1. 前往 [https://app.stack-auth.com](https://app.stack-auth.com) 注册账号
2. 创建项目
3. 配置登录方式

   ![stackauth](https://docs.relayx.cc/assets/images/auth-method-c325a4fea576c2f6764ec93cf80f7710.png)
4. 配置你的域名

   ![stackauth](https://docs.relayx.cc/assets/images/domain-8aa84123f7af848c2be84aec961fbd2d.png)
5. 配置邮箱服务

   ![stackauth](https://docs.relayx.cc/assets/images/email-3b3416050cae50d7526422fe0cb0f83e.png)
6. 获取 API 密钥

   进入项目设置页面，找到 API Keys 部分，获取以下三个值，后续部署 RelayX 时需要用到：

   - `STACK_PROJECT_ID` - 项目 ID
   - `STACK_PUBLISHABLE_CLIENT_KEY` - 客户端公钥
   - `STACK_SECRET_SERVER_KEY` - 服务端密钥

使用 SaaS 服务时，`STACK_API_URL` 保持默认值 `https://api.stack-auth.com` 即可。

<a id="使用自建服务"></a>

## 使用自建服务

如果月活用户超过 1W，或者有数据合规要求，可以考虑自建 StackAuth 服务。

> **注意**
>
> 自建服务需要你自行负责更新和维护，包括安全补丁和 bug 修复。

<a id="前置要求"></a>

### 前置要求

- PostgreSQL 数据库（StackAuth 使用 PostgreSQL，非 MySQL）
- ClickHouse 数据库（用于数据分析）

<a id="docker-compose-部署"></a>

### Docker Compose 部署

你可以参考以下 docker compose 部署示例：

**compose.yaml**

```yaml
services:
  stackauth:
    image: stackauth/server:latest
    container_name: stackauth
    restart: always
    env_file:
      - .env.stackauth
    ports:
      - 8101:8101
      - 8102:8102
    depends_on:
      stackauth-db:
        condition: service_healthy
      stackauth-clickhouse:
        condition: service_healthy
  stackauth-db:
    image: postgres:latest
    container_name: stackauth-db
    restart: always
    environment:
      POSTGRES_USER: postgres
      POSTGRES_PASSWORD: changeme
      POSTGRES_DB: stackauth
    volumes:
      - stackauth_db:/var/lib/postgresql
    healthcheck:
      test: ["CMD", "pg_isready", "-U", "postgres"]
  stackauth-clickhouse:
    image: clickhouse/clickhouse-server:latest
    container_name: stackauth-clickhouse
    restart: always
    environment:
      CLICKHOUSE_DB: analytics
      CLICKHOUSE_USER: stackframe
      CLICKHOUSE_PASSWORD: changeme
      CLICKHOUSE_DEFAULT_ACCESS_MANAGEMENT: 1
    volumes:
      - stackauth_clickhouse:/var/lib/clickhouse
    healthcheck:
      test: ["CMD", "clickhouse-client", "--user", "stackframe", "--password", "changeme", "--query", "SELECT 1"]

volumes:
  stackauth_db:
    name: stackauth-db
  stackauth_clickhouse:
    name: stackauth-clickhouse
```

准备环境变量文件 `.env.stackauth`：

**.env.stackauth**

```python
# （必填）API 和 Dashboard 地址，部署到服务器时需替换为实际域名
NEXT_PUBLIC_STACK_API_URL="http://localhost:8102"
NEXT_PUBLIC_STACK_DASHBOARD_URL="http://localhost:8101"
NEXT_PUBLIC_BROWSER_STACK_API_URL="http://localhost:8102"
# （必填）数据库连接地址
STACK_DATABASE_CONNECTION_STRING="postgresql://postgres:changeme@stackauth-db:5432/stackauth"
# （必填）修改为一个随机字符串，用于加密
STACK_SERVER_SECRET="changeme-to-a-random-string"
# 启用注册和 OTP 登录
STACK_SEED_INTERNAL_PROJECT_SIGN_UP_ENABLED=true
STACK_SEED_INTERNAL_PROJECT_OTP_ENABLED=true
STACK_SEED_INTERNAL_PROJECT_ALLOW_LOCALHOST=true
# （必填）ClickHouse 连接地址
STACK_CLICKHOUSE_URL="http://stackauth-clickhouse:8123"
# （必填）ClickHouse 管理员密码，需与 compose 中 CLICKHOUSE_PASSWORD 一致
STACK_CLICKHOUSE_ADMIN_PASSWORD="changeme"
# （必填）ClickHouse 外部访问密码
STACK_CLICKHOUSE_EXTERNAL_PASSWORD="changeme"
# （必填）Vercel Sandbox 令牌，在 https://vercel.com/account/settings/tokens 生成
STACK_VERCEL_SANDBOX_TOKEN="xxx"
# （必填）用于发送验证码邮件，可在 https://dash.freestyle.sh/api-tokens 生成
STACK_FREESTYLE_API_KEY="xxx"
```

> **提示**
>
> 更多环境变量配置请参考 [官方文档](https://github.com/stack-auth/stack-auth/blob/dev/docker/server/.env)。

启动服务：

```bash
docker compose up -d
```

启动后，Dashboard 访问地址为 `http://your-server-ip:8101`，API 地址为 `http://your-server-ip:8102`。

<a id="配置步骤"></a>

### 配置步骤

1. 打开 Dashboard（端口 8101），注册管理员账号
2. 创建项目，获取 API 密钥（同 SaaS 服务步骤 6）
3. 在 RelayX 的 `.env` 中将 `STACK_API_URL` 设置为你的自建 API 地址，例如 `https://your-stackauth-domain.com`（建议配置反向代理和 HTTPS）
