package main

import (
	"flag"
	"testing"
	"time"
)

func TestDirectResourceFlagsConfigureBudgetsAndRejectInvalidSettings(t *testing.T) {
	fs := flag.NewFlagSet("node", flag.ContinueOnError)
	l := directResourceFlags(fs)
	if err := fs.Parse([]string{"-direct-max-connections=9", "-direct-max-connections-per-rule=4", "-direct-max-pending-dials=2", "-direct-dial-timeout=150ms", "-direct-idle-timeout=3s"}); err != nil {
		t.Fatal(err)
	}
	if l.MaxConnections != 9 || l.MaxConnectionsPerRule != 4 || l.MaxPendingDials != 2 || l.DialTimeout != 150*time.Millisecond || l.IdleTimeout != 3*time.Second {
		t.Fatalf("启动参数未接到资源配置: %+v", l)
	}
	if err := l.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := fs.Parse([]string{"-direct-max-connections=-1"}); err != nil {
		t.Fatal(err)
	}
	if l.Validate() == nil {
		t.Fatal("负数配置未被拒绝")
	}
}
