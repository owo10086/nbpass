package panel

import "net/http"

func (p *Panel) getOperations(w http.ResponseWriter, r *http.Request) {
	view, err := p.operationsSnapshot(r.Context())
	if err != nil {
		problem(w, 503, "运维状态暂不可用")
		return
	}
	respond(w, 200, view)
}
