package clusterharness_test

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"nbpass/internal/controlpb"
	"nbpass/internal/node"
	"nbpass/internal/panel"
)

// 仅在真实检测点的拨号边界控制各目标完成时间，不合并控制报告。
func allOfflineChecker(gates map[string]<-chan struct{}) func(*node.Config, *node.Dependencies) {
	return func(_ *node.Config, d *node.Dependencies) {
		d.CheckerDial = func(ctx context.Context, address string) (net.Conn, error) {
			if address == "203.0.113.9:443" {
				return checkerPipe(), nil
			}
			if gate := gates[address]; gate != nil {
				select {
				case <-gate:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			return nil, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("目标不可达")}
		}
	}
}

func (f *allPolicyRig) offlineCheck(t *testing.T, index int, outcome string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var checks []any
	for time.Now().Before(deadline) {
		checks = f.c.Do("GET", "/api/entry-groups/"+f.g+"/offline-checks", nil, 200)["checks"].([]any)
		for _, raw := range checks {
			c := raw.(map[string]any)
			if c["node_id"] == f.ids[index] && c["outcome"] == outcome {
				return c
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("成员 %d 的离线确认未达到 %s：%v", index, outcome, checks)
	return nil
}

func (f *allPolicyRig) assertOriginalOfflineCheck(t *testing.T, index int, original map[string]any, outcome string) {
	t.Helper()
	checks := f.c.Do("GET", "/api/entry-groups/"+f.g+"/offline-checks", nil, 200)["checks"].([]any)
	count := 0
	for _, raw := range checks {
		c := raw.(map[string]any)
		if c["node_id"] != f.ids[index] {
			continue
		}
		count++
		if c["id"] != original["id"] || c["outcome"] != outcome {
			t.Fatalf("其他成员动作令离线确认失效或重新排期：原=%v，现=%v", original, c)
		}
	}
	if count != 1 {
		t.Fatalf("成员 %d 应仅检测一次，实际 %d 次：%v", index, count, checks)
	}
}

// 组版本失效会令第二、第三个成员在第一轮截止前重新排期，甚至持续饥饿。
func TestAllPublishStaggeredOfflineChecksDoNotRestartAfterWithdrawal(t *testing.T) {
	f := newAllPolicyRig(t, 3)
	f.publish(t)
	gates := []chan struct{}{make(chan struct{}), make(chan struct{}), make(chan struct{})}
	var once [3]sync.Once
	for i := range gates {
		defer once[i].Do(func() { close(gates[i]) })
	}
	startChecker(t, f.h, f.c, 0, 1, nil, allOfflineChecker(map[string]<-chan struct{}{
		"8.8.8.8:18080": gates[0], "1.1.1.1:18080": gates[1], "2.2.2.2:18080": gates[2],
	}))
	for i := range 3 {
		f.nodes[i].Disconnect()
		awaitNode(t, f.c, f.ids[i], func(n map[string]any) bool { return n["connected"] == false })
		awaitControlIdle(t, f.h)
		f.h.Clock.Advance(time.Second)
	}
	f.samples(t, 9)
	checks := []map[string]any{f.offlineCheck(t, 0, "pending"), f.offlineCheck(t, 1, "pending"), f.offlineCheck(t, 2, "pending")}
	deadline, err := time.Parse(time.RFC3339Nano, checks[0]["deadline"].(string))
	if err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		once[i].Do(func() { close(gates[i]) })
		f.offlineCheck(t, i, "unreachable")
		awaitControlIdle(t, f.h)
		f.h.Clock.Advance(time.Second)
		if i < 2 {
			f.awaitEnabled(t, i, false)
			for j := i + 1; j < 3; j++ {
				f.assertOriginalOfflineCheck(t, j, checks[j], "pending")
			}
		} else {
			waitOfflineEvents(t, f.c, "?type=entry_member_kept", 1)
			f.awaitEnabled(t, i, true)
			if !strings.Contains(allPolicyMember(t, f.c, f.g, f.ids[i])["reason"].(string), "最少保留数") {
				t.Fatal("最后一条记录未按最少保留数保留")
			}
		}
		f.assertOriginalOfflineCheck(t, i, checks[i], "unreachable")
	}
	if f.h.Clock.Now().After(deadline.Add(5 * time.Second)) {
		t.Fatal("错峰摘除超过首轮检测截止周期加五秒余量")
	}
}

// 手动摘除另一成员不能取消已经在途的检测任务。
func TestAllPublishPendingOfflineCheckSurvivesOtherMemberAction(t *testing.T) {
	for _, change := range []string{"手动摘除", "地址同步"} {
		t.Run(change, func(t *testing.T) {
			f := newAllPolicyRig(t, 3)
			f.publish(t)
			gate := make(chan struct{})
			var once sync.Once
			defer once.Do(func() { close(gate) })
			startChecker(t, f.h, f.c, 0, 1, gate)
			f.disconnect(t, 0)
			check := f.offlineCheck(t, 0, "pending")
			if change == "手动摘除" {
				f.action(1, "withdraw", 202)
			} else {
				f.c.Do("POST", "/api/nodes/"+f.ids[1]+"/sync-address", map[string]any{"ipv4": "1.1.1.1", "confirm_inbound": true}, 200)
			}
			awaitDNSStage(t, f.c, f.g, panel.EntryWritten)
			f.assertOriginalOfflineCheck(t, 0, check, "pending")
			once.Do(func() { close(gate) })
			f.offlineCheck(t, 0, "unreachable")
			awaitControlIdle(t, f.h)
			f.h.Clock.Advance(time.Second)
			f.awaitEnabled(t, 0, false)
			f.awaitEnabled(t, 2, true)
			f.assertOriginalOfflineCheck(t, 0, check, "unreachable")
			deadline, err := time.Parse(time.RFC3339Nano, check["deadline"].(string))
			if err != nil || f.h.Clock.Now().After(deadline.Add(5*time.Second)) {
				t.Fatal("其他成员动作令摘除超过首轮截止周期", err)
			}
		})
	}
}

// 专门验证同批语义：同一接收事务中确认的三个成员须按顺序、同一动作摘除。
func TestAllPublishSameBatchWithdrawalKeepsMemberOrder(t *testing.T) {
	f := newAllPolicyRig(t, 3)
	f.publish(t)
	var reports [][]byte
	batched := make(chan struct{})
	f.h.TransformControlReports(func(report *controlpb.NodeReport) {
		if !report.CheckerOnly || len(report.CheckerResults) == 0 {
			return
		}
		reports = append(reports, report.CheckerResults...)
		report.CheckerResults = nil
		if len(reports) == 3 {
			report.CheckerResults, reports = reports, nil
			close(batched)
		}
	})
	startChecker(t, f.h, f.c, 0, 1, nil, allOfflineChecker(nil))
	f.disconnect(t, 0, 1, 2)
	waitSignal(t, batched)
	awaitControlIdle(t, f.h)
	f.h.TransformControlReports(nil)
	f.h.Clock.Advance(time.Second)
	f.awaitEnabled(t, 1, false)
	f.awaitEnabled(t, 2, false)
	f.awaitEnabled(t, 0, true)
	a := publication(f.c, f.g)["action"].(map[string]any)
	if len(a["records"].([]any)) != 2 {
		t.Fatalf("未在一个动作内决定两个摘除：%v", a)
	}
	waitOfflineEvents(t, f.c, "?type=entry_member_kept", 1)
}

// 去掉组版本约束后，当前成员自身的配置、恢复、维护和绑定变化仍须拒绝迟到证据。
func TestAllPublishPendingOfflineCheckRejectsChangedMember(t *testing.T) {
	for _, change := range []string{"检测端口", "恢复上线", "维护", "重新绑定"} {
		t.Run(change, func(t *testing.T) {
			f := newAllPolicyRig(t, 3)
			f.publish(t)
			gate := make(chan struct{})
			var once sync.Once
			defer once.Do(func() { close(gate) })
			startChecker(t, f.h, f.c, 0, 1, gate)
			f.disconnect(t, 0)
			check := f.offlineCheck(t, 0, "pending")
			switch change {
			case "检测端口":
				f.c.Do("PUT", "/api/nodes/"+f.ids[0]+"/listener-ports", map[string]int{"tunnel_listen_port": 18443, "check_port": 18081}, 200)
			case "恢复上线":
				f.nodes[0].Restore()
				f.nodes[0].Restart()
				awaitNode(t, f.c, f.ids[0], connected)
			case "维护":
				f.c.Do("PUT", "/api/nodes/"+f.ids[0]+"/maintenance", map[string]bool{"enabled": true}, 204)
			case "重新绑定":
				bindMember(f.c, f.g, f.ids[0], f.account, "100", 200)
				awaitDNSStage(t, f.c, f.g, panel.EntryWritten)
			}
			if invalid := f.offlineCheck(t, 0, "invalidated"); invalid["id"] != check["id"] {
				t.Fatalf("失效的不是原检测：%v", invalid)
			}
			writes := f.dns.Writes()
			once.Do(func() { close(gate) })
			f.samples(t, 2)
			f.awaitEnabled(t, 0, change != "重新绑定")
			if f.dns.Writes() != writes {
				t.Fatal("成员变化后迟到的旧证据仍触发 DNS 写入")
			}
		})
	}
}
