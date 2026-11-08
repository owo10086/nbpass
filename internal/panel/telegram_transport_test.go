package panel

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type telegramRoundTrip func(*http.Request) (*http.Response, error)

func (f telegramRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// 真实适配器必须正确编码、识别 Bot API 拒绝/限流，且不把 token 或响应带入错误。
func TestTelegramTransportValidatesBotAPIAndRedactsErrors(t *testing.T) {
	const token = "12345:secret-transport-marker-abcdefghijklmnop"
	for _, test := range []struct {
		name           string
		status         int
		body           string
		transportError bool
		wantError      bool
		retry          time.Duration
	}{
		{"success", 200, `{"ok":true,"result":{"message_id":1}}`, false, false, 0},
		{"rejected", 200, `{"ok":false,"description":"` + token + `"}`, false, true, 0},
		{"malformed", 200, `{`, false, true, 0},
		{"oversized", 200, strings.Repeat("x", 65537), false, true, 0},
		{"http-error", 502, token, false, true, 0},
		{"redirect", 302, token, false, true, 0},
		{"network", 0, "", true, true, 0},
		{"rate-limit", 429, `{"ok":false,"error_code":429,"parameters":{"retry_after":30}}`, false, true, 30 * time.Second},
	} {
		t.Run(test.name, func(t *testing.T) {
			called := false
			sender := telegramHTTP{client: &http.Client{Transport: telegramRoundTrip(func(r *http.Request) (*http.Response, error) {
				called = true
				if r.Method != "POST" || r.URL.Scheme != "https" || r.URL.Host != "api.telegram.org" || r.URL.Path != "/bot"+token+"/sendMessage" {
					t.Errorf("发送端点错误: %s %s", r.Method, r.URL.Redacted())
				}
				var payload map[string]any
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Error(err)
				}
				if payload["chat_id"] != "-10012345" || payload["text"] != "节点「A<&」恢复上线" || payload["parse_mode"] != nil {
					t.Errorf("发送载荷错误: %v", payload)
				}
				if test.transportError {
					return nil, errors.New("network: " + token)
				}
				return &http.Response{StatusCode: test.status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(test.body))}, nil
			})}}
			err := sender.Send(context.Background(), TelegramMessage{BotToken: token, ChatID: "-10012345", Text: "节点「A<&」恢复上线"})
			if !called || (err != nil) != test.wantError {
				t.Fatalf("发送结果: called=%v err=%v", called, err)
			}
			if err != nil {
				if strings.Contains(err.Error(), token) {
					t.Fatal("第三方错误泄露 token")
				}
				var safe telegramSendError
				if !errors.As(err, &safe) || safe.retryAfter != test.retry {
					t.Fatalf("安全错误/限流信息丢失: %v", err)
				}
			}
		})
	}
}
