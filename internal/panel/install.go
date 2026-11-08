package panel

import (
	_ "embed"
	"io"
	"net/http"
	"strings"
)

//go:embed node-install.sh
var nodeInstaller string

func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }

func (p *Panel) installNode(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	_, _ = io.WriteString(w, strings.ReplaceAll(nodeInstaller, "__NODE_DOWNLOAD_URL__", shellQuote(p.config.PublicOrigin+"/downloads/nbpass-node-linux-amd64")))
}

func (p *Panel) downloadNode(w http.ResponseWriter, r *http.Request) {
	if p.deps.NodeBinary == nil {
		problem(w, 503, "当前面板未包含节点程序")
		return
	}
	image, err := p.deps.NodeBinary()
	if err != nil {
		problem(w, 503, "无法读取节点程序")
		return
	}
	defer image.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="nbpass-node-linux-amd64"`)
	_, _ = io.Copy(w, image)
}
