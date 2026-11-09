package clusterharness_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"nbpass/internal/clusterharness"
	"nbpass/internal/node"
)

// 只替换网络故障与目标地址；面板、节点、TLS/WSS/yamux 与业务 TCP 都是真实代码。
type exitLink struct {
	mu          sync.Mutex
	blocked     bool
	connections []net.Conn
}

func (l *exitLink) dial(ctx context.Context, address string) (net.Conn, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.blocked {
		return nil, errors.New("模拟入口到出口链路断开")
	}
	c, err := (&net.Dialer{}).DialContext(ctx, "tcp4", address)
	if err == nil {
		l.connections = append(l.connections, c)
	}
	return c, err
}
func (l *exitLink) block(closeExisting bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.blocked = true
	if closeExisting {
		for _, c := range l.connections {
			c.Close()
		}
	}
}
func (l *exitLink) recover() { l.mu.Lock(); l.blocked = false; l.mu.Unlock() }

type failoverRig struct {
	h            *clusterharness.Harness
	c            *clusterharness.Client
	entryID      string
	entry        *clusterharness.Node
	exits        []*clusterharness.Node
	exitIDs      []string
	links        []*exitLink
	targetFailed []atomic.Bool
	reservePort  func() int
	port         int
	body         map[string]any
}

func setupFailover(t *testing.T, count int, options ...func(int, *node.Dependencies)) *failoverRig {
	t.Helper()
	h, c := nodeBrowser(t)
	reserve, listen := directPorts(t)
	r := &failoverRig{h: h, c: c, reservePort: reserve, targetFailed: make([]atomic.Bool, count)}
	ports := map[string]*exitLink{}
	for i := 0; i < count; i++ {
		row := addNode(c, fmt.Sprintf("出口%d", i))
		id := row["id"].(string)
		target := echoTarget(t, fmt.Sprintf("%d:", i))
		n := h.StartNode(row["installation_ticket"].(string), func(config *node.Config, d *node.Dependencies) {
			listen(config, d)
			d.Forwarding.DialTarget = func(ctx context.Context, _ string) (net.Conn, error) {
				if r.targetFailed[i].Load() {
					return nil, errors.New("目标不可达")
				}
				return (&net.Dialer{}).DialContext(ctx, "tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(target)))
			}
			for _, option := range options {
				option(i, d)
			}
		})
		awaitNode(t, c, id, connected)
		p := reserve()
		link := &exitLink{}
		ports[net.JoinHostPort("127.0.0.1", strconv.Itoa(p))] = link
		r.links = append(r.links, link)
		r.exits = append(r.exits, n)
		r.exitIDs = append(r.exitIDs, id)
		c.Do("PUT", "/api/nodes/"+id+"/tunnel", map[string]any{"address": "127.0.0.1", "listen_port": p, "transport": "wss"}, 200)
	}
	row := addNode(c, "入口")
	r.entryID = row["id"].(string)
	r.entry = h.StartNode(row["installation_ticket"].(string), func(config *node.Config, d *node.Dependencies) {
		listen(config, d)
		d.Forwarding.DialTunnel = func(ctx context.Context, address string) (net.Conn, error) { return ports[address].dial(ctx, address) }
		d.Forwarding.DialTarget = func(context.Context, string) (net.Conn, error) { return nil, errors.New("禁止入口直连") }
	})
	awaitNode(t, c, r.entryID, connected)
	g := c.Do("POST", "/api/exit-groups", map[string]any{"name": "主备", "members": r.exitIDs, "strategy": "primary_backup"}, 201)
	listed := c.Do("GET", "/api/exit-groups", nil, 200)["exit_groups"].([]any)
	if len(listed) != 1 || len(listed[0].(map[string]any)["members"].([]any)) != count {
		t.Fatalf("出口顺序未保存: %v", listed)
	}
	r.port = reserve()
	r.body = directBody(entryGroup(c, r.entryID)["id"].(string), r.port, "127.0.0.1", 12345)
	r.body["kind"], r.body["exit_group_id"] = "tunnel", g["id"]
	created := c.Do("POST", "/api/rules", r.body, 201)
	r.body["id"] = created["id"]
	for _, id := range r.exitIDs {
		awaitRevision(t, c, id, 0)
	}
	awaitRevision(t, c, r.entryID, 0)
	return r
}

func TestExitCandidatesAreConfirmedPerRuleAndUpdateWithoutDisconnect(t *testing.T) {
	store := &faultyConfigStore{}
	r := setupFailover(t, 2, func(i int, d *node.Dependencies) {
		if i == 0 {
			store.ConfigStore = d.Store.(node.ConfigStore)
			d.ConfigStore = store
		}
	})
	old := tcpExchange(t, r.port, "0:")
	store.fail.Store(true)
	backupRev := nodeRow(t, r.c, r.exitIDs[1])["applied_revision"].(float64)
	entryRev := nodeRow(t, r.c, r.entryID)["applied_revision"].(float64)
	body := map[string]any{}
	for k, v := range r.body {
		if k != "id" {
			body[k] = v
		}
	}
	port := r.reservePort()
	body["listen_port"] = port
	r.c.Do("POST", "/api/rules", body, 201)
	awaitNode(t, r.c, r.exitIDs[0], func(v map[string]any) bool { return v["config_status"] == "同步失败" })
	awaitRevision(t, r.c, r.exitIDs[1], backupRev)
	awaitRevision(t, r.c, r.entryID, entryRev)
	// 同组旧规则的确认不能借给新规则；只有备出口已应用新规则。
	backup := tcpExchange(t, port, "1:")
	tcpExchange(t, r.port, "0:")
	store.fail.Store(false)
	r.h.Clock.Advance(10 * time.Second)
	awaitRevision(t, r.c, r.exitIDs[0], 0)
	awaitRevision(t, r.c, r.entryID, entryRev+1)
	tcpExchange(t, port, "0:")
	for _, pair := range []struct {
		c    net.Conn
		want string
	}{{old, "0:ping"}, {backup, "1:ping"}} {
		pair.c.SetDeadline(time.Now().Add(time.Second))
		pair.c.Write([]byte("ping"))
		b := make([]byte, 6)
		if _, err := io.ReadFull(pair.c, b); err != nil || string(b) != pair.want {
			t.Fatalf("候选确认变动断开已有流: %q %v", b, err)
		}
	}
}

func TestExitAttemptsAtMostThreeDifferentExits(t *testing.T) {
	r := setupFailover(t, 4)
	for i := 0; i < 3; i++ {
		r.targetFailed[i].Store(true)
	}
	// 第四个出口可用，但本条新连接已用完三个不同出口的预算。
	assertTCPFailure(t, r.port)
	r.targetFailed[2].Store(false)
	tcpExchange(t, r.port, "2:")
}

func TestExitConnectionTotalBudgetIsTenSeconds(t *testing.T) {
	r := setupFailover(t, 2, func(_ int, d *node.Dependencies) {
		d.Forwarding.DialTarget = func(ctx context.Context, _ string) (net.Conn, error) { <-ctx.Done(); return nil, ctx.Err() }
		d.Forwarding.LookupIPv4 = func(context.Context, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("127.0.0.1"), netip.MustParseAddr("127.0.0.2"), netip.MustParseAddr("127.0.0.3")}, nil
		}
	})
	oldRev := nodeRow(t, r.c, r.entryID)["applied_revision"].(float64)
	r.body["target_address"] = "budget.invalid"
	r.c.Do("PUT", "/api/rules/"+r.body["id"].(string), r.body, 200)
	for _, id := range r.exitIDs {
		awaitRevision(t, r.c, id, 0)
	}
	awaitRevision(t, r.c, r.entryID, oldRev)
	started := time.Now()
	c, err := net.DialTimeout("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(r.port)), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(12 * time.Second))
	c.Write([]byte("ping"))
	_, err = c.Read(make([]byte, 1))
	if err == nil {
		t.Fatal("超时目标仍成功")
	}
	if elapsed := time.Since(started); elapsed < 9*time.Second || elapsed > 11*time.Second {
		t.Fatalf("总预算不是10秒: %v, %v", elapsed, err)
	}
}

func TestExitRevocationDuringDialDoesNotPoisonSharedExit(t *testing.T) {
	started := make(chan struct{}, 1)
	r := setupFailover(t, 2, func(i int, d *node.Dependencies) {
		original := d.Forwarding.DialTarget
		d.Forwarding.DialTarget = func(ctx context.Context, address string) (net.Conn, error) {
			if strings.HasSuffix(address, ":23456") {
				if i == 0 {
					select {
					case started <- struct{}{}:
					default:
					}
				}
				<-ctx.Done()
				return nil, ctx.Err()
			}
			return original(ctx, address)
		}
	})
	body := map[string]any{}
	for k, v := range r.body {
		if k != "id" {
			body[k] = v
		}
	}
	port := r.reservePort()
	body["listen_port"], body["target_port"] = port, 23456
	oldRev := nodeRow(t, r.c, r.entryID)["applied_revision"].(float64)
	created := r.c.Do("POST", "/api/rules", body, 201)
	for _, id := range r.exitIDs {
		awaitRevision(t, r.c, id, 0)
	}
	awaitRevision(t, r.c, r.entryID, oldRev)
	r.entry.Disconnect()
	c, err := net.DialTimeout("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.Write([]byte("ping"))
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("目标拨号未开始")
	}
	exitRev := nodeRow(t, r.c, r.exitIDs[0])["applied_revision"].(float64)
	r.c.Do("DELETE", "/api/rules/"+created["id"].(string), nil, 204)
	awaitRevision(t, r.c, r.exitIDs[0], exitRev)
	r.h.Clock.Advance(time.Second)
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := c.Read(make([]byte, 1)); err == nil {
		t.Fatal("待拨号流未撤销")
	}
	// 两条规则共享主出口。另一规则不应因逐流撤权被赶到备出口。
	tcpExchange(t, r.port, "0:")
}
func TestExitFailoverImmediateAndOffline(t *testing.T) {
	r := setupFailover(t, 2)
	tcpExchange(t, r.port, "0:")
	r.entry.Disconnect()
	for _, n := range r.exits {
		n.Disconnect()
	}
	r.links[0].block(true)
	// 一次真实建连失败即使用备出口，无需等三次周期探测。
	tcpExchange(t, r.port, "1:")
	r.links[1].block(true)
	assertTCPFailure(t, r.port)
}
func TestExitTargetFailureDoesNotPoisonOtherRules(t *testing.T) {
	r := setupFailover(t, 2)
	r.targetFailed[0].Store(true)
	tcpExchange(t, r.port, "1:")
	r.targetFailed[0].Store(false)
	// 主出口没有因目标不可达而被标故障，下一个新连接仍首先选它。
	tcpExchange(t, r.port, "0:")
}
func TestExitFailoverNeverReplaysBusiness(t *testing.T) {
	r := setupFailover(t, 2)
	first := tcpExchange(t, r.port, "0:")
	r.links[0].block(true)
	first.SetDeadline(time.Now().Add(time.Second))
	first.Write([]byte("more"))
	if _, err := io.ReadFull(first, make([]byte, 6)); err == nil {
		t.Fatal("已发业务在另一个出口重放")
	}
	tcpExchange(t, r.port, "1:")
}

func exitHealthRow(t *testing.T, r *failoverRig, exit string) map[string]any {
	t.Helper()
	rows := r.c.Do("GET", "/api/nodes/"+r.entryID+"/exit-health", nil, 200)["exits"].([]any)
	for _, row := range rows {
		v := row.(map[string]any)
		if v["exit_id"] == exit {
			return v
		}
	}
	return nil
}
func advanceExitProbe(t *testing.T, r *failoverRig, exit string) map[string]any {
	t.Helper()
	before := exitHealthRow(t, r, exit)
	for step := 0; step < 3; step++ {
		r.h.Clock.Advance(5500 * time.Millisecond)
		deadline := time.Now().Add(200 * time.Millisecond)
		for time.Now().Before(deadline) {
			row := exitHealthRow(t, r, exit)
			if row != nil && (before == nil || row["checked_at_unix"] != before["checked_at_unix"]) {
				return row
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	t.Fatal("探测未通过真实节点与HTTP状态更新")
	return nil
}
func TestExitProbeThreeFailuresAndRecoveryPreservesStreams(t *testing.T) {
	r := setupFailover(t, 2)
	first := tcpExchange(t, r.port, "0:")
	r.links[0].block(false)
	var row map[string]any
	for i := 0; i < 6; i++ {
		row = advanceExitProbe(t, r, r.exitIDs[0])
		if row["state"] == "unhealthy" {
			break
		}
	}
	if row["state"] != "unhealthy" {
		t.Fatalf("连续链路探测失败未切换: %v", row)
	}
	tcpExchange(t, r.port, "1:")
	// 只影响新连接，先前经主出口建立的流仍可交换业务。
	first.SetDeadline(time.Now().Add(time.Second))
	first.Write([]byte("ping"))
	b := make([]byte, 6)
	if _, err := io.ReadFull(first, b); err != nil || string(b) != "0:ping" {
		t.Fatalf("切换迁移或断开已有流: %q %v", b, err)
	}
	r.links[0].recover()
	for i := 0; i < 3; i++ {
		row = advanceExitProbe(t, r, r.exitIDs[0])
		if row["state"] == "recovering" {
			break
		}
	}
	if row["state"] != "recovering" {
		t.Fatalf("未开始恢复: %v", row)
	}
	start := row["recovery_since_unix"].(float64)
	for {
		row = advanceExitProbe(t, r, r.exitIDs[0])
		elapsed := float64(r.h.Clock.Now().Unix()) - start
		if elapsed < 60 {
			if row["state"] != "recovering" {
				t.Fatalf("提前恢复: %v", row)
			}
			tcpExchange(t, r.port, "1:")
			continue
		}
		if row["state"] != "healthy" {
			continue
		}
		break
	}
	tcpExchange(t, r.port, "0:")
	r.entry.Disconnect()
	awaitNode(t, r.c, r.entryID, func(v map[string]any) bool { return v["connected"] == false })
	if r.c.Do("GET", "/api/nodes/"+r.entryID+"/exit-health", nil, 200)["fresh"] != false {
		t.Fatal("失联仍显示实时健康")
	}
}

// 票11的规则容量拒绝不能被票12当成链路失败，绕到备出口继续接入。
func TestExitCapacityDoesNotFailOverOrPoisonHealth(t *testing.T) {
	r := setupFailover(t, 2, func(i int, d *node.Dependencies) {
		if i == 0 {
			d.Forwarding.Limits.MaxConnectionsPerRule = 1
		}
	})
	first := tcpExchange(t, r.port, "0:")
	for range 3 {
		assertTCPFailure(t, r.port)
	}
	first.SetDeadline(time.Now().Add(time.Second))
	first.Write([]byte("ping"))
	if _, err := io.ReadFull(first, make([]byte, 6)); err != nil {
		t.Fatalf("资源拒绝影响既有流: %v", err)
	}
	first.(*net.TCPConn).SetLinger(0)
	first.Close()
	f := &tunnelLimitsFixture{h: r.h, c: r.c}
	sampleTunnelBusiness(t, f, r.exitIDs[0], 0)
	tcpExchange(t, r.port, "0:")
}
