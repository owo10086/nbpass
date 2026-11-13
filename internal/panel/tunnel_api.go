package panel

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net/http"
	"strings"

	"nbpass/internal/forwarding"
)

type tunnelExitView struct {
	NodeID         string                     `json:"node_id"`
	Address        string                     `json:"address"`
	PublicPort     *int                       `json:"public_port"`
	ListenPort     int                        `json:"listen_port"`
	Transport      forwarding.TunnelTransport `json:"transport"`
	ServerName     string                     `json:"server_name"`
	CertificatePEM string                     `json:"certificate_pem"`
}
type exitGroupView struct {
	ID              string                  `json:"id"`
	Name            string                  `json:"name"`
	Strategy        forwarding.ExitStrategy `json:"strategy"`
	Members         []string                `json:"members"`
	MemberAddresses []memberAddress         `json:"member_addresses"`
}

type memberAddress struct {
	NodeID    string `json:"node_id"`
	AddressID string `json:"address_id"`
}

func (p *Panel) tunnelRoutes(mux *http.ServeMux) {
	p.exitBlockedRoutes(mux)
	mux.HandleFunc("GET /api/forward-chains", p.listForwardChains)
	mux.HandleFunc("POST /api/forward-chains", p.saveForwardChain)
	mux.HandleFunc("PUT /api/forward-chains/{id}", p.saveForwardChain)
	mux.HandleFunc("DELETE /api/forward-chains/{id}", p.deleteForwardChain)
	mux.HandleFunc("GET /api/tunnel-exits", p.listTunnelExits)
	mux.HandleFunc("GET /api/nodes/{id}/exit-health", p.getExitHealth)
	mux.HandleFunc("POST /api/nodes/{id}/tunnel/rotation", p.startTunnelRotation)
	mux.HandleFunc("GET /api/nodes/{id}/tunnel/rotation", p.getTunnelRotation)
	mux.HandleFunc("PUT /api/nodes/{id}/tunnel", p.saveTunnelExit)
	mux.HandleFunc("GET /api/exit-groups", p.listExitGroups)
	mux.HandleFunc("POST /api/exit-groups", p.saveExitGroup)
	mux.HandleFunc("PUT /api/exit-groups/{id}", p.saveExitGroup)
	mux.HandleFunc("DELETE /api/exit-groups/{id}", p.deleteExitGroup)
}
func (p *Panel) listTunnelExits(w http.ResponseWriter, r *http.Request) {
	p.controlMu.Lock()
	exits, err := loadTunnelExits(r.Context(), p.db)
	p.controlMu.Unlock()
	if err != nil {
		problem(w, 503, "存储暂不可用")
		return
	}
	respond(w, 200, map[string]any{"exits": exits})
}
func (p *Panel) saveTunnelExit(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Address    string                     `json:"address"`
		ListenPort int                        `json:"listen_port"`
		Transport  forwarding.TunnelTransport `json:"transport"`
		PublicPort json.RawMessage            `json:"public_port"`
	}
	if !decode(w, r, &body) {
		return
	}
	body.Address = normalizeTunnelAddress(body.Address)
	if forwarding.ValidateTarget(body.Address, true) != nil || body.ListenPort < 1 || body.ListenPort > 65535 || body.Transport != forwarding.WSS {
		problem(w, 400, "出口须指定可连接 IPv4 或域名、1–65535 本机监听端口和 WSS 传输")
		return
	}
	var publicPort *int
	if len(body.PublicPort) > 0 {
		if json.Unmarshal(body.PublicPort, &publicPort) != nil || !validPublicPort(publicPort) {
			problem(w, 400, "对外端口须为空或 1–65535")
			return
		}
	}
	id := r.PathValue("id")
	var saved tunnelExitView
	if !p.mutateForwarding(w, r, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(r.Context(), "SELECT count(*) FROM nodes WHERE id=?", id).Scan(&n); err != nil {
			return err
		}
		if n != 1 {
			return &forwardingError{404, "节点不存在"}
		}
		existing, err := loadTunnelExits(r.Context(), tx)
		if err != nil {
			return err
		}
		var old *tunnelExitView
		for i := range existing {
			if existing[i].NodeID == id {
				old = &existing[i]
				break
			}
		}
		if old != nil && len(body.PublicPort) == 0 {
			publicPort = old.PublicPort
		}
		if old == nil {
			name, cert, key, err := p.newExitIdentity()
			if err != nil {
				return err
			}
			key, err = p.secrets.(*sealedSecrets).seal("tunnel-key:"+id, key)
			if err != nil {
				return err
			}
			_, err = tx.ExecContext(r.Context(), "INSERT INTO tunnel_exits(node_id,address,listen_port,transport,server_name,certificate_pem,private_key_ciphertext,public_port) VALUES(?,?,?,?,?,?,?,?)", id, body.Address, body.ListenPort, body.Transport, name, cert, key, publicPort)
			if err != nil {
				return err
			}
		} else {
			_, err = tx.ExecContext(r.Context(), "UPDATE tunnel_exits SET address_version=CASE WHEN address!=? OR listen_port!=? OR public_port IS NOT ? THEN '' ELSE address_version END,address=?,listen_port=?,transport=?,public_port=? WHERE node_id=?", body.Address, body.ListenPort, publicPort, body.Address, body.ListenPort, body.Transport, publicPort, id)
			if err != nil {
				return err
			}
		}
		all, err := loadTunnelExits(r.Context(), tx)
		if err != nil {
			return err
		}
		for _, v := range all {
			if v.NodeID == id {
				saved = v
				break
			}
		}
		return nil
	}) {
		return
	}
	respond(w, 200, struct {
		tunnelExitView
		Configured bool `json:"configured"`
	}{saved, true})
}

// 独立于 IP 的随机 DNS SAN；每出口自签，私钥只下发该出口。
func (p *Panel) newExitIdentity() (string, []byte, []byte, error) {
	identity, _, err := p.randomCredential()
	if err != nil {
		return "", nil, nil, err
	}
	sum := sha256.Sum256([]byte(identity))
	name := "exit-" + hex.EncodeToString(sum[:16]) + ".nbpass.internal"
	cert, key, err := p.newExitCertificate(name)
	return name, cert, key, err
}

func (p *Panel) newExitCertificate(name string) ([]byte, []byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), p.deps.Random)
	if err != nil {
		return nil, nil, err
	}
	now := p.deps.Clock.Now().UTC().Truncate(1e9)
	serialBytes := make([]byte, 16)
	if _, err := io.ReadFull(p.deps.Random, serialBytes); err != nil {
		return nil, nil, err
	}
	serialBytes[0] |= 1
	template := &x509.Certificate{SerialNumber: new(big.Int).SetBytes(serialBytes), Subject: pkix.Name{CommonName: name}, DNSNames: []string{name}, NotBefore: now, NotAfter: now.AddDate(5, 0, 0), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(p.deps.Random, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), nil
}
func loadExitGroups(ctx context.Context, q queryer) ([]exitGroupView, error) {
	rows, err := q.QueryContext(ctx, "SELECT g.id,g.name,g.strategy,m.node_id,m.address_id FROM exit_groups g JOIN exit_group_members m ON g.id=m.group_id ORDER BY g.id,m.position")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	groups := []exitGroupView{}
	for rows.Next() {
		var g exitGroupView
		var member memberAddress
		if err := rows.Scan(&g.ID, &g.Name, &g.Strategy, &member.NodeID, &member.AddressID); err != nil {
			return nil, err
		}
		g.Members = []string{member.NodeID}
		g.MemberAddresses = []memberAddress{member}
		if len(groups) > 0 && groups[len(groups)-1].ID == g.ID {
			groups[len(groups)-1].Members = append(groups[len(groups)-1].Members, member.NodeID)
			groups[len(groups)-1].MemberAddresses = append(groups[len(groups)-1].MemberAddresses, member)
		} else {
			groups = append(groups, g)
		}
	}
	return groups, rows.Err()
}
func (p *Panel) listExitGroups(w http.ResponseWriter, r *http.Request) {
	groups, err := loadExitGroups(r.Context(), p.db)
	if err == nil {
		err = sortByDisplayOrder(r.Context(), p.db, listOrders["exit-groups"], groups, func(g exitGroupView) string { return g.ID })
	}
	if err != nil {
		problem(w, 503, "存储暂不可用")
		return
	}
	respond(w, 200, map[string]any{"exit_groups": groups})
}
func (p *Panel) saveExitGroup(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name            string                  `json:"name"`
		Members         json.RawMessage         `json:"members"`
		MemberAddresses json.RawMessage         `json:"member_addresses"`
		Strategy        forwarding.ExitStrategy `json:"strategy"`
	}
	if !decode(w, r, &body) {
		return
	}
	var members []string
	var addresses []memberAddress
	if len(body.Members) > 0 && (string(body.Members) == "null" || json.Unmarshal(body.Members, &members) != nil) {
		problem(w, 400, "成员须为列表")
		return
	}
	if len(body.MemberAddresses) > 0 {
		if string(body.MemberAddresses) == "null" || json.Unmarshal(body.MemberAddresses, &addresses) != nil {
			problem(w, 400, "成员地址须为列表")
			return
		}
		if len(body.Members) > 0 && len(members) != len(addresses) {
			problem(w, 400, "成员与地址列表长度或顺序不一致")
			return
		}
		for i, m := range addresses {
			if m.AddressID != "public" || m.NodeID == "" || len(body.Members) > 0 && members[i] != m.NodeID {
				problem(w, 400, "成员列表不一致或端点地址标识无效")
				return
			}
		}
		members = nil
		for _, m := range addresses {
			members = append(members, m.NodeID)
		}
	} else {
		for _, id := range members {
			addresses = append(addresses, memberAddress{id, "public"})
		}
	}
	body.Name = strings.TrimSpace(body.Name)
	if body.Name == "" || len(body.Name) > 200 || len(members) == 0 || body.Strategy != forwarding.PrimaryBackup {
		problem(w, 400, "出口组须有名称、不重复的已配置 WSS 出口成员（按主备顺序）及主备策略")
		return
	}
	creating := r.Method == "POST"
	id := r.PathValue("id")
	if creating {
		var err error
		id, _, err = p.randomCredential()
		if err != nil {
			problem(w, 500, "无法创建出口组")
			return
		}
	}
	if !p.mutateForwarding(w, r, func(tx *sql.Tx) error {
		var n int
		seen := map[string]bool{}
		for _, member := range addresses {
			if seen[member.NodeID] {
				return &forwardingError{400, "出口成员不能重复"}
			}
			seen[member.NodeID] = true
			if err := tx.QueryRowContext(r.Context(), "SELECT count(*) FROM tunnel_exits WHERE node_id=?", member.NodeID).Scan(&n); err != nil {
				return err
			}
			if n != 1 {
				return &forwardingError{400, "出口成员不存在或尚未配置 WSS"}
			}
		}
		if creating {
			if _, err := tx.ExecContext(r.Context(), "INSERT INTO exit_groups(id,name,strategy) VALUES(?,?,?)", id, body.Name, body.Strategy); err != nil {
				return err
			}
		} else {
			if err := missingResult(tx.ExecContext(r.Context(), "UPDATE exit_groups SET name=?,strategy=? WHERE id=?", body.Name, body.Strategy, id)); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(r.Context(), "DELETE FROM exit_group_members WHERE group_id=?", id); err != nil {
			return err
		}
		for position, member := range addresses {
			if _, err := tx.ExecContext(r.Context(), "INSERT INTO exit_group_members(group_id,node_id,position,address_id) VALUES(?,?,?,?)", id, member.NodeID, position, member.AddressID); err != nil {
				return err
			}
		}
		return validatePathChanges(r.Context(), tx, pathChanges{ExitGroupIDs: []string{id}})
	}) {
		return
	}
	code := 200
	if creating {
		code = 201
	}
	respond(w, code, exitGroupView{ID: id, Name: body.Name, Strategy: body.Strategy, Members: members, MemberAddresses: addresses})
}
func (p *Panel) deleteExitGroup(w http.ResponseWriter, r *http.Request) {
	if !p.mutateForwarding(w, r, func(tx *sql.Tx) error {
		var chain string
		err := tx.QueryRowContext(r.Context(), "SELECT c.name FROM forward_chains c JOIN forward_chain_hops h ON h.chain_id=c.id WHERE h.exit_group_id=? ORDER BY c.id LIMIT 1", r.PathValue("id")).Scan(&chain)
		if err == nil {
			return &forwardingError{409, "端点组仍被转发链“" + chain + "”引用"}
		}
		if err != sql.ErrNoRows {
			return err
		}
		var n int
		if err := tx.QueryRowContext(r.Context(), "SELECT count(*) FROM forward_rules WHERE exit_group_id=?", r.PathValue("id")).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return &forwardingError{409, "请先删除引用该出口组的转发规则"}
		}
		return missingResult(tx.ExecContext(r.Context(), "DELETE FROM exit_groups WHERE id=?", r.PathValue("id")))
	}) {
		return
	}
	respond(w, 204, nil)
}
