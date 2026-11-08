package clusterharness_test

import (
	"context"
	"crypto/x509"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"nbpass/internal/clusterharness"
	"nbpass/internal/node"
	"nbpass/internal/panel"
	"nbpass/internal/rotation"
)

// 商家和查询源是外部假实现，节点、磁盘、TLS/gRPC 和面板均为真实核心。
type rotationFixture struct {
	h          *clusterharness.Harness
	c          *clusterharness.Client
	n          *clusterharness.Node
	id, path   string
	ip, second atomic.Value
	calls      atomic.Int32
	queryFails atomic.Bool
}

func newRotationFixture(t *testing.T, timeout bool, options ...func(*node.Config, *node.Dependencies)) *rotationFixture {
	t.Helper()
	h, c := regionBrowser(t)
	return rotationFixtureAt(t, h, c, timeout, options...)
}

func rotationFixtureAt(t *testing.T, h *clusterharness.Harness, c *clusterharness.Client, timeout bool, options ...func(*node.Config, *node.Dependencies)) *rotationFixture {
	t.Helper()
	f := &rotationFixture{h: h, c: c}
	f.ip.Store("223.5.5.5")
	f.second.Store("")
	vendor := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.calls.Add(1)
		if timeout {
			<-r.Context().Done()
			return
		}
		w.WriteHeader(204)
	}))
	t.Cleanup(vendor.Close)
	roots := x509.NewCertPool()
	roots.AddCert(vendor.Certificate())
	created := addNode(c, "未确认冻结")
	f.id = created["id"].(string)
	f.path = "/api/nodes/" + f.id + "/ip-rotation"
	settings := []func(*node.Config, *node.Dependencies){func(_ *node.Config, d *node.Dependencies) {
		d.PublicIPv4 = func(context.Context) (string, error) { return f.ip.Load().(string), nil }
		query := func(context.Context) (string, error) {
			if f.queryFails.Load() {
				return "", errors.New("查询源故障，不得泄漏凭据")
			}
			return f.ip.Load().(string), nil
		}
		d.RotationIPv4Sources = [2]func(context.Context) (string, error){query, func(ctx context.Context) (string, error) {
			if value := f.second.Load().(string); value != "" {
				return value, nil
			}
			return query(ctx)
		}}
		d.RotationLocalIPv4 = func(context.Context) ([]string, error) { return []string{f.ip.Load().(string)}, nil }
		d.RotationHTTP = (rotation.HTTPExecutor{RootCAs: roots}).Do
	}}
	f.n = h.StartNode(created["installation_ticket"].(string), append(settings, options...)...)
	awaitNode(t, c, f.id, connected)
	c.Do("PUT", f.path, map[string]any{"url": vendor.URL + "/change", "method": "POST", "private_endpoints": []string{vendor.URL + "/change"}}, 200)
	return f
}

func (f *rotationFixture) finishObservation(t *testing.T) map[string]any {
	t.Helper()
	for i := 1; i <= 5; i++ {
		f.h.Clock.Advance(time.Minute)
		awaitRotationView(t, f, func(v map[string]any) bool { return v["action"].(map[string]any)["attempts"] == float64(i) })
	}
	return awaitRotation(t, f.c, f.path, "unconfirmed")
}

func awaitRotationView(t *testing.T, f *rotationFixture, ready func(map[string]any) bool) map[string]any {
	t.Helper()
	deadline := time.Now().Add(6 * time.Second)
	var v map[string]any
	for time.Now().Before(deadline) {
		v = f.c.Do("GET", f.path, nil, 200)
		if ready(v) {
			return v
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("换 IP 视图未收敛: %v", v)
	return nil
}

// 缺少持久冻结、仅靠旧 IP 宣告失败、或解除时丢弃去重记录都会破坏此用例。
func TestRotationUnconfirmedFreezeAndManualRelease(t *testing.T) {
	f := newRotationFixture(t, false)
	f.c.Do("POST", f.path+"/actions", nil, 202)
	a := awaitRotation(t, f.c, f.path, "observing")
	f.c.Do("POST", f.path+"/actions", nil, 409)
	f.c.Do("POST", f.path+"/unfreeze", map[string]any{"action_id": a["id"]}, 409)
	f.finishObservation(t)
	v := f.c.Do("GET", f.path, nil, 200)
	if v["frozen"] != true || v["automatic_eligible"] != false || v["freeze_reason"] == "" || v["can_unfreeze"] != true {
		t.Fatalf("未确认没有冻结/原因/解除入口: %v", v)
	}
	if err := f.h.Restart(); err != nil {
		t.Fatal(err)
	}
	f.c = f.h.Client()
	login(f.c)
	if f.c.Do("GET", f.path, nil, 200)["frozen"] != true {
		t.Fatal("面板重建丢失冻结")
	}
	f.c.Do("POST", f.path+"/actions", nil, 409)
	f.c.Do("POST", f.path+"/unfreeze", map[string]any{"action_id": "stale"}, 409)
	f.c.Do("POST", f.path+"/unfreeze", map[string]any{"action_id": a["id"]}, 202)
	// 离线时只排队，节点持久化解除后才释放互斥。
	for range 5 {
		f.h.Clock.Advance(time.Second)
		time.Sleep(20 * time.Millisecond)
	}
	released := awaitRotation(t, f.c, f.path, "released")
	if released["id"] != a["id"] || released["attempts"] != float64(5) || released["new_ip"] != "" {
		t.Fatalf("解除改写历史事实: %v", released)
	}
	v = f.c.Do("GET", f.path, nil, 200)
	if v["frozen"] != false || v["automatic_eligible"] != true {
		t.Fatalf("解除后未恢复资格: %v", v)
	}
	if f.calls.Load() != 1 {
		t.Fatal("解除调用了商家")
	}
	f.n.Restart()
	for range 5 {
		f.h.Clock.Advance(time.Second)
		time.Sleep(20 * time.Millisecond)
	}
	awaitNode(t, f.c, f.id, connected)
	f.c.Do("POST", f.path+"/actions", nil, 202)
	next := awaitRotation(t, f.c, f.path, "observing")
	if next["id"] == a["id"] || f.calls.Load() != 2 {
		t.Fatal("解除后新动作未执行")
	}
}

func TestRotationReconcileReadsIPWithoutCallingVendor(t *testing.T) {
	f := newRotationFixture(t, false)
	f.c.Do("POST", f.path+"/actions", nil, 202)
	a := awaitRotation(t, f.c, f.path, "observing")
	f.finishObservation(t)
	f.c.Do("POST", f.path+"/reconcile", map[string]any{"action_id": a["id"]}, 202)
	awaitRotationView(t, f, func(v map[string]any) bool { return v["pending_operation"] == "" })
	if f.c.Do("GET", f.path, nil, 200)["frozen"] != true {
		t.Fatal("仍见旧 IP 就解除了冻结")
	}
	f.ip.Store("8.8.8.8")
	f.c.Do("POST", f.path+"/reconcile", map[string]any{"action_id": a["id"]}, 202)
	changed := awaitRotation(t, f.c, f.path, "changed")
	if changed["id"] != a["id"] || changed["new_ip"] != "8.8.8.8" || f.calls.Load() != 1 {
		t.Fatalf("核对重调接口或丢失动作: %v 调用%d", changed, f.calls.Load())
	}
	if v := f.c.Do("GET", f.path, nil, 200); v["frozen"] != false || v["automatic_eligible"] != true {
		t.Fatalf("核对清楚没有解冻: %v", v)
	}
	if nodeRow(t, f.c, f.id)["public_ipv4"] != "8.8.8.8" {
		t.Fatal("核对结果未更新节点 IP")
	}
}

func TestRotationQueryUncertaintyFreezesDuringObservation(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(map[bool]string{false: "双源不一致", true: "查询失败"}[failure], func(t *testing.T) {
			f := newRotationFixture(t, false)
			f.c.Do("POST", f.path+"/actions", nil, 202)
			awaitRotation(t, f.c, f.path, "observing")
			if failure {
				f.queryFails.Store(true)
			} else {
				f.second.Store("8.8.8.8")
			}
			f.h.Clock.Advance(time.Minute)
			a := awaitRotation(t, f.c, f.path, "unconfirmed")
			v := f.c.Do("GET", f.path, nil, 200)
			if v["frozen"] != true || v["automatic_eligible"] != false || v["can_unfreeze"] != false || strings.Contains(a["reason"].(string), "凭据") {
				t.Fatalf("观察不确定时没有安全冻结: %v", v)
			}
			f.c.Do("POST", f.path+"/actions", nil, 409)
			// 后续达成双源共识就核对清楚；不再调用商家。
			f.queryFails.Store(false)
			f.second.Store("")
			f.ip.Store("8.8.8.8")
			f.h.Clock.Advance(time.Minute)
			awaitRotation(t, f.c, f.path, "changed")
			v = f.c.Do("GET", f.path, nil, 200)
			if f.calls.Load() != 1 || v["frozen"] != false || v["automatic_eligible"] != true {
				t.Fatal("观察恢复未正确收敛")
			}
		})
	}
}

func TestRotationHTTPTimeoutResumesSameActionAfterRestart(t *testing.T) {
	for _, tc := range []struct {
		name                string
		restart, duringCall bool
	}{{"超时后重建", true, false}, {"调用中重建", true, true}, {"超时后断链", false, false}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRotationFixture(t, true)
			f.c.Do("POST", f.path+"/actions", nil, 202)
			a := awaitRotation(t, f.c, f.path, "started")
			deadline := time.Now().Add(5 * time.Second)
			for f.calls.Load() == 0 && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			if f.calls.Load() != 1 {
				t.Fatal("假商家未收到调用")
			}
			if !tc.duringCall {
				f.h.Clock.Advance(time.Minute)
				awaitRotation(t, f.c, f.path, "unconfirmed")
				if v := f.c.Do("GET", f.path, nil, 200); v["frozen"] != true || v["can_unfreeze"] != false {
					t.Fatalf("超时没有立即冻结并保持观察: %v", v)
				}
				f.c.Do("POST", f.path+"/actions", nil, 409)
			}
			f.n.Disconnect()
			awaitNode(t, f.c, f.id, func(v map[string]any) bool { return v["connected"] == false })
			if tc.restart {
				f.n.Restart()
			}
			f.ip.Store("8.8.8.8")
			if !tc.restart {
				f.h.Clock.Advance(time.Minute)
			}
			f.n.Restore()
			for range 5 {
				f.h.Clock.Advance(time.Second)
				time.Sleep(20 * time.Millisecond)
			}
			awaitNode(t, f.c, f.id, connected)
			f.h.Clock.Advance(time.Minute)
			changed := awaitRotation(t, f.c, f.path, "changed")
			if changed["id"] != a["id"] || f.calls.Load() != 1 {
				t.Fatalf("重建没有恢复同一动作: %v 调用%d", changed, f.calls.Load())
			}
		})
	}
}

func TestRotationReleaseConfirmationSurvivesBothCoresRestart(t *testing.T) {
	panelFaults := &clusterharness.PersistenceFaults{}
	h := clusterharness.New(t, func(d *panel.Dependencies) {
		d.Passwords = nodePasswords{}
		panelFaults.Base = d.Persistence
		d.Persistence = panelFaults
	})
	c := h.Client()
	setup(c)
	login(c)
	nodeFaults := &clusterharness.RotationPersistenceFaults{}
	f := rotationFixtureAt(t, h, c, false, func(_ *node.Config, d *node.Dependencies) {
		nodeFaults.Base = d.Store.(node.RotationStore)
		d.RotationStore = nodeFaults
	})
	c.Do("POST", f.path+"/actions", nil, 202)
	a := awaitRotation(t, c, f.path, "observing")
	f.finishObservation(t)
	nodeFaults.FailState("released")
	c.Do("POST", f.path+"/unfreeze", map[string]any{"action_id": a["id"]}, 202)
	if err := panelFaults.FailRotationActions("UPDATE"); err != nil {
		t.Fatal(err)
	}
	nodeFaults.FailState("")
	// 节点成功落盘后上报触发面板写失败，控制流断开，但面板仍保留 pending。
	for range 5 {
		h.Clock.Advance(time.Second)
		time.Sleep(20 * time.Millisecond)
	}
	awaitNode(t, c, f.id, func(v map[string]any) bool { return v["connected"] == false })
	if v := c.Do("GET", f.path, nil, 200); v["frozen"] != true || v["pending_operation"] != "release" {
		t.Fatalf("未收确认就释放互斥: %v", v)
	}
	f.n.Disconnect()
	f.n.Restart()
	if err := h.Restart(); err != nil {
		t.Fatal(err)
	}
	f.c = h.Client()
	login(f.c)
	f.n.Restore()
	for range 5 {
		h.Clock.Advance(time.Second)
		time.Sleep(20 * time.Millisecond)
	}
	released := awaitRotation(t, f.c, f.path, "released")
	if released["id"] != a["id"] || f.calls.Load() != 1 {
		t.Fatalf("解除补报丢失或重调商家: %v", released)
	}
	awaitNode(t, f.c, f.id, connected)
	f.c.Do("POST", f.path+"/actions", nil, 202)
	awaitRotation(t, f.c, f.path, "observing")
	if f.calls.Load() != 2 {
		t.Fatal("解除补报后新动作未恢复")
	}
}

func TestRotationManagementRequiresAdministratorAndSameOrigin(t *testing.T) {
	h, c := regionBrowser(t)
	created := addNode(c, "管理门禁")
	path := "/api/nodes/" + created["id"].(string) + "/ip-rotation"
	for _, operation := range []string{"unfreeze", "reconcile"} {
		h.Client().Do("POST", path+"/"+operation, map[string]any{"action_id": "a"}, 401)
		response := c.Request("POST", path+"/"+operation, map[string]any{"action_id": "a"}, map[string]string{"Origin": "https://other.example"})
		response.Body.Close()
		if response.StatusCode != 403 {
			t.Fatalf("跨源管理未被拒绝: %d", response.StatusCode)
		}
		c.Do("POST", path+"/"+operation, map[string]any{"action_id": "a", "unknown": true}, 400)
	}
}

func TestRotationStartedPersistenceFailureNeverCallsVendor(t *testing.T) {
	faults := &clusterharness.RotationPersistenceFaults{}
	faults.FailState("started")
	f := newRotationFixture(t, false, func(_ *node.Config, d *node.Dependencies) {
		faults.Base = d.Store.(node.RotationStore)
		d.RotationStore = faults
	})
	f.c.Do("POST", f.path+"/actions", nil, 202)
	a := awaitRotation(t, f.c, f.path, "cancelled")
	if f.calls.Load() != 0 || !strings.Contains(a["reason"].(string), "持久化失败") {
		t.Fatalf("开始落盘失败仍调用或缺少说明: %v 调用%d", a, f.calls.Load())
	}
	f.n.Restart()
	for range 5 {
		f.h.Clock.Advance(time.Second)
		time.Sleep(20 * time.Millisecond)
	}
	awaitNode(t, f.c, f.id, connected)
	if f.calls.Load() != 0 {
		t.Fatal("重启执行了落盘失败的动作")
	}
}

// 同一独立命令经真实 gRPC 重复发送，商家只能收到一次调用。
func TestRotationRepeatedPermitCallsVendorOnce(t *testing.T) {
	f := newRotationFixture(t, false)
	f.h.RepeatRotationCommands(true)
	f.c.Do("POST", f.path+"/actions", nil, 202)
	a := awaitRotation(t, f.c, f.path, "observing")
	f.finishObservation(t)
	f.c.Do("POST", f.path+"/unfreeze", map[string]any{"action_id": a["id"]}, 202)
	awaitRotation(t, f.c, f.path, "released")
	if f.calls.Load() != 1 {
		t.Fatalf("重复许可或管理命令再次调用商家: %d", f.calls.Load())
	}
}

func TestRotationPanelWriteFailuresDoNotAuthorizeOrUnfreeze(t *testing.T) {
	faults := &clusterharness.PersistenceFaults{}
	h := clusterharness.New(t, func(d *panel.Dependencies) {
		d.Passwords = nodePasswords{}
		faults.Base = d.Persistence
		d.Persistence = faults
	})
	c := h.Client()
	setup(c)
	login(c)
	f := rotationFixtureAt(t, h, c, false)
	if err := faults.FailRotationActions("INSERT"); err != nil {
		t.Fatal(err)
	}
	c.Do("POST", f.path+"/actions", nil, 503)
	if v := c.Do("GET", f.path, nil, 200); v["action"] != nil || f.calls.Load() != 0 {
		t.Fatalf("未保存许可却调用商家: %v", v)
	}
	if err := faults.FailRotationActions(""); err != nil {
		t.Fatal(err)
	}
	c.Do("POST", f.path+"/actions", nil, 202)
	a := awaitRotation(t, c, f.path, "observing")
	f.finishObservation(t)
	if err := faults.FailRotationActions("UPDATE"); err != nil {
		t.Fatal(err)
	}
	c.Do("POST", f.path+"/unfreeze", map[string]any{"action_id": a["id"]}, 503)
	if v := c.Do("GET", f.path, nil, 200); v["frozen"] != true || v["pending_operation"] != "" || f.calls.Load() != 1 {
		t.Fatalf("解除保存失败仍生效: %v", v)
	}
	if err := faults.FailRotationActions(""); err != nil {
		t.Fatal(err)
	}
}

func TestRotationReleaseWaitsForNodePersistence(t *testing.T) {
	faults := &clusterharness.RotationPersistenceFaults{}
	f := newRotationFixture(t, false, func(_ *node.Config, d *node.Dependencies) {
		faults.Base = d.Store.(node.RotationStore)
		d.RotationStore = faults
	})
	f.c.Do("POST", f.path+"/actions", nil, 202)
	a := awaitRotation(t, f.c, f.path, "observing")
	f.finishObservation(t)
	faults.FailState("released")
	f.c.Do("POST", f.path+"/unfreeze", map[string]any{"action_id": a["id"]}, 202)
	for range 3 {
		f.h.Clock.Advance(time.Second)
		time.Sleep(20 * time.Millisecond)
	}
	if v := f.c.Do("GET", f.path, nil, 200); v["frozen"] != true || v["automatic_eligible"] != false || v["pending_operation"] != "release" {
		t.Fatalf("节点未落盘就解除冻结: %v", v)
	}
	f.c.Do("POST", f.path+"/actions", nil, 409)
	f.n.Restart()
	faults.FailState("")
	for range 5 {
		f.h.Clock.Advance(time.Second)
		time.Sleep(20 * time.Millisecond)
	}
	awaitRotation(t, f.c, f.path, "released")
	if f.calls.Load() != 1 {
		t.Fatal("解除写失败/重启重调了商家")
	}
}
