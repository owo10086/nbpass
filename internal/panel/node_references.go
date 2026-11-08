package panel

import (
	"context"
	"database/sql"
	"net/http"
	"strings"
)

type nodeReference struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	Name string `json:"name"`
}

// nodeReferences 与删除共用事务。出口组只约定关系表接缝，不提前创建其管理模型。
// 票 08 沿用 group_id/node_id 和 forward_rules.exit_group_id 即可接入。
func nodeReferences(ctx context.Context, tx *sql.Tx, id string) ([]nodeReference, error) {
	var exists int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM nodes WHERE id=?", id).Scan(&exists); err != nil {
		return nil, err
	}
	if exists == 0 {
		return nil, &forwardingError{404, "节点不存在"}
	}
	queries := []string{
		`SELECT DISTINCT 'entry_group',g.id,g.name FROM entry_groups g JOIN entry_group_members m ON m.group_id=g.id WHERE m.node_id=?`,
		`SELECT DISTINCT 'rule',r.id,r.name FROM forward_rules r JOIN entry_group_members m ON m.group_id=r.entry_group_id WHERE m.node_id=?`,
	}
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_schema WHERE type='table' AND name IN ('exit_groups','exit_group_members')").Scan(&exists); err != nil {
		return nil, err
	}
	if exists == 2 {
		queries = append(queries, `SELECT DISTINCT 'exit_group',g.id,g.name FROM exit_groups g JOIN exit_group_members m ON m.group_id=g.id WHERE m.node_id=?`)
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM pragma_table_info('forward_rules') WHERE name='exit_group_id'").Scan(&exists); err != nil {
			return nil, err
		}
		if exists > 0 {
			queries = append(queries, `SELECT DISTINCT 'rule',r.id,r.name FROM forward_rules r JOIN exit_group_members m ON m.group_id=r.exit_group_id WHERE m.node_id=?`)
		}
	}
	args := make([]any, len(queries))
	for i := range args {
		args[i] = id
	}
	rows, err := tx.QueryContext(ctx, strings.Join(queries, " UNION ")+" ORDER BY 1,2", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	refs := []nodeReference{}
	for rows.Next() {
		var ref nodeReference
		if err := rows.Scan(&ref.Kind, &ref.ID, &ref.Name); err != nil {
			return nil, err
		}
		refs = append(refs, ref)
	}
	return refs, rows.Err()
}

func (p *Panel) getNodeReferences(w http.ResponseWriter, r *http.Request) {
	tx, err := p.db.BeginTx(r.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		problem(w, 503, "存储暂不可用")
		return
	}
	defer tx.Rollback()
	refs, err := nodeReferences(r.Context(), tx, r.PathValue("id"))
	_ = tx.Rollback()
	if err != nil {
		if !writeConfigurationError(w, err) {
			problem(w, 503, "无法读取节点引用")
		}
		return
	}
	respond(w, 200, map[string]any{"references": refs})
}

func (p *Panel) deleteNode(w http.ResponseWriter, r *http.Request) {
	refs, err := p.removeNode(r.Context(), r.PathValue("id"))
	if err != nil {
		if !writeConfigurationError(w, err) {
			problem(w, 503, "无法删除节点，请检查关联配置")
		}
		return
	}
	if len(refs) > 0 {
		labels := make([]string, 0, len(refs))
		for _, ref := range refs {
			labels = append(labels, map[string]string{"rule": "转发规则", "entry_group": "入口组", "exit_group": "出口组"}[ref.Kind]+"「"+ref.Name+"」（"+ref.ID+"）")
		}
		respond(w, 409, map[string]any{"error": "节点仍被引用，请先移除：" + strings.Join(labels, "、"), "code": "node_referenced", "references": refs})
		return
	}
	respond(w, 204, nil)
}

func (p *Panel) removeNode(ctx context.Context, id string) ([]nodeReference, error) {
	p.controlMu.Lock()
	defer p.controlMu.Unlock()
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	refs, err := nodeReferences(ctx, tx, id)
	if err != nil || len(refs) > 0 {
		return refs, err
	}
	// 删除运行态和凭据，事件历史没有外键且保留。业务引用绝不级联删除。
	for _, table := range []string{"installation_tickets", "retired_control_instances", "probe_realtime", "probe_minutes", "node_regions", "node_snapshots"} {
		if _, err = tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE node_id=?", id); err != nil {
			return nil, err
		}
	}
	if err = missingResult(tx.ExecContext(ctx, "DELETE FROM nodes WHERE id=?", id)); err == nil {
		err = tx.Commit()
	}
	if err != nil {
		// 新增关系若尚未接入引用清单，数据库外键仍阻止留下悬空配置。
		return nil, err
	}
	if session := p.controls[id]; session != nil {
		delete(p.controls, id)
		session.stop()
	}
	for session := range p.pendingControls[id] {
		session.stop()
	}
	delete(p.pendingControls, id)
	return refs, nil
}
