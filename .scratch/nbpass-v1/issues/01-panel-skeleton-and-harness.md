# 01: 面板骨架、首次设置密码与登录，以及集群测试台

**What to build:** 管理员启动面板单二进制，浏览器打开内嵌的 React + AntD 前端：首次访问设置管理员密码，之后用密码登录、退出；会话有空闲超时和绝对期限，写操作有同源校验。面板首次启动生成本机密钥文件，之后所有需要再次下发的秘密都用它加密存储。同时建立进程内集群测试台的第一版：在测试进程内启动真实的面板核心，注入可快进时钟，只通过面板 HTTP API 断言。

**Blocked by:** None (can start immediately)

**Status:** ready-for-agent

**Refs:** 规格用户故事 3、6、7、10、12；规格「部署形态」「认证与会话」「秘密存储」「Testing Decisions」

- [ ] 单个 Go 二进制同时提供内嵌前端和 API；网页 HTTP 端口与 gRPC 控制端口分开配置
- [ ] SQLite 以 WAL 模式打开，有版本化迁移
- [ ] 未设置密码时只允许设置密码；之后用密码登录。密码用加盐慢哈希保存，密码校验并发受限
- [ ] 会话可撤销，空闲 24 小时、绝对 7 天到期；Cookie 为 HttpOnly、Secure、SameSite
- [ ] 跨源写请求被拒
- [ ] 首次启动生成最小读写权限的本机密钥文件，提供秘密的加密存取；数据库中不出现秘密明文
- [ ] 时钟、文件系统持久化等外部依赖经可注入接口进入面板核心；测试台可在进程内启动面板核心、快进时钟、调用 HTTP API
- [ ] 以上行为由测试台通过面板 HTTP API 验证（会话到期用快进时钟）
- [ ] 登录页、首次设置页和空的主框架（人工验收）

## Comments

2026-11-08 实施记录（当前 main 工作树，未创建 worktree，未提交 Git）：

- 工程：Go 单模块 `nbpass`；`cmd/nbpass` 负责启动、独立 HTTP/控制监听和退出；`internal/panel` 是真实面板核心；`internal/platform` 提供 SQLite、本机密钥文件及真实时钟；`internal/clusterharness` 提供进程内测试台；React + AntD + Vite + TypeScript 位于 `web/`，`web/embed.go` 内嵌 `web/dist/`。保留构建产物，使检出后可直接运行 Go 检查；`Makefile` 提供 `build`、`test`、`linux-amd64`。
- 认证：首次设置成功后进入登录页，不自动登录；密码限制为 12–72 字节，使用 bcrypt cost 12 和随机盐；哈希与校验共享并发上限，默认 4，超额返回 429。初始化通过数据库唯一约束保证并发请求只有一次成功。来源 IP 失败锁定、可信代理、修改/重置密码属于票 06。
- 会话：随机 256 位 token，仅 SHA-256 摘要入库；持久化创建时间与最后活动时间；空闲 24 小时或绝对 7 天到期，边界时刻即失效。支持单会话退出、全部会话撤销；Cookie 为 `__Host-nbpass_session`、HttpOnly、Secure、SameSite=Strict、Path=/，无 Domain。
- 写请求：必须携带与显式 `-public-origin` 匹配的 Origin，不采信转发头；拒绝跨源、缺失 Origin 和 `Origin: null`。公共源规范化域名大小写、默认 443 端口及末尾根路径，防止合法配置导致浏览器写入全被拒。
- 存储：纯 Go `modernc.org/sqlite`，WAL、busy timeout、foreign keys、synchronous FULL；按 `PRAGMA user_version` 事务化迁移，当前版本 1，高于程序支持的版本拒绝打开。数据目录 0700，数据库和本机密钥文件 0600；密钥为 32 字节随机值，经同步临时文件和原子硬链接发布，不覆盖已有密钥，拒绝符号链接、错误权限和错误长度。秘密用 AES-256-GCM 加密，名称作为关联数据；加密哨兵检查本机密钥与数据库是否匹配。
- HTTP API：`GET /api/auth/status`；`POST /api/auth/setup`、`/api/auth/login`、`/api/auth/logout`、`/api/auth/sessions/revoke`；认证后的 `GET /api/overview`；`PUT /api/secrets/{name}` 接收 `{"value":"..."}`，`GET` 同路径验证可解密并只返回 `{"configured":true}`，完整秘密只供核心内部消费者读取，不向浏览器回显。所有非公开 API 在未初始化时返回 428，未登录时返回 401。静态页面和初始化状态 API 是加载首次设置页所必需的公开入口。
- 启动：先 `make build`，再执行 `./bin/nbpass -public-origin https://panel.example.com -http-listen 127.0.0.1:8080 -control-listen 127.0.0.1:8081 -data-dir ./data`；HTTPS 由管理员的反向代理终结。控制端口目前只接受并关闭连接，不实现 gRPC；非回环控制监听须显式指定 `-trust-control-network`。
- 测试台：`clusterharness.New(t, func(*panel.Dependencies){...})` 注入依赖；`h.Client()` 创建独立 CookieJar 的浏览器；`c.Do(method, path, body, expectedStatus)` 通过真实 HTTPS 传输操作面板 HTTP API；`h.Clock.Advance(...)` 快进时钟；`h.Restart()` 保留磁盘重建真实核心并返回启动错误。测试 HTTPS 仅模拟面板外的 TLS 终结，生产核心本身只提供 HTTP。后续票可扩展 `Dependencies`，加入节点、网络和假 DNSPod 等外部依赖；核心入口为 `panel.Open(ctx, Config, Dependencies)`、`Handler()`，秘密消费者使用 `Secrets().Put/Get`。
- TDD 与复核：首轮 API 门禁、认证、会话、同源测试先以 501 失败；秘密 API、静态资源路由及公共源序列化也观察到相应失败后实现。复核发现并修正测试台共享 CookieJar 的竞态（新增独立浏览器退出回归先复现失败），以及登录表单卸载后保留密码的问题。前端登录成功和表单卸载均清空密码。
- 已验证：`env -u FORCE_COLOR make test`（依次执行前端 `npm ci`、TypeScript/Vite build、`go vet ./...`、`go test ./...`）通过；`go test -race ./... -count=1` 通过，16 个顶层 HTTP 验收测试，另含公共源配置子场景。覆盖错误密码、退出后 Cookie 重放、独立会话、全部撤销、两类到期和重启后到期、跨源写入、Cookie 属性、校验并发、并发首次设置、秘密持久化及不回显、请求校验、随机源与持久化故障。Linux amd64 以 `CGO_ENABLED=0` 成功交叉编译，产物为静态链接 ELF；本机二进制冒烟验证内嵌页面及全部引用资源、设置密码、登录 Cookie、独立控制占位端口和 SIGTERM 正常退出。
- 已决定的测试边界：用户允许为物理属性补充独立白盒断言，测试台 `clusterharness` 的业务断言仍只走 HTTP API，不在测试台读取面板私有状态，也不新增存储诊断 API。物理断言位于 `internal/platform/storage_test.go`（真实数据目录 0700、密钥/数据库及运行中的 WAL/SHM 0600、WAL 模式与重开、密钥持久化，以及拒绝符号链接/错误权限/错误长度）、`internal/panel/storage_test.go`（当前迁移版本及重开持久化、拒绝高版本且不修改其版本、运行中 WAL 与 checkpoint/关闭后的主库不含秘密/原始会话 token/管理员密码明文、存储哈希与会话摘要的正向验证、重开后秘密可解密）和 `internal/panel/passwords_test.go`（同密码两次 bcrypt 哈希不同、均可校验且 cost 为 12）。新增 7 个顶层测试首次运行全绿，未发现实现缺陷，无需修改生产代码；本轮 `make test` 与 `go test -race ./... -count=1` 均通过。
- 待人工验收：浏览器控制连接不可用，尚未进行页面视觉验收。通过 HTTPS 代理打开新数据目录上的面板，确认首次设置与密码确认、设置后登录页、错误密码提示、登录后空主框架、移动端布局、退出后密码输入框为空、另一浏览器会话不受单次退出影响、退出全部会话后两端均需重新登录。Linux 文件权限验收应在进程运行时用 `stat` 检查数据目录 0700，密钥、数据库及 WAL/SHM 文件 0600，所属用户为运行服务的用户；数据库验收检查 `PRAGMA journal_mode` 为 `wal`、`PRAGMA user_version` 为 `1`，重启后密码和秘密仍可使用；这些人工项尚未执行。
