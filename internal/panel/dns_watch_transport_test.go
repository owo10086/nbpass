package panel

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"nbpass/internal/controlpb"
)

// 物理控制协议隔离：恶意 DNS 报告夹带其他载荷也不能推进其他通道，业务读取只经 HTTP。
func TestDNSWatchReportConsumesOnlyItsOwnPayload(t *testing.T) {
	p := openDiskPanel(t, t.TempDir())
	diskRequest(t, p, "POST", "/api/auth/setup", map[string]string{"password": "administrator-password-123"}, nil, 201)
	login := diskRequest(t, p, "POST", "/api/auth/login", map[string]string{"password": "administrator-password-123"}, nil, 200)
	cookie := login.Result().Cookies()[0]
	_, digest, err := p.randomCredential()
	if err != nil {
		t.Fatal(err)
	}
	at := p.deps.Clock.Now().Add(-time.Second).UnixNano()
	if _, err = p.db.Exec("INSERT INTO nodes(id,name,token_hash,created_at,last_seen_at,desired_revision) VALUES('dns-node','检测点',?,?,?,7)", digest, at, at); err != nil {
		t.Fatal(err)
	}
	session := &controlSession{done: make(chan struct{}), instance: "dns-instance"}
	p.controlMu.Lock()
	p.controls["dns-node"] = session
	p.controlMu.Unlock()
	report := &controlpb.NodeReport{DnsWatchOnly: true, ProtocolVersion: 1, ProcessInstance: "dns-instance", AppliedRevision: 7, AppliedDigest: "forged", AttemptedRevision: 7, PublicIpv4: "8.8.8.8", Probe: &controlpb.ProbeSample{Sequence: 7, Values: map[string]float64{"cpu_percent": 42}}, Events: []*controlpb.Event{{EventId: "forged", Type: "node_started", OccurredAt: p.deps.Clock.Now().Format(time.RFC3339Nano), Message: "不应接受"}}}
	if _, err = p.acceptReport(context.Background(), "dns-node", digest, session, report); err != nil {
		t.Fatal(err)
	}
	response := diskRequest(t, p, "GET", "/api/nodes", nil, cookie, 200)
	var nodes struct {
		Nodes []nodeView `json:"nodes"`
	}
	if err = json.Unmarshal(response.Body.Bytes(), &nodes); err != nil {
		t.Fatal(err)
	}
	row := nodes.Nodes[0]
	if row.AppliedRevision != 0 || row.LastSeenAt.UnixNano() != at || row.PublicIPv4 != "" {
		t.Fatalf("DNS 报告推进了其他状态: %+v", row)
	}
	probe := diskRequest(t, p, "GET", "/api/nodes/dns-node/probe", nil, cookie, 200)
	var result map[string]any
	if err = json.Unmarshal(probe.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result["realtime"] != nil {
		t.Fatal("DNS 报告更新了探针")
	}
	events := diskRequest(t, p, "GET", "/api/events", nil, cookie, 200)
	if err = json.Unmarshal(events.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result["events"].([]any)) != 0 {
		t.Fatal("DNS 报告推进了事件确认")
	}
}

// 物理 HTTP 传输：慢读者不能一直占用采样锁，阻塞设置保存和采样结果接收。
func TestDNSWatchSlowHTTPReaderDoesNotBlockSettings(t *testing.T) {
	p := openDiskPanel(t, t.TempDir())
	diskRequest(t, p, "POST", "/api/auth/setup", map[string]string{"password": "administrator-password-123"}, nil, 201)
	login := diskRequest(t, p, "POST", "/api/auth/login", map[string]string{"password": "administrator-password-123"}, nil, 200)
	cookie := login.Result().Cookies()[0]
	w := &stalledProbeWriter{ResponseRecorder: httptest.NewRecorder(), entered: make(chan struct{}), release: make(chan struct{})}
	req := httptest.NewRequest("GET", "https://panel.test/api/dns-watches", nil)
	req.AddCookie(cookie)
	done := make(chan struct{})
	go func() { defer close(done); p.Handler().ServeHTTP(w, req) }()
	defer func() { close(w.release); <-done }()
	select {
	case <-w.entered:
	case <-time.After(time.Second):
		t.Fatal("未进入慢读")
	}
	saved := make(chan struct{})
	go func() {
		defer close(saved)
		diskRequest(t, p, "PUT", "/api/settings/dns-watch", map[string]any{"resolvers": []map[string]string{{"kind": "doh", "address": "https://dns.example/dns-query"}}}, cookie, 200)
	}()
	select {
	case <-saved:
	case <-time.After(time.Second):
		t.Error("慢读者阻塞了解析设置保存")
	}
}
