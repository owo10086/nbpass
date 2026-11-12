package panel

import (
	"context"
	"database/sql"
	"net/http"
	"strings"

	"nbpass/internal/forwarding"
)

type forwardChainView struct {
	forwarding.ForwardChain
	RuleCount int                   `json:"rule_count"`
	Hops      []forwarding.ChainHop `json:"hops"`
}

func loadForwardChains(ctx context.Context, q queryer) ([]forwarding.ForwardChain, error) {
	rows, err := q.QueryContext(ctx, `SELECT c.id,c.name,h.exit_group_id FROM forward_chains c LEFT JOIN forward_chain_hops h ON h.chain_id=c.id ORDER BY c.id,h.position`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	chains := []forwarding.ForwardChain{}
	for rows.Next() {
		var id, name string
		var group sql.NullString
		if err = rows.Scan(&id, &name, &group); err != nil {
			return nil, err
		}
		if len(chains) == 0 || chains[len(chains)-1].ID != id {
			chains = append(chains, forwarding.ForwardChain{ID: id, Name: name, ExitGroupIDs: []string{}})
		}
		if group.Valid {
			chains[len(chains)-1].ExitGroupIDs = append(chains[len(chains)-1].ExitGroupIDs, group.String)
		}
	}
	return chains, rows.Err()
}

// loadChainGroups projects only public dialing choices. No private key,
// credential or runtime grant can enter a path preview through this boundary.
func loadChainGroups(ctx context.Context, q queryer) (map[string]forwarding.ChainGroup, error) {
	rows, err := q.QueryContext(ctx, `SELECT g.id,g.name,n.id,n.name,m.address_id,
 CASE WHEN m.address_id='public' THEN t.address ELSE a.address END,
 COALESCE(CASE WHEN m.address_id='public' THEN t.public_port ELSE a.public_port END,t.listen_port)
 FROM exit_groups g JOIN exit_group_members m ON m.group_id=g.id
 JOIN nodes n ON n.id=m.node_id JOIN tunnel_exits t ON t.node_id=m.node_id
 LEFT JOIN tunnel_addresses a ON a.node_id=m.node_id AND a.id=m.address_id
 ORDER BY g.id,m.position`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	groups := map[string]forwarding.ChainGroup{}
	for rows.Next() {
		var id, name string
		var m forwarding.ChainMember
		if err = rows.Scan(&id, &name, &m.NodeID, &m.NodeName, &m.AddressID, &m.Address, &m.Port); err != nil {
			return nil, err
		}
		g := groups[id]
		g.ID = id
		g.Name = name
		g.Members = append(g.Members, m)
		groups[id] = g
	}
	return groups, rows.Err()
}

func (p *Panel) listForwardChains(w http.ResponseWriter, r *http.Request) {
	tx, err := p.db.BeginTx(r.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		problem(w, 503, "存储暂不可用")
		return
	}
	defer tx.Rollback()
	chains, err := loadForwardChains(r.Context(), tx)
	var groups map[string]forwarding.ChainGroup
	if err == nil {
		groups, err = loadChainGroups(r.Context(), tx)
	}
	views := []forwardChainView{}
	if err == nil {
		for _, chain := range chains {
			hops, e := forwarding.ExpandForwardChain(chain, groups)
			if e != nil {
				err = e
				break
			}
			views = append(views, forwardChainView{ForwardChain: chain, Hops: hops})
		}
	}
	_ = tx.Rollback()
	if err != nil {
		problem(w, 503, "无法读取转发链")
		return
	}
	respond(w, 200, map[string]any{"chains": views})
}

func (p *Panel) saveForwardChain(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name         string   `json:"name"`
		ExitGroupIDs []string `json:"exit_group_ids"`
	}
	if !decode(w, r, &body) {
		return
	}
	body.Name = strings.TrimSpace(body.Name)
	if body.Name == "" || len(body.Name) > 200 || len(body.ExitGroupIDs) < 2 {
		problem(w, 400, "转发链须有 1–200 字节名称及至少两个有序端点组")
		return
	}
	id := r.PathValue("id")
	creating := r.Method == "POST"
	if creating {
		var err error
		id, _, err = p.randomCredential()
		if err != nil {
			problem(w, 500, "无法创建转发链")
			return
		}
	}
	chain := forwarding.ForwardChain{ID: id, Name: body.Name, ExitGroupIDs: body.ExitGroupIDs}
	var hops []forwarding.ChainHop
	if !p.mutateForwarding(w, r, func(tx *sql.Tx) error {
		groups, err := loadChainGroups(r.Context(), tx)
		if err != nil {
			return err
		}
		hops, err = forwarding.ExpandForwardChain(chain, groups)
		if err != nil {
			return &forwardingError{400, err.Error()}
		}
		if creating {
			_, err = tx.ExecContext(r.Context(), "INSERT INTO forward_chains(id,name) VALUES(?,?)", id, chain.Name)
		} else {
			err = missingResult(tx.ExecContext(r.Context(), "UPDATE forward_chains SET name=? WHERE id=?", chain.Name, id))
		}
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(r.Context(), "DELETE FROM forward_chain_hops WHERE chain_id=?", id); err != nil {
			return err
		}
		for i, group := range chain.ExitGroupIDs {
			if _, err = tx.ExecContext(r.Context(), "INSERT INTO forward_chain_hops(chain_id,position,exit_group_id) VALUES(?,?,?)", id, i+1, group); err != nil {
				return err
			}
		}
		return validatePathChanges(r.Context(), tx, pathChanges{ChainIDs: []string{id}})
	}) {
		return
	}
	code := 200
	if creating {
		code = 201
	}
	respond(w, code, forwardChainView{ForwardChain: chain, Hops: hops})
}

func (p *Panel) deleteForwardChain(w http.ResponseWriter, r *http.Request) {
	if !p.mutateForwarding(w, r, func(tx *sql.Tx) error {
		return missingResult(tx.ExecContext(r.Context(), "DELETE FROM forward_chains WHERE id=?", r.PathValue("id")))
	}) {
		return
	}
	respond(w, 204, nil)
}
