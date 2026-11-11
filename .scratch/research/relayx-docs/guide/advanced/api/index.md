<!-- Source URL: https://docs.relayx.cc/guide/advanced/api | Capture date: 2026-11-10 -->

# API 使用指南

RelayX 提供 RESTful API 接口，支持通过 API Key 进行身份验证，方便对接自动化工具和第三方系统。

> **信息**
>
> API 功能需要商业授权才能使用。

<a id="获取-api-key"></a>

## 获取 API Key

1. 登录 RelayX 面板
2. 进入「个人中心」
3. 在 API Key 区域点击「重置」获取你的 API Key（仅显示一次，请立即复制保存）

<a id="api-文档"></a>

## API 文档

RelayX 提供了完整的 OpenAPI 文档，部署完成后可以通过以下地址访问：

- **API 文档（Scalar UI）**：`https://你的域名/docs`
- **OpenAPI JSON**：`https://你的域名/openapi.json`

<a id="认证方式"></a>

## 认证方式

所有 API 请求需要在 HTTP Header 中携带 API Key：

```bash
Authorization: Bearer 你的API_KEY
```

<a id="使用示例"></a>

## 使用示例

<a id="获取用户信息"></a>

### 获取用户信息

```bash
curl -X GET https://你的域名/user/info \
  -H "Authorization: Bearer 你的API_KEY"
```

<a id="获取隧道列表"></a>

### 获取隧道列表

```bash
curl -X GET https://你的域名/tunnel \
  -H "Authorization: Bearer 你的API_KEY"
```

<a id="创建隧道"></a>

### 创建隧道

```bash
curl -X POST https://你的域名/tunnel \
  -H "Authorization: Bearer 你的API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "我的隧道",
    "tunnel_type": "wss",
    "listen_port": 8080,
    "in_node_group_id": 1,
    "out_node_group_id": 2,
    "forward_addresses": [
      {
        "address": "127.0.0.1:3000",
        "weight": 1
      }
    ]
  }'
```

<a id="更新隧道"></a>

### 更新隧道

```bash
curl -X PATCH https://你的域名/tunnel/1 \
  -H "Authorization: Bearer 你的API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "新名称",
    "forward_addresses": [
      {
        "address": "127.0.0.1:8080",
        "weight": 1
      }
    ]
  }'
```

<a id="启用停用隧道"></a>

### 启用/停用隧道

```bash
curl -X PATCH https://你的域名/tunnel/1/status \
  -H "Authorization: Bearer 你的API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "status": "active"
  }'
```

<a id="删除隧道"></a>

### 删除隧道

```bash
curl -X DELETE https://你的域名/tunnel \
  -H "Authorization: Bearer 你的API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "ids": [1, 2, 3]
  }'
```

<a id="获取流量统计"></a>

### 获取流量统计

```bash
curl -X GET "https://你的域名/tunnel/traffic/stats?type=day" \
  -H "Authorization: Bearer 你的API_KEY"
```

<a id="主要-api-端点"></a>

## 主要 API 端点

| 路径 | 方法 | 说明 |
| --- | --- | --- |
| `/user/info` | GET | 获取用户信息和当前套餐 |
| `/tunnel` | GET | 获取隧道列表 |
| `/tunnel` | POST | 创建隧道 |
| `/tunnel/:id` | PATCH | 更新隧道 |
| `/tunnel/:id/status` | PATCH | 启用/停用隧道 |
| `/tunnel` | DELETE | 批量删除隧道 |
| `/tunnel/import` | POST | 批量导入隧道 |
| `/tunnel/traffic/stats` | GET | 流量统计 |
| `/tunnel/stats` | GET | 隧道状态统计 |
| `/node/group` | GET | 获取节点组列表 |
| `/node/group/summary` | GET | 节点组概要 |
| `/node` | GET | 获取节点列表 |
| `/plan` | GET | 获取套餐列表 |
| `/plan/:id/subscribe` | POST | 订阅套餐 |
| `/plan/renew` | POST | 续费套餐 |

> **提示**
>
> 完整的 API 接口文档和参数说明请访问 `/docs` 页面，支持在线调试。

<a id="错误码"></a>

## 错误码

| HTTP 状态码 | 说明 |
| --- | --- |
| 200 | 请求成功 |
| 400 | 请求参数错误 |
| 401 | 认证失败（API Key 无效或过期） |
| 403 | 权限不足（需要商业授权或套餐不满足） |
| 404 | 资源不存在 |
| 500 | 服务器内部错误 |
