<!-- Source URL: https://nyanpass.pages.dev/reference/be_config/ | Capture date: 2026-11-10 -->

<a id="_1"></a>
# 面板端配置

`/opt/backend/config.yml`

编辑之前确保你已学会基本的 YAML 语法，不要出现语法错误。

```yaml
# 数据库
database-path: sqlite3://data.db
# database-path: "mysql://用户名:密码@tcp(127.0.0.1:3306)/数据库名?charset=utf8mb4&parseTime=true"
# database-path: "postgres://host=localhost user=gorm password=gorm dbname=gorm port=5432 sslmode=disable TimeZone=Asia/Shanghai"

# 数据库连接池选项
# max-open-connection: 100
# max-idle-connection: 5

# 禁用流量队列服务，节点上报的流量可能会被并发提交到数据库（除非遇到问题，否则请保持默认）
# disable-queue: true

# 监听端口
listen: 127.0.0.1:18888

# 授权码，建议在配置文件中指定，也可以在命令中使用 -key 指定
key: xxxxxx

# 前端文件位置（建议不设置此项，而是由 nginx / caddy 等 Web 服务器提供前端文件）
# html-path: ./public

# HTTPS TLS 配置，如果不配置，则监听 HTTP
# tls-cert: ./some.crt
# tls-key: ./some.key

# 禁用自带的网页压缩（除非遇到问题，否则请保持默认）
# disable-gzip: true

# 离线节点保留显示在探针的时间（秒），最低 600 默认 86400
# offline-node-retention-time: 86400

# 探针节点与面板连续多少（秒）不通信，判断为离线，最低 20 默认 20
# offline-node-time: 20

# 用户 API 操作频率限制
# user-rate-limit:
#   rate: 5 # 限制周期，单位：秒
#   limit: 5 # 每个周期内接受的请求数量

# 默认 API 频率限制（例如登录，限制条件为 IP）
# default-rate-limit:
#   rate: 5
#   limit: 5
```

<a id="_2"></a>
## 数据库说明

- sqlite 适用于用户、规则和设备数都不多的站
- mysql 和 postgres 适用于大型站点
- MySQL 最低版本要求为 5.7

<a id="_3"></a>
### 大小写问题

不同数据库对用户名等的大小写处理策略不同，这可能在跨数据库迁移或迁移后出现意外行为。

| 数据库类型 | 默认行为 | 说明 |
| --- | --- | --- |
| MySQL (InnoDB) | **不区分**大小写 | 用户名 `Admin` 与 `admin` 被视为相同 |
| PostgreSQL | **区分**大小写 | 用户名 `Admin` 与 `admin` 被视为不同 |
| SQLite | **区分**大小写 | 同上 |

**迁移注意事项：**

- 从 MySQL 迁移到 PostgreSQL（或反之）时，若业务逻辑依赖用户名大小写敏感性（如判断用户是否存在、唯一性约束），需提前检查并可能需要手动转换数据。
- 常见影响场景：
- **用户名唯一性**：MySQL 下 `Admin` 和 `admin` 被视为相同用户名（只能存在一个），迁移到 PostgreSQL 后可能变成两个独立用户，需注意数据一致性。
- **登录行为**：用户在 MySQL 下不区分大小写的登录方式，迁移到 PostgreSQL 后可能需要精确匹配大小写才能登录。

<a id="_4"></a>
### 队列压力

流量保存到数据库的队列情况可在“仪表盘”查看，0=无待处理任务，数字越大说明压力越高。

20250610 版本起，对大量设备产生流量回报的场景进行了优化，配合默认的流量队列使用，可以降低数据库压力。

以下内容可能已经不适用：

> 如果您遇到压力过高的问题，可以尝试设置 `disable-queue: true`。有些用户设置后问题解决，有些用户（主要是 mysql）设置后反而导致整站锁死，目前难以优化。

<a id="_5"></a>
## 网页压缩

默认开启网页压缩，显著减少面板服务器流量消耗。

有部分用户反馈开启网页压缩导致面板服务器 CPU 占用过高，如果您遇到这种问题，可以尝试设置 `disable-gzip: true`。
