package clusterharness_test

import (
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"nbpass/internal/clusterharness"
	"nbpass/internal/controlpb"
	"nbpass/internal/node"
	"nbpass/internal/panel"
)

// A control outage must retain the installed version and a durable, unresolved task.
func TestUpgradeControlOutageIsUnconfirmedAndReconcilesAfterPanelRestart(t *testing.T) {
	h := clusterharness.New(t, func(d *panel.Dependencies) {
		d.Passwords = nodePasswords{}
		d.NodeBinary = func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("embedded-node-binary")), nil }
	})
	c := h.Client()
	setup(c)
	login(c)
	created := addNode(c, "控制确认超时")
	id := created["id"].(string)
	var n *clusterharness.Node
	switched := make(chan struct{})
	n = h.StartNode(created["installation_ticket"].(string), func(_ *node.Config, d *node.Dependencies) {
		d.SoftwareVersion = "0.0.9"
		d.Updater = h.UpdateService(d.Store, clusterharness.UpdateEnvironment{Switch: func() {
			n.Disconnect()
			n.RestartImage(node.SoftwareVersion, []byte("embedded-node-binary"))
			close(switched)
		}})
	})
	awaitNode(t, c, id, connected)
	task := c.Do("POST", "/api/nodes/"+id+"/upgrade", nil, 202)["task"].(map[string]any)
	select {
	case <-switched:
	case <-time.After(5 * time.Second):
		t.Fatal("程序未切换")
	}
	h.Clock.Advance(3 * time.Minute)
	p := awaitUpgrade(t, c, id, "unconfirmed")
	if p["manifest"].(map[string]any)["task_id"] != task["manifest"].(map[string]any)["task_id"] || nodeRow(t, c, id)["status"] != "maintenance" {
		t.Fatal("确认超时丢失任务或提前结束维护")
	}
	if err := h.Restart(); err != nil {
		t.Fatal(err)
	}
	c = h.Client()
	login(c)
	awaitUpgrade(t, c, id, "unconfirmed")
	n.Restore()
	p = driveUpgrade(t, h, c, id, "ready")
	if nodeRow(t, c, id)["software_version"] != node.SoftwareVersion || strings.Contains(strings.Join(anyStrings(p["steps"]), ","), "rolled_back") {
		t.Fatal("仅控制失联触发回滚")
	}
}

func TestUpgradeRequiresMatchingRunningArtifactEvidence(t *testing.T) {
	for _, field := range []string{"instance", "version", "platform", "sha256", "size", "ready", "missing"} {
		t.Run(field, func(t *testing.T) {
			h, c, _, id := upgradeRig(t, clusterharness.UpdateEnvironment{})
			h.TransformControlReports(func(report *controlpb.NodeReport) {
				if !report.UpgradeOnly {
					return
				}
				var p map[string]any
				if json.Unmarshal(report.UpgradeProgress, &p) != nil {
					return
				}
				m := p["manifest"].(map[string]any)
				v := map[string]any{"instance": report.ProcessInstance, "version": m["version"], "platform": m["platform"], "sha256": m["sha256"], "size": m["size"], "ready": true}
				switch field {
				case "ready":
					v[field] = false
				case "size":
					v[field] = 1
				default:
					v[field] = "wrong"
				}
				p["verification"] = v
				if field == "missing" {
					delete(p, "verification")
				}
				report.UpgradeProgress, _ = json.Marshal(p)
			})
			c.Do("POST", "/api/nodes/"+id+"/upgrade", nil, 202)
			driveUpgrade(t, h, c, id, "reconnecting")
			for range 15 {
				h.Clock.Advance(time.Second)
				awaitControlIdle(t, h)
				time.Sleep(2 * time.Millisecond)
			}
			p := c.Do("GET", "/api/nodes/"+id+"/upgrade", nil, 200)["upgrade"].(map[string]any)
			if p["state"] == "ready" {
				t.Fatal("运行产物证据不符仍确认升级成功")
			}
		})
	}
}

func TestUpgradeRechecksReadinessWithoutNewUpdaterProgress(t *testing.T) {
	h, c, _, id := upgradeRig(t, clusterharness.UpdateEnvironment{})
	h.TransformControlReports(func(r *controlpb.NodeReport) {
		if !r.UpgradeOnly {
			return
		}
		var p map[string]any
		if json.Unmarshal(r.UpgradeProgress, &p) != nil {
			return
		}
		if v, ok := p["verification"].(map[string]any); ok {
			v["ready"] = false
		}
		r.UpgradeProgress, _ = json.Marshal(p)
	})
	c.Do("POST", "/api/nodes/"+id+"/upgrade", nil, 202)
	driveUpgrade(t, h, c, id, "reconnecting")
	h.TransformControlReports(nil)
	// The updater sequence has stopped changing. A fresh independent verification must
	// still be sent when business readiness recovers on this same control connection.
	driveUpgrade(t, h, c, id, "ready")
}

func anyStrings(v any) []string {
	var result []string
	for _, s := range v.([]any) {
		result = append(result, s.(string))
	}
	return result
}
