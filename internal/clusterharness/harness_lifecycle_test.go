package clusterharness

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"nbpass/internal/controlpb"
)

func TestRestoreWaitsForInFlightControlHandler(t *testing.T) {
	h := New(t)
	snapshot := filepath.Join(h.Dir, "before-restore.db")
	if err := h.Snapshot(snapshot); err != nil {
		t.Fatal(err)
	}
	entered, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	// Hold a real RPC through cancellation, as an in-flight database operation
	// can outlive transport shutdown. The database must not be replaced yet.
	h.controlServer.Load().RegisterService(&grpc.ServiceDesc{
		ServiceName: "test.RestoreBarrier",
		Methods: []grpc.MethodDesc{{MethodName: "Hold", Handler: func(_ any, ctx context.Context, _ func(any) error, _ grpc.UnaryServerInterceptor) (any, error) {
			close(entered)
			<-ctx.Done()
			close(canceled)
			<-release
			return nil, ctx.Err()
		}}},
	}, nil)
	roots := x509.NewCertPool()
	roots.AddCert(h.Control.Certificate())
	conn, err := grpc.NewClient(h.Control.Listener.Addr().String(), grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12})))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go conn.Invoke(ctx, "/test.RestoreBarrier/Hold", &controlpb.NodeReport{}, &controlpb.PanelMessage{})
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("控制请求未进入处理器")
	}
	restored := make(chan error, 1)
	restoreDone := make(chan struct{})
	go func() { defer close(restoreDone); restored <- h.RestoreSnapshot(snapshot) }()
	defer func() { unblock(); <-restoreDone }()
	select {
	case <-canceled:
	case <-ctx.Done():
		t.Fatal("恢复未关闭旧控制连接")
	}
	select {
	case err := <-restored:
		t.Fatalf("旧控制处理器退出前替换了数据库: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	unblock()
	select {
	case err := <-restored:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("控制处理器退出后恢复未完成")
	}
}
