package panel

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"nbpass/internal/forwarding"
)

type entryGroupView struct {
	ID                string            `json:"id"`
	Name              string            `json:"name"`
	Members           []string          `json:"members"`
	PublicationStatus string            `json:"publication_status"`
	SyncStatus        string            `json:"sync_status"`
	SyncMembers       []groupSyncMember `json:"sync_members"`
}

func (p *Panel) forwardingRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/entry-groups", p.listEntryGroups)
	mux.HandleFunc("POST /api/entry-groups", p.saveEntryGroup)
	mux.HandleFunc("PUT /api/entry-groups/{id}", p.saveEntryGroup)
	mux.HandleFunc("DELETE /api/entry-groups/{id}", p.deleteEntryGroup)
	mux.HandleFunc("GET /api/rules", p.listRules)
	mux.HandleFunc("GET /api/rules/export", p.exportRules)
	mux.HandleFunc("POST /api/rules/import/preflight", p.importRules)
	mux.HandleFunc("POST /api/rules/import", p.importRules)
	mux.HandleFunc("POST /api/rules", p.saveRule)
	mux.HandleFunc("PUT /api/rules/{id}", p.saveRule)
	mux.HandleFunc("DELETE /api/rules/{id}", p.deleteRule)
}
func (p *Panel) listEntryGroups(w http.ResponseWriter, r *http.Request) {
	groups, err := p.entryGroupViews(r.Context())
	if err != nil {
		problem(w, 503, "存储暂不可用")
		return
	}
	respond(w, 200, map[string]any{"entry_groups": groups})
}

func (p *Panel) entryGroupViews(ctx context.Context) ([]entryGroupView, error) {
	p.controlMu.Lock()
	defer p.controlMu.Unlock()
	rows, err := p.db.QueryContext(ctx, `SELECT id,name,publication_status FROM entry_groups ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	groups := []entryGroupView{}
	for rows.Next() {
		var g entryGroupView
		if err := rows.Scan(&g.ID, &g.Name, &g.PublicationStatus); err != nil {
			return nil, err
		}
		groups = append(groups, g)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	nodes, members, err := p.loadSyncNodes(ctx)
	if err != nil {
		return nil, err
	}
	for i := range groups {
		groups[i].Members = members[groups[i].ID]
	}
	rules, err := loadRules(ctx, p.db)
	if err != nil {
		return nil, err
	}
	attachGroupSync(groups, buildRuleViews(rules, nodes, members), nodes)
	return groups, nil
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
		err = recordTrafficSources(ctx, tx)
	}
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
	p.entryMu.Lock()
	defer p.entryMu.Unlock()
	var body struct {
		Name    string   `json:"name"`
		Members []string `json:"members"`
	}
	if !decode(w, r, &body) {
		return
	}
	body.Name = strings.TrimSpace(body.Name)
	if body.Name == "" || len(body.Name) > 200 || len(body.Members) == 0 || len(body.Members) > 64 {
		problem(w, 400, "入口组须有名称及 1–64 个有序成员")
		return
	}
	seen := map[string]bool{}
	for _, member := range body.Members {
		if member == "" || seen[member] {
			problem(w, 400, "入口成员不能为空或重复")
			return
		}
		seen[member] = true
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
	publicationStatus := "未发布"
	if !p.mutateForwarding(w, r, func(tx *sql.Tx) error {
		if err := validateEntryMembers(r.Context(), tx, body.Members); err != nil {
			return err
		}
		if !creating {
			var active string
			err := tx.QueryRowContext(r.Context(), "SELECT active_node_id FROM entry_publications WHERE group_id=?", id).Scan(&active)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if active != "" && !containsEntryMember(body.Members, active) {
				return &forwardingError{409, "请先通过入口动作切换活动入口，再移除该成员"}
			}
			if err := protectPendingEntryMembers(r.Context(), tx, id, body.Members); err != nil {
				return err
			}
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
		for position, member := range body.Members {
			if _, err := tx.ExecContext(r.Context(), "INSERT INTO entry_group_members(group_id,node_id,position) VALUES(?,?,?)", id, member, position); err != nil {
				return err
			}
		}
		return tx.QueryRowContext(r.Context(), "SELECT publication_status FROM entry_groups WHERE id=?", id).Scan(&publicationStatus)
	}) {
		return
	}
	code := 200
	if creating {
		code = 201
	}
	respond(w, code, entryGroupView{ID: id, Name: body.Name, Members: body.Members, PublicationStatus: publicationStatus})
}
func (p *Panel) deleteEntryGroup(w http.ResponseWriter, r *http.Request) {
	p.entryMu.Lock()
	defer p.entryMu.Unlock()
	if !p.mutateForwarding(w, r, func(tx *sql.Tx) error {
		var pending int
		if err := tx.QueryRowContext(r.Context(), "SELECT count(*) FROM entry_actions WHERE group_id=? AND stage<>?", r.PathValue("id"), EntryWritten).Scan(&pending); err != nil {
			return err
		}
		if pending > 0 {
			return &forwardingError{409, "入口动作尚未核对完成，不能删除入口组"}
		}
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

const ruleColumns = "r.id,r.name,r.entry_group_id,r.kind,r.listen_port,r.target_address,r.target_family,r.target_port,r.allow_private,r.enabled,r.exit_group_id"

type scanner interface{ Scan(...any) error }

func scanRule(row scanner) (forwarding.Rule, error) {
	var r forwarding.Rule
	err := row.Scan(&r.ID, &r.Name, &r.EntryGroupID, &r.Kind, &r.ListenPort, &r.TargetAddress, &r.TargetFamily, &r.TargetPort, &r.AllowPrivate, &r.Enabled, &r.ExitGroupID)
	return r, err
}
func (p *Panel) listRules(w http.ResponseWriter, r *http.Request) {
	rules, err := p.ruleSyncViews(r.Context())
	if err != nil {
		problem(w, 503, "存储暂不可用")
		return
	}
	views, err := p.rulesWithTraffic(r.Context(), rules)
	if err != nil {
		problem(w, 503, "存储暂不可用")
		return
	}
	respond(w, 200, map[string]any{"rules": views})
}

func (p *Panel) ruleSyncViews(ctx context.Context) ([]ruleView, error) {
	p.controlMu.Lock()
	defer p.controlMu.Unlock()
	rules, err := loadRules(ctx, p.db)
	if err != nil {
		return nil, err
	}
	nodes, groups, err := p.loadSyncNodes(ctx)
	if err != nil {
		return nil, err
	}
	return buildRuleViews(rules, nodes, groups), nil
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
	var input struct {
		forwarding.Rule
		RandomPort bool `json:"random_port"`
	}
	if !decode(w, r, &input) {
		return
	}
	body := input.Rule
	creating := r.Method == "POST"
	if input.RandomPort {
		if !creating || body.ListenPort != 0 {
			problem(w, 400, "仅新建规则可选择随机端口，且不能同时指定监听端口")
			return
		}
		body.ListenPort = 1024 // 先验证其余输入，具体端口在同一保存事务内选择。
	}
	if err := body.Validate(); err != nil {
		problem(w, 400, err.Error())
		return
	}
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
		if body.Kind == forwarding.Tunnel {
			if err := tx.QueryRowContext(r.Context(), "SELECT count(*) FROM exit_groups WHERE id=?", body.ExitGroupID).Scan(&n); err != nil {
				return err
			}
			if n != 1 {
				return &forwardingError{400, "出口组不存在"}
			}
		}
		if input.RandomPort {
			port, err := p.randomRulePort(r.Context(), tx, body.EntryGroupID)
			if err != nil {
				return err
			}
			body.ListenPort = port
		}
		return storeRule(r.Context(), tx, body, creating)
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
