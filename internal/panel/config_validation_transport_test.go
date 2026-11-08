package panel

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// 仅测传输物理属性：校验错误和引用清单写给慢读者时，必须释放数据库和控制锁。
func TestValidationSlowHTTPReaderDoesNotBlockRevocation(t *testing.T) {
	for _, operation := range []string{"references", "delete", "conflict"} {
		t.Run(operation, func(t *testing.T) {
			deps := diskDependencies(t.TempDir())
			deps.Passwords = BcryptPasswords{Cost: 4}
			p, err := Open(context.Background(), Config{PublicOrigin: "https://panel.test"}, deps)
			if err != nil {
				t.Fatal(err)
			}
			defer p.Close()
			diskRequest(t, p, "POST", "/api/auth/setup", map[string]string{"password": "administrator-password-123"}, nil, 201)
			login := diskRequest(t, p, "POST", "/api/auth/login", map[string]string{"password": "administrator-password-123"}, nil, 200)
			cookie := login.Result().Cookies()[0]
			created := diskRequest(t, p, "POST", "/api/nodes", map[string]string{"name": "慢读者节点"}, cookie, 201)
			var node, group struct{ ID string }
			if err := json.Unmarshal(created.Body.Bytes(), &node); err != nil {
				t.Fatal(err)
			}
			created = diskRequest(t, p, "POST", "/api/entry-groups", map[string]any{"name": "慢读者入口组", "members": []string{node.ID}}, cookie, 201)
			if err := json.Unmarshal(created.Body.Bytes(), &group); err != nil {
				t.Fatal(err)
			}
			method, path := "GET", "/api/nodes/"+node.ID+"/references"
			var body []byte
			if operation == "delete" {
				method, path = "DELETE", "/api/nodes/"+node.ID
			} else if operation == "conflict" {
				method, path = "POST", "/api/rules"
				body, err = json.Marshal(map[string]any{"name": "冲突规则", "entry_group_id": group.ID, "kind": "direct", "listen_port": 18080, "target_address": "example.com", "target_family": "ipv4", "target_port": 443, "enabled": true})
				if err != nil {
					t.Fatal(err)
				}
			}
			request := httptest.NewRequest(method, "https://panel.test"+path, bytes.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Origin", "https://panel.test")
			request.AddCookie(cookie)
			w := &stalledProbeWriter{ResponseRecorder: httptest.NewRecorder(), entered: make(chan struct{}), release: make(chan struct{})}
			readDone := make(chan struct{})
			go func() { defer close(readDone); p.Handler().ServeHTTP(w, request) }()
			defer func() { close(w.release); <-readDone }()
			select {
			case <-w.entered:
			case <-time.After(2 * time.Second):
				t.Fatal("未进入阻塞响应")
			}
			revoke := httptest.NewRequest("POST", "https://panel.test/api/nodes/"+node.ID+"/token/revoke", nil)
			revoke.Header.Set("Origin", "https://panel.test")
			revoke.AddCookie(cookie)
			result := httptest.NewRecorder()
			writeDone := make(chan struct{})
			go func() { defer close(writeDone); p.Handler().ServeHTTP(result, revoke) }()
			select {
			case <-writeDone:
				if result.Code != http.StatusNoContent {
					t.Fatalf("吊销失败: %d %s", result.Code, result.Body.String())
				}
			case <-time.After(time.Second):
				t.Fatal("慢读校验响应阻塞了凭据撤销")
			}
		})
	}
}
