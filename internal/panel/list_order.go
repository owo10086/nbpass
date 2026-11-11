package panel

import (
	"context"
	"net/http"
	"slices"
)

// 显示顺序与配置快照无关：只影响列表和下拉框，loadRules 等编译路径保持按 id。
const listOrderMigration = `ALTER TABLE forward_rules ADD COLUMN sort_order INTEGER;
ALTER TABLE nodes ADD COLUMN sort_order INTEGER;
ALTER TABLE entry_groups ADD COLUMN sort_order INTEGER;
ALTER TABLE exit_groups ADD COLUMN sort_order INTEGER;`

type listOrder struct{ table, orderBy string }

// 调过顺序的行按 sort_order；从未调过的行和新建行按插入顺序排在其后，创建与导入无需填写。
var listOrders = map[string]listOrder{
	"rules":        {"forward_rules", "sort_order IS NULL, sort_order, rowid"},
	"nodes":        {"nodes", "sort_order IS NULL, sort_order, created_at, id"},
	"entry-groups": {"entry_groups", "sort_order IS NULL, sort_order, rowid"},
	"exit-groups":  {"exit_groups", "sort_order IS NULL, sort_order, rowid"},
}

func (p *Panel) listOrderRoutes(mux *http.ServeMux) {
	for path, list := range listOrders {
		mux.HandleFunc("PUT /api/"+path+"/order", func(w http.ResponseWriter, r *http.Request) { p.saveListOrder(w, r, list) })
	}
}

// displayOrder 返回 id 到显示位置的映射；表名只来自 listOrders，不拼接请求输入。
func displayOrder(ctx context.Context, q queryer, list listOrder) (map[string]int, error) {
	rows, err := q.QueryContext(ctx, "SELECT id FROM "+list.table+" ORDER BY "+list.orderBy)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	order := map[string]int{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		order[id] = len(order)
	}
	return order, rows.Err()
}

func sortByDisplayOrder[T any](ctx context.Context, q queryer, list listOrder, items []T, id func(T) string) error {
	order, err := displayOrder(ctx, q, list)
	if err != nil {
		return err
	}
	slices.SortStableFunc(items, func(a, b T) int { return order[id(a)] - order[id(b)] })
	return nil
}

// 整表提交新顺序：不走 mutateForwarding，不编译快照也不递增配置版本。
func (p *Panel) saveListOrder(w http.ResponseWriter, r *http.Request, list listOrder) {
	var body struct {
		IDs []string `json:"ids"`
	}
	if !decodeLimited(w, r, &body, 1<<20) {
		return
	}
	p.controlMu.Lock()
	defer p.controlMu.Unlock()
	tx, err := p.db.BeginTx(r.Context(), nil)
	if err != nil {
		problem(w, 503, "存储暂不可用")
		return
	}
	defer tx.Rollback()
	current, err := displayOrder(r.Context(), tx, list)
	if err != nil {
		problem(w, 503, "存储暂不可用")
		return
	}
	seen := map[string]bool{}
	for _, id := range body.IDs {
		if _, ok := current[id]; !ok || seen[id] {
			break
		}
		seen[id] = true
	}
	if len(seen) != len(body.IDs) || len(seen) != len(current) {
		problem(w, 409, "列表已变化，请刷新后重试")
		return
	}
	for position, id := range body.IDs {
		if _, err = tx.ExecContext(r.Context(), "UPDATE "+list.table+" SET sort_order=? WHERE id=?", position, id); err != nil {
			problem(w, 503, "存储暂不可用")
			return
		}
	}
	if err = tx.Commit(); err != nil {
		problem(w, 503, "存储暂不可用")
		return
	}
	respond(w, 204, nil)
}
