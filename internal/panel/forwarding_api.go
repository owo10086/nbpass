package panel

import (
	"context"
	"database/sql"
	"net/http"
	"strings"

	"nbpass/internal/forwarding"
)

type entryGroupView struct {
	ID                string   `json:"id"`
	Name              string   `json:"name"`
	Members           []string `json:"members"`
	PublicationStatus string   `json:"publication_status"`
}

func (p *Panel) forwardingRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/entry-groups", p.listEntryGroups)
	mux.HandleFunc("POST /api/entry-groups", p.saveEntryGroup)
	mux.HandleFunc("PUT /api/entry-groups/{id}", p.saveEntryGroup)
	mux.HandleFunc("DELETE /api/entry-groups/{id}", p.deleteEntryGroup)
	mux.HandleFunc("GET /api/rules", p.listRules)
	mux.HandleFunc("POST /api/rules", p.saveRule)
	mux.HandleFunc("PUT /api/rules/{id}", p.saveRule)
	mux.HandleFunc("DELETE /api/rules/{id}", p.deleteRule)
}
func (p *Panel) listEntryGroups(w http.ResponseWriter, r *http.Request) {
	rows, err := p.db.QueryContext(r.Context(), `SELECT g.id,g.name,g.publication_status,m.node_id FROM entry_groups g JOIN entry_group_members m ON m.group_id=g.id ORDER BY g.id,m.position`)
	if err != nil {
		problem(w, 503, "存储暂不可用")
		return
	}
	defer rows.Close()
	groups := []entryGroupView{}
	for rows.Next() {
		var g entryGroupView
		var member string
		if err := rows.Scan(&g.ID, &g.Name, &g.PublicationStatus, &member); err != nil {
			problem(w, 503, "存储暂不可用")
			return
		}
		g.Members = []string{member}
		groups = append(groups, g)
	}
	if rows.Err() != nil {
		problem(w, 503, "存储暂不可用")
		return
	}
	respond(w, 200, map[string]any{"entry_groups": groups})
}

// mutateForwarding 把业务改动和所有受影响节点的完整快照原子提交，然后通知控制流。
func (p *Panel) mutateForwarding(w http.ResponseWriter, r *http.Request, change func(*sql.Tx) error) bool {
	if err := p.commitForwarding(r.Context(), change); err != nil {
		if !writeConfigurationError(w, err) {
			problem(w, 503, "无法保存配置快照")
		}
		return false
	}
	return true
}

// 提交与 HTTP 写出分离；慢读者不能持有控制锁或数据库连接。
func (p *Panel) commitForwarding(ctx context.Context, change func(*sql.Tx) error) error {
	p.controlMu.Lock()
	defer p.controlMu.Unlock()
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = change(tx); err != nil {
		return err
	}
	changed, err := p.compileSnapshots(ctx, tx)
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		return err
	}
	for _, id := range changed {
		if session := p.controls[id]; session != nil {
			select {
			case session.changed <- struct{}{}:
			default:
			}
		}
	}
	return nil
}

type forwardingError struct {
	status  int
	message string
}

func (e *forwardingError) Error() string { return e.message }
func missingResult(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return &forwardingError{404, "记录不存在"}
	}
	return nil
}
func (p *Panel) saveEntryGroup(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name    string   `json:"name"`
		Members []string `json:"members"`
	}
	if !decode(w, r, &body) {
		return
	}
	body.Name = strings.TrimSpace(body.Name)
	if body.Name == "" || len(body.Name) > 200 || len(body.Members) != 1 {
		problem(w, 400, "本阶段入口组须有名称且只有一个成员")
		return
	}
	id := r.PathValue("id")
	creating := r.Method == "POST"
	if creating {
		var err error
		id, _, err = p.randomCredential()
		if err != nil {
			problem(w, 500, "无法创建入口组")
			return
		}
	}
	if !p.mutateForwarding(w, r, func(tx *sql.Tx) error {
		var exists int
		if err := tx.QueryRowContext(r.Context(), "SELECT count(*) FROM nodes WHERE id=?", body.Members[0]).Scan(&exists); err != nil {
			return err
		}
		if exists != 1 {
			return &forwardingError{400, "入口成员不存在"}
		}
		if creating {
			if _, err := tx.ExecContext(r.Context(), "INSERT INTO entry_groups(id,name) VALUES(?,?)", id, body.Name); err != nil {
				return err
			}
		} else {
			if err := missingResult(tx.ExecContext(r.Context(), "UPDATE entry_groups SET name=? WHERE id=?", body.Name, id)); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(r.Context(), "DELETE FROM entry_group_members WHERE group_id=?", id); err != nil {
			return err
		}
		_, err := tx.ExecContext(r.Context(), "INSERT INTO entry_group_members(group_id,node_id,position) VALUES(?,?,0)", id, body.Members[0])
		return err
	}) {
		return
	}
	code := 200
	if creating {
		code = 201
	}
	respond(w, code, entryGroupView{ID: id, Name: body.Name, Members: body.Members, PublicationStatus: "未发布"})
}
func (p *Panel) deleteEntryGroup(w http.ResponseWriter, r *http.Request) {
	if !p.mutateForwarding(w, r, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(r.Context(), "SELECT count(*) FROM forward_rules WHERE entry_group_id=?", r.PathValue("id")).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return &forwardingError{409, "请先删除入口组上的转发规则"}
		}
		return missingResult(tx.ExecContext(r.Context(), "DELETE FROM entry_groups WHERE id=?", r.PathValue("id")))
	}) {
		return
	}
	respond(w, 204, nil)
}

const ruleColumns = "r.id,r.name,r.entry_group_id,r.kind,r.listen_port,r.target_address,r.target_family,r.target_port,r.allow_private,r.enabled"

type scanner interface{ Scan(...any) error }

func scanRule(row scanner) (forwarding.Rule, error) {
	var r forwarding.Rule
	err := row.Scan(&r.ID, &r.Name, &r.EntryGroupID, &r.Kind, &r.ListenPort, &r.TargetAddress, &r.TargetFamily, &r.TargetPort, &r.AllowPrivate, &r.Enabled)
	return r, err
}
func (p *Panel) listRules(w http.ResponseWriter, r *http.Request) {
	rules, err := loadRules(r.Context(), p.db)
	if err != nil {
		problem(w, 503, "存储暂不可用")
		return
	}
	respond(w, 200, map[string]any{"rules": rules})
}

type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func loadRules(ctx context.Context, q queryer) ([]forwarding.Rule, error) {
	rows, err := q.QueryContext(ctx, "SELECT "+ruleColumns+" FROM forward_rules r ORDER BY r.id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	rules := []forwarding.Rule{}
	for rows.Next() {
		r, err := scanRule(rows)
		if err != nil {
			return nil, err
		}
		rules = append(rules, r)
	}
	return rules, rows.Err()
}
func (p *Panel) saveRule(w http.ResponseWriter, r *http.Request) {
	var body forwarding.Rule
	if !decode(w, r, &body) {
		return
	}
	if err := body.Validate(); err != nil {
		problem(w, 400, err.Error())
		return
	}
	creating := r.Method == "POST"
	body.ID = r.PathValue("id")
	if creating {
		var err error
		body.ID, _, err = p.randomCredential()
		if err != nil {
			problem(w, 500, "无法创建规则")
			return
		}
	}
	if !p.mutateForwarding(w, r, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(r.Context(), "SELECT count(*) FROM entry_groups WHERE id=?", body.EntryGroupID).Scan(&n); err != nil {
			return err
		}
		if n != 1 {
			return &forwardingError{400, "入口组不存在"}
		}
		args := []any{body.Name, body.EntryGroupID, body.Kind, body.ListenPort, body.TargetAddress, body.TargetFamily, body.TargetPort, body.AllowPrivate, body.Enabled, body.ID}
		if creating {
			_, err := tx.ExecContext(r.Context(), `INSERT INTO forward_rules(name,entry_group_id,kind,listen_port,target_address,target_family,target_port,allow_private,enabled,id) VALUES(?,?,?,?,?,?,?,?,?,?)`, args...)
			return err
		}
		return missingResult(tx.ExecContext(r.Context(), `UPDATE forward_rules SET name=?,entry_group_id=?,kind=?,listen_port=?,target_address=?,target_family=?,target_port=?,allow_private=?,enabled=? WHERE id=?`, args...))
	}) {
		return
	}
	code := 200
	if creating {
		code = 201
	}
	respond(w, code, body)
}
func (p *Panel) deleteRule(w http.ResponseWriter, r *http.Request) {
	if !p.mutateForwarding(w, r, func(tx *sql.Tx) error {
		return missingResult(tx.ExecContext(r.Context(), "DELETE FROM forward_rules WHERE id=?", r.PathValue("id")))
	}) {
		return
	}
	respond(w, 204, nil)
}
