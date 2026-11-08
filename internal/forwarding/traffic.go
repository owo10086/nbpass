package forwarding

import (
	"net"
	"sync"
	"time"

	"nbpass/internal/controlpb"
)

// 计量只包裹入口业务复制；出口代理、WSS 和 yamux 握手均不经过此处。
type trafficKey struct {
	rule   string
	minute int64
}
type trafficBytes struct{ up, down uint64 }
type trafficMeter struct {
	mu     sync.Mutex
	bytes  map[trafficKey]trafficBytes
	active map[string]uint64
}

func (e *Engine) registerTrafficRule(id string) {
	e.traffic.mu.Lock()
	defer e.traffic.mu.Unlock()
	if e.traffic.active == nil {
		e.traffic.active = map[string]uint64{}
	}
	if _, exists := e.traffic.active[id]; !exists {
		e.traffic.active[id] = 0
	}
}

func (e *Engine) unregisterTrafficRule(id string) {
	e.traffic.mu.Lock()
	defer e.traffic.mu.Unlock()
	delete(e.traffic.active, id)
}

func (e *Engine) meterConnection(id string) func() {
	m := &e.traffic
	m.mu.Lock()
	if m.active == nil {
		m.active = map[string]uint64{}
	}
	m.active[id]++
	m.mu.Unlock()
	return func() { m.mu.Lock(); m.active[id]--; m.mu.Unlock() }
}

type meteredConn struct {
	net.Conn
	record func(int)
}

func (c *meteredConn) Write(b []byte) (int, error) {
	n, err := c.Conn.Write(b)
	if n > 0 {
		c.record(n)
	}
	return n, err
}
func (c *meteredConn) CloseWrite() error {
	if half, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return half.CloseWrite()
	}
	return nil
}
func (e *Engine) meteredConns(id string, client, target net.Conn) (net.Conn, net.Conn) {
	record := func(up bool, n int) {
		m := &e.traffic
		m.mu.Lock()
		defer m.mu.Unlock()
		minute := e.deps.Now().UTC().Truncate(time.Minute).Unix()
		if m.bytes == nil {
			m.bytes = map[trafficKey]trafficBytes{}
		}
		key := trafficKey{id, minute}
		v := m.bytes[key]
		if up {
			v.up += uint64(n)
		} else {
			v.down += uint64(n)
		}
		m.bytes[key] = v
	}
	return &meteredConn{client, func(n int) { record(false, n) }}, &meteredConn{target, func(n int) { record(true, n) }}
}

func (e *Engine) proxyMetered(id string, client, target net.Conn) {
	client, target = e.meteredConns(id, client, target)
	proxyTCP(client, target)
}

// Traffic 返回不可变快照，允许采集器与配置应用/转发并发。
// 分钟桶独立累计，丢弃过期桶不会使新流量重新携带过期增量。
func (e *Engine) Traffic() []*controlpb.RuleTraffic {
	m := &e.traffic
	m.mu.Lock()
	defer m.mu.Unlock()
	now := e.deps.Now().UTC()
	minute := now.Truncate(time.Minute).Unix()
	if m.bytes == nil {
		m.bytes = map[trafficKey]trafficBytes{}
	}
	for id := range m.active {
		key := trafficKey{id, minute}
		if _, ok := m.bytes[key]; !ok {
			m.bytes[key] = trafficBytes{}
		}
	}
	out := make([]*controlpb.RuleTraffic, 0, len(m.bytes))
	for key, v := range m.bytes {
		if time.Unix(key.minute, 0).Before(now.Add(-24 * time.Hour)) {
			delete(m.bytes, key)
			continue
		}
		out = append(out, &controlpb.RuleTraffic{RuleId: key.rule, BucketUnix: key.minute, UploadBytes: v.up, DownloadBytes: v.down, ActiveConnections: m.active[key.rule], SampledAt: now.Format(time.RFC3339Nano)})
	}
	return out
}

// ForgetTraffic 仅释放已成功持久化的闭合桶；当前桶与并发新增字节必须保留。
func (e *Engine) ForgetTraffic(samples []*controlpb.RuleTraffic) {
	m := &e.traffic
	m.mu.Lock()
	defer m.mu.Unlock()
	minute := e.deps.Now().UTC().Truncate(time.Minute).Unix()
	for _, s := range samples {
		key := trafficKey{s.RuleId, s.BucketUnix}
		if v, ok := m.bytes[key]; ok && s.BucketUnix < minute && v.up == s.UploadBytes && v.down == s.DownloadBytes {
			delete(m.bytes, key)
		}
	}
}
