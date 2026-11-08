package main

import (
	"context"
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"google.golang.org/grpc"

	"nbpass/internal/nodebundle"
	"nbpass/internal/panel"
	"nbpass/internal/platform"
	"nbpass/web"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	if len(os.Args) > 1 && os.Args[1] == "reset-password" {
		return resetPassword(ctx, os.Args[2:], os.Stdin, os.Stdout)
	}
	httpAddress := flag.String("http-listen", "127.0.0.1:8080", "网页和 API 的 HTTP 监听地址")
	controlAddress := flag.String("control-listen", "127.0.0.1:8081", "gRPC 控制监听地址")
	controlURL := flag.String("control-url", "", "节点访问的 HTTPS 控制地址，默认与 public-origin 相同")
	trustedControl := flag.Bool("trust-control-network", false, "明确确认控制地址位于可信网络，允许非回环监听")
	origin := flag.String("public-origin", "", "浏览器访问面板的 HTTPS 源，例如 https://panel.example.com（必填）")
	dir := flag.String("data-dir", "data", "数据库和本机密钥的专用目录")
	concurrency := flag.Int("password-concurrency", 4, "密码哈希和校验的最大并发")
	flag.Parse()
	host, _, err := net.SplitHostPort(*controlAddress)
	if err != nil {
		return fmt.Errorf("控制监听地址无效: %w", err)
	}
	ip := net.ParseIP(host)
	if (ip == nil || !ip.IsLoopback()) && !*trustedControl {
		return errors.New("控制端口仅可绑定回环地址；可信网络需显式指定 -trust-control-network")
	}
	core, err := panel.Open(ctx, panel.Config{PublicOrigin: *origin, ControlURL: *controlURL, PasswordConcurrency: *concurrency}, panel.Dependencies{
		Clock:       platform.RealClock{},
		Persistence: platform.SQLite{Path: filepath.Join(*dir, "panel.db")},
		KeyFile:     platform.LocalKeyFile{Path: filepath.Join(*dir, "panel.key")},
		Random:      rand.Reader,
		Assets:      web.Assets,
		NodeBinary:  nodebundle.Open,
	})
	if err != nil {
		return fmt.Errorf("启动面板: %w", err)
	}
	defer core.Close()
	httpListener, err := net.Listen("tcp4", *httpAddress)
	if err != nil {
		return fmt.Errorf("监听网页端口: %w", err)
	}
	defer httpListener.Close()
	controlListener, err := net.Listen("tcp4", *controlAddress)
	if err != nil {
		return fmt.Errorf("监听控制端口: %w", err)
	}
	defer controlListener.Close()
	server := &http.Server{Handler: core.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	failures := make(chan error, 2)
	controlServer := grpc.NewServer(grpc.MaxRecvMsgSize(64<<10), grpc.MaxSendMsgSize(64<<10))
	core.RegisterControl(controlServer)
	defer controlServer.Stop()
	go func() {
		if err := server.Serve(httpListener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			failures <- err
		}
	}()
	go func() {
		if err := controlServer.Serve(controlListener); err != nil {
			failures <- err
		}
	}()
	log.Printf("面板 HTTP 监听 %s；gRPC 控制监听 %s；公网源 %s", httpListener.Addr(), controlListener.Addr(), *origin)
	var serveErr error
	select {
	case <-ctx.Done():
	case serveErr = <-failures:
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return errors.Join(serveErr, server.Shutdown(shutdown))
}
