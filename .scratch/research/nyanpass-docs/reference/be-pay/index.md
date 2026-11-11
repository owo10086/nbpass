<!-- Source URL: https://nyanpass.pages.dev/reference/be_pay/ | Capture date: 2026-11-10 -->

<a id="_1"></a>
# 支付网关参考

本文档说明系统支持的支付网关类型及其配置参数。

<a id="epay"></a>
## Epay（易支付）

<a id="url"></a>
### 基本 URL 格式

| 模式 | URL 格式 |
| --- | --- |
| submit | `https://域名/submit.php` |
| mapi | `https://域名/mapi.php?type=alipay&device=pc` |

<a id="_2"></a>
### 支付模式

<a id="submit"></a>
#### submit 模式（默认）

用户直接向支付网关提交订单。一般情况下，用户可在收银台选择支付通道。

<a id="mapi"></a>
#### mapi 模式

触发条件：URL 中含有 `mapi.php` **或** Query 参数中含有 `mapi=1`。

由面板服务器提交订单，并指定支付通道类型（`type`），如 `alipay`、`wxpay` 等。面板返回支付网关生成的链接或二维码给用户完成支付。

<a id="_3"></a>
### 兼容性

| 类型 | 说明 |
| --- | --- |
| 标准 Epay（彩虹易支付） | ✅ 原生支持 |
| 非标准 Epay | ❌ 不支持，需自行编写 HTTP API 转换器 |

<a id="epusdt"></a>
## Epusdt

<a id="_4"></a>
### 面板配置参数

| 参数 | 说明 |
| --- | --- |
| `PID` | 无需填写 |
| `Secret` | 填写 Api Token |

<a id="epusdt_1"></a>
### 原版 Epusdt

- **API 端点**：`https://域名/api/v1/order/create-transaction`
- **特点**：仅支持 TRC20
- **状态**：项目已归档，**不建议使用**

<a id="bepusdt-acetaffy"></a>
### Bepusdt / AceTaffy（新版）

- **API 端点**：`https://域名/api/v1/order/create-order`
- **特点**：跳转至收银台，用户选择支付方式。无须提前指定链和币种

<a id="acetaffy"></a>
### AceTaffy（旧版）

- **API 端点**：`https://域名/api/v1/order/create-transaction?channel=aptos&exchange_rate=6.8`

| Query 参数 | 类型 | 说明 |
| --- | --- | --- |
| `channel` | 可选 | 支付链，如 `polygon` |
| `exchange_rate` | 可选 | USDT-CNY 汇率，如 `7.2` |

<a id="gmwalletappepusdt"></a>
## GMwalletApp/Epusdt

此版本的 Epusdt 已放弃原有接口，改为兼容 Epay 接口。请参照 Epay 模式进行对接。

<a id="tokenpay"></a>
## TokenPay

<a id="_5"></a>
### 面板配置参数

| 参数 | 说明 |
| --- | --- |
| `PID` | 无需填写 |
| `Secret` | 填写 Api Token |

<a id="api-url"></a>
### API URL 端点与参数

```text
https://域名/CreateOrder?currency=USDT_TRC20
```

| Query 参数 | 说明 |
| --- | --- |
| `currency` | 支持币种，完整列表参见：[TokenPay Currency List](https://github.com/LightCountry/TokenPay/blob/master/Wiki/Currency.md) |

<a id="cryptomus"></a>
## Cryptomus

<a id="_6"></a>
### 面板配置参数

| 参数 | 说明 |
| --- | --- |
| `PID` | 填写 `merchant-id` |
| `Secret` | 填写 `payment-key`（**注意**：不是 `payout-key`） |

<a id="api-url_1"></a>
### API URL 端点与参数

```text
https://cryptomus/?currency=CNY&subtract=100
```

| Query 参数 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `currency` | 可选 | `CNY` | 支付金额的计价币种 |
| `subtract` | 可选 | `0` | 客户承担手续费的百分比，范围 `0` ~ `100` |
