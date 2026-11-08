package panel

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// 仅验证传输物理属性：慢读者不能占住控制锁，业务操作仍经面板 HTTP API。
func TestProbeSlowHTTPReaderDoesNotBlockNodeTokenRevocation(t *testing.T) {
	p := openDiskPanel(t, t.TempDir())
	defer p.Close()
	diskRequest(t, p, "POST", "/api/auth/setup", map[string]string{"password": "administrator-password-123"}, nil, 201)
	login := diskRequest(t, p, "POST", "/api/auth/login", map[string]string{"password": "administrator-password-123"}, nil, 200)
	cookie := login.Result().Cookies()[0]
	created := diskRequest(t, p, "POST", "/api/nodes", map[string]string{"name": "节点 A"}, cookie, 201)
	var installation struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &installation); err != nil {
		t.Fatal(err)
	}
	w := &stalledProbeWriter{ResponseRecorder: httptest.NewRecorder(), entered: make(chan struct{}), release: make(chan struct{})}
	request := httptest.NewRequest("GET", "https://panel.test/api/nodes/"+installation.ID+"/probe", nil)
	request.AddCookie(cookie)
	readDone := make(chan struct{})
	go func() { defer close(readDone); p.Handler().ServeHTTP(w, request) }()
	defer func() { close(w.release); <-readDone }()
	select {
	case <-w.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("没有进入阻塞响应")
	}
	request = httptest.NewRequest("POST", "https://panel.test/api/nodes/"+installation.ID+"/token/revoke", nil)
	request.Header.Set("Origin", "https://panel.test")
	request.AddCookie(cookie)
	result := httptest.NewRecorder()
	writeDone := make(chan struct{})
	go func() { defer close(writeDone); p.Handler().ServeHTTP(result, request) }()
	select {
	case <-writeDone:
		if result.Code != http.StatusNoContent {
			t.Fatalf("吊销失败: %d %s", result.Code, result.Body.String())
		}
	case <-time.After(time.Second):
		t.Fatal("慢读者阻塞了凭据撤销")
	}
}

type stalledProbeWriter struct {
	*httptest.ResponseRecorder
	entered, release chan struct{}
}

func (w *stalledProbeWriter) Write(data []byte) (int, error) {
	close(w.entered)
	<-w.release
	return w.ResponseRecorder.Write(data)
}
