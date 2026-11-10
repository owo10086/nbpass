package panel

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

func (p *Panel) Handler() http.Handler {
	mux := http.NewServeMux()
	p.upgradeRoutes(mux)
	p.recoveryRoutes(mux)
	p.forwardingRoutes(mux)
	p.tunnelRoutes(mux)
	p.managedDNSRoutes(mux)
	p.telegramRoutes(mux)
	p.rotationRoutes(mux)
	p.selfCheckRoutes(mux)
	p.dnsWatchRoutes(mux)
	p.retentionRoutes(mux)
	mux.HandleFunc("GET /api/auth/status", p.status)
	mux.HandleFunc("POST /api/auth/setup", p.setup)
	mux.HandleFunc("POST /api/auth/login", p.login)
	mux.HandleFunc("POST /api/auth/logout", p.logout)
	mux.HandleFunc("POST /api/auth/sessions/revoke", p.revokeSessions)
	mux.HandleFunc("POST /api/auth/password", p.changePassword)
	mux.HandleFunc("GET /api/settings/trusted-proxies", p.getTrustedProxies)
	mux.HandleFunc("PUT /api/settings/trusted-proxies", p.putTrustedProxies)
	mux.HandleFunc("PUT /api/secrets/{name}", p.putSecret)
	mux.HandleFunc("GET /api/secrets/{name}", p.getSecret)
	mux.HandleFunc("GET /api/nodes", p.listNodes)
	mux.HandleFunc("GET /api/events", p.listEvents)
	mux.HandleFunc("POST /api/nodes", p.addNode)
	mux.HandleFunc("GET /api/nodes/{id}/listener-ports", p.getNodeListenerPorts)
	mux.HandleFunc("PUT /api/nodes/{id}/listener-ports", p.setNodeListenerPorts)
	mux.HandleFunc("GET /api/nodes/{id}/references", p.getNodeReferences)
	mux.HandleFunc("DELETE /api/nodes/{id}", p.deleteNode)
	mux.HandleFunc("GET /api/nodes/{id}/probe", p.getProbe)
	mux.HandleFunc("POST /api/nodes/{id}/token/reset", p.resetNodeToken)
	mux.HandleFunc("POST /api/nodes/{id}/token/revoke", p.revokeNodeToken)
	mux.HandleFunc("PUT /api/nodes/{id}/maintenance", p.maintainNode)
	mux.HandleFunc("PUT /api/nodes/{id}/region", p.setNodeRegion)
	mux.HandleFunc("PUT /api/nodes/{id}/checker", p.setNodeChecker)
	mux.HandleFunc("GET /install-node.sh", p.installNode)
	mux.HandleFunc("GET /downloads/nbpass-node-linux-amd64", p.downloadNode)
	mux.HandleFunc("GET /api/overview", p.getOperations)
	mux.HandleFunc("/", p.frontend)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; object-src 'none'; base-uri 'self'; frame-ancestors 'none'")
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
			if r.Header.Get("Origin") != p.config.PublicOrigin || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
				problem(w, http.StatusForbidden, "写请求必须来自面板同源页面")
				return
			}
		}
		if strings.HasPrefix(r.URL.Path, "/api/") && r.URL.Path != "/api/auth/status" && r.URL.Path != "/api/auth/setup" {
			configured, err := p.configured(r)
			if err != nil {
				problem(w, 503, "存储暂不可用")
				return
			}
			if !configured {
				problem(w, http.StatusPreconditionRequired, "请先设置管理员密码")
				return
			}
			if r.URL.Path != "/api/auth/login" {
				valid, err := p.authenticate(r)
				if err != nil {
					problem(w, 503, "存储暂不可用")
					return
				}
				if !valid {
					p.clearSession(w)
					problem(w, http.StatusUnauthorized, "请登录面板")
					return
				}
			}
		}
		mux.ServeHTTP(w, r)
	})
}

func respond(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if body != nil {
		_ = json.NewEncoder(w).Encode(body)
	}
}

func problem(w http.ResponseWriter, status int, message string) {
	respond(w, status, map[string]string{"error": message})
}

func decode(w http.ResponseWriter, r *http.Request, body any) bool {
	if media := strings.Split(r.Header.Get("Content-Type"), ";")[0]; media != "application/json" {
		problem(w, http.StatusUnsupportedMediaType, "请求体必须使用 JSON")
		return false
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(body); err != nil {
		problem(w, 400, "请求体无效或过大")
		return false
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		problem(w, 400, "只允许一个 JSON 对象")
		return false
	}
	return true
}
