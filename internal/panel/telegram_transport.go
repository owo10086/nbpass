package panel

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"
)

type telegramHTTP struct{ client *http.Client }

var defaultTelegram = telegramHTTP{client: &http.Client{
	Transport:     &http.Transport{ForceAttemptHTTP2: true},
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}}

func (s telegramHTTP) Send(ctx context.Context, message TelegramMessage) error {
	payload, err := json.Marshal(map[string]any{"chat_id": message.ChatID, "text": message.Text, "link_preview_options": map[string]bool{"is_disabled": true}})
	if err != nil {
		return telegramSendError{message: "Telegram 请求无法编码"}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.telegram.org/bot"+message.BotToken+"/sendMessage", bytes.NewReader(payload))
	if err != nil {
		return telegramSendError{message: "Telegram 请求无效"}
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := s.client.Do(request)
	if err != nil {
		return telegramSendError{message: "Telegram 网络请求失败"}
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 65537))
	if err != nil || len(data) > 65536 {
		return telegramSendError{message: "Telegram 响应无效或过大"}
	}
	var result struct {
		OK         bool `json:"ok"`
		Code       int  `json:"error_code"`
		Parameters struct {
			RetryAfter int `json:"retry_after"`
		} `json:"parameters"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return telegramSendError{message: "Telegram 响应无法解析"}
	}
	if response.StatusCode == 429 || result.Code == 429 {
		delay := result.Parameters.RetryAfter
		if delay < 1 {
			delay = 1
		}
		if delay > 3600 {
			delay = 3600
		}
		return telegramSendError{message: "Telegram 请求受到限流，稍后重试", retryAfter: time.Duration(delay) * time.Second}
	}
	if response.StatusCode != http.StatusOK || !result.OK {
		return telegramSendError{message: "Telegram 拒绝发送，请检查 Bot token 和聊天目标"}
	}
	return nil
}
