package panel

import (
	_ "embed"
	"io"
	"net/http"
	"strings"
)

//go:embed node-install.sh
var nodeInstaller string

//go:embed node-update-install.sh
var nodeUpdateInstaller string

// Download fully before executing: a failed or truncated response must never
// execute a partial installer or have its exit status hidden by a shell pipe.
const nodeInstallBootstrap = `set -eu; umask 077; installer=$(mktemp "${TMPDIR:-/tmp}/nbpass-install.XXXXXX"); trap 'rm -f "$installer"' EXIT; trap 'exit 1' HUP INT TERM; printf '%s\n' '[准备] 正在下载安装脚本，网络故障最多重试 2 次…'; if curl --proto '=https' --proto-redir '=https' --tlsv1.2 -fL --show-error --connect-timeout 60 --max-time 180 --retry 2 --retry-delay 3 --retry-connrefused "$1" -o "$installer"; then shift; sh "$installer" "$@"; else result=$?; printf '%s\n' '[失败] 安装脚本下载失败，未执行安装；请检查网络后重试。' >&2; exit "$result"; fi`

func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }

func (p *Panel) installNode(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	script := strings.ReplaceAll(nodeInstaller, "__UPDATE_INSTALL__", strings.ReplaceAll(nodeUpdateInstaller, "__UPDATE_ORIGIN__", p.config.PublicOrigin))
	_, _ = io.WriteString(w, strings.ReplaceAll(script, "__NODE_DOWNLOAD_URL__", shellQuote(p.config.PublicOrigin+"/downloads/nbpass-node-linux-amd64")))
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
