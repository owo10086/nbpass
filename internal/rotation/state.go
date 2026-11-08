package rotation

// Reasons 仅允许安全的固定摘要，第三方错误及响应不进入动作、事件或通知。
var Reasons = map[string]string{
	"event_storage_failed": "开始事件持久化失败，已取消且未调用接口",
	"invalid_config":       "换 IP 请求配置无效，已取消",
	"started":              "已持久化动作，准备调用接口",
	"observing":            "接口调用已结束，等待确认公网 IPv4 变化",
	"changed":              "已观察到公网 IPv4 变化；可达性尚未复检",
	"query_failed":         "换前公网 IPv4 查询失败，已取消且未调用接口",
	"inconsistent":         "两个查询源不一致，疑似 NAT 或多 IP，已取消",
	"old_ip_mismatch":      "当前公网 IPv4 与许可绑定的旧 IP 不符，已取消",
	"expired":              "执行许可已过期，已取消且未调用接口",
	"offline":              "控制连接已中断，已取消且未调用接口",
	"unconfirmed":          "观察到期仍未确认 IP 变化，不得重新调用接口",
	"http_unconfirmed":     "接口请求结果未确认，继续观察公网 IPv4",
}
