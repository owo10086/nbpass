package panel

import (
	"database/sql"
	"errors"
	"net/http"
)

// 整批在一次 mutateForwarding 中完成：只编译一次快照，受影响节点的配置版本只涨一次。
// 任一规则不存在或启用后配置校验失败，整批回滚。
func (p *Panel) batchRules(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Action string   `json:"action"`
		IDs    []string `json:"ids"`
	}
	if !decodeLimited(w, r, &body, 64<<10) {
		return
	}
	if (body.Action != "enable" && body.Action != "disable" && body.Action != "delete") || len(body.IDs) < 1 || len(body.IDs) > 500 {
		problem(w, 400, "批量操作须为启用、停用或删除，每批 1–500 条规则")
		return
	}
	seen := map[string]bool{}
	for _, id := range body.IDs {
		if seen[id] {
			problem(w, 400, "批量操作的规则不能重复")
			return
		}
		seen[id] = true
	}
	if !p.mutateForwarding(w, r, func(tx *sql.Tx) error {
		for _, id := range body.IDs {
			var err error
			if body.Action == "delete" {
				err = removeRule(r.Context(), tx, id)
			} else {
				err = missingResult(tx.ExecContext(r.Context(), "UPDATE forward_rules SET enabled=? WHERE id=?", body.Action == "enable", id))
			}
			var missing *forwardingError
			if errors.As(err, &missing) && missing.status == 404 {
				return &forwardingError{404, "选中的规则不存在，请刷新后重试"}
			}
			if err != nil {
				return err
			}
		}
		return nil
	}) {
		return
	}
	respond(w, 200, map[string]int{"count": len(body.IDs)})
}
