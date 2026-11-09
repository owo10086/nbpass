package main

import (
	"flag"
	"testing"
	"time"
)

func TestTunnelResourceFlagsAndValidation(t *testing.T) {
	fs := flag.NewFlagSet("node", flag.ContinueOnError)
	l := directResourceFlags(fs)
	if err := fs.Parse([]string{"-max-connections=9", "-max-connections-per-rule=4", "-max-pending-dials=2", "-dial-timeout=150ms", "-idle-timeout=3s", "-tunnel-max-streams=7", "-tunnel-handshake-timeout=200ms"}); err != nil {
		t.Fatal(err)
	}
	if l.MaxConnections != 9 || l.MaxConnectionsPerRule != 4 || l.MaxPendingDials != 2 || l.DialTimeout != 150*time.Millisecond || l.IdleTimeout != 3*time.Second || l.MaxTunnelStreams != 7 || l.TunnelHandshakeTimeout != 200*time.Millisecond {
		t.Fatalf("隧道/共享预算参数未生效: %+v", l)
	}
	for _, args := range [][]string{{"-tunnel-max-streams=-1"}, {"-tunnel-handshake-timeout=-1s"}, {"-tunnel-max-streams=4294967296"}} {
		fs := flag.NewFlagSet("node", flag.ContinueOnError)
		l := directResourceFlags(fs)
		if err := fs.Parse(args); err != nil {
			t.Fatal(err)
		}
		if l.Validate() == nil {
			t.Fatalf("接受无效参数: %v", args)
		}
	}
}
