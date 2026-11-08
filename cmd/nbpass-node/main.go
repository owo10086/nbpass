package main

import (
	"context"
	"crypto/rand"
	"crypto/x509"
	"errors"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"nbpass/internal/forwarding"
	"nbpass/internal/node"
	"nbpass/internal/platform"
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
	control := flag.String("control-url", "", "经 TLS 校验的 HTTPS 控制地址（必填）")
	ticket := flag.String("ticket", "", "首次接入或重新安装的一次性安装票据")
	dir := flag.String("data-dir", "/var/lib/nbpass-node", "节点凭据与本机密钥专用目录")
	enroll := flag.Bool("enroll-only", false, "兑换并持久化票据后退出，供安装脚本使用")
	ca := flag.String("ca-file", "", "可选的专用控制端点 CA 文件，默认使用系统根证书")
	limits := directResourceFlags(flag.CommandLine)
	flag.Parse()
	if err := limits.Validate(); err != nil {
		return err
	}
	var roots *x509.CertPool
	if *ca != "" {
		data, err := os.ReadFile(*ca)
		if err != nil {
			return errors.New("无法读取控制端点 CA 文件")
		}
		roots = x509.NewCertPool()
		if !roots.AppendCertsFromPEM(data) {
			return errors.New("控制端点 CA 文件无有效证书")
		}
	}
	return node.Run(ctx, node.Config{ControlURL: *control, InstallationTicket: *ticket, EnrollOnly: *enroll}, node.Dependencies{
		Clock: platform.RealClock{}, Store: node.FileStore{Dir: *dir, Random: rand.Reader}, Random: rand.Reader, RootCAs: roots,
		Forwarding: forwarding.Dependencies{Limits: *limits},
	})
}
