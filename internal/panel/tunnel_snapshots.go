package panel

import (
	"context"
	"database/sql"
	"errors"

	"google.golang.org/protobuf/proto"
	"nbpass/internal/controlpb"
	"nbpass/internal/forwarding"
)

// compileTunnels 给入口只发相关出口证书/节点对凭据，给出口发私钥及允许的规则目标。
// 节点 revision 不参与跨节点授权；独立授权代次和应用确认控制入口门禁。
func (p *Panel) compileTunnels(ctx context.Context, tx *sql.Tx, rules []forwarding.Rule, members map[string][]string, compiled map[string]*controlpb.ConfigSnapshot) error {
	groups, err := loadExitGroups(ctx, tx)
	if err != nil {
		return err
	}
	byGroup := map[string]exitGroupView{}
	for _, g := range groups {
		byGroup[g.ID] = g
	}
	capable, err := authorizationCapabilities(ctx, tx)
	if err != nil {
		return err
	}
	generations := map[string]uint64{}
	for _, r := range rules {
		generation, err := updateAuthorizationGeneration(ctx, tx, r, members[r.EntryGroupID], byGroup[r.ExitGroupID].Members)
		if err != nil {
			return err
		}
		generations[r.ID] = generation
	}
	rows, err := tx.QueryContext(ctx, "SELECT node_id,address,listen_port,transport,server_name,certificate_pem,private_key_ciphertext FROM tunnel_exits ORDER BY node_id")
	if err != nil {
		return err
	}
	endpoints := map[string]*controlpb.TunnelEndpoint{}
	for rows.Next() {
		var id, address, transport, name string
		var port uint32
		var cert, sealed []byte
		if err := rows.Scan(&id, &address, &port, &transport, &name, &cert, &sealed); err != nil {
			rows.Close()
			return err
		}
		key, err := p.secrets.(*sealedSecrets).open("tunnel-key:"+id, sealed)
		if err != nil {
			rows.Close()
			return err
		}
		compiled[id].TunnelServer = &controlpb.TunnelServer{ListenPort: port, Transport: controlpb.TunnelTransport_TUNNEL_TRANSPORT_WSS, ServerName: name, CertificatePem: cert, PrivateKeyPem: key}
		endpoints[id] = &controlpb.TunnelEndpoint{NodeId: id, Address: address, Port: port, Transport: controlpb.TunnelTransport_TUNNEL_TRANSPORT_WSS, ServerName: name, CertificatePem: cert}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	legacy := map[[3]string]bool{}
	if len(endpoints) > 0 {
		legacy, err = p.legacyTunnelAuthorizations(ctx, tx, rules)
		if err != nil {
			return err
		}
	}
	legacyReady := map[[2]string]bool{}
	pairs := map[string]bool{}
	seenGroups := map[string]bool{}
	for _, r := range rules {
		if !r.Enabled || r.Kind != forwarding.Tunnel {
			continue
		}
		g, ok := byGroup[r.ExitGroupID]
		if !ok {
			return errors.New("隧道规则引用的出口组不存在")
		}
		for _, entry := range members[r.EntryGroupID] {
			groupKey := entry + ":" + g.ID
			cg := &controlpb.ExitGroup{Id: g.ID, Strategy: controlpb.ExitStrategy_EXIT_STRATEGY_PRIMARY_BACKUP}
			for _, exit := range g.Members {
				pairKey := entry + ":" + exit
				token, err := p.pairCredential(ctx, tx, entry, exit)
				if err != nil {
					return err
				}
				ep := proto.Clone(endpoints[exit]).(*controlpb.TunnelEndpoint)
				ep.EntryId, ep.Credential = entry, token
				cg.Exits = append(cg.Exits, ep)
				server := compiled[exit].TunnelServer
				if !pairs[pairKey] {
					server.Peers = append(server.Peers, &controlpb.TunnelPeer{EntryId: entry, Credential: token})
					pairs[pairKey] = true
				}
				if capable[entry] && capable[exit] {
					config := r.Config()
					config.AuthorizationGeneration = generations[r.ID]
					server.Grants = append(server.Grants, &controlpb.TunnelGrant{EntryId: entry, Rule: config})
				} else if legacy[[3]string{exit, entry, r.ID}] {
					server.Grants = append(server.Grants, &controlpb.TunnelGrant{EntryId: entry, Rule: r.Config()})
					legacyReady[[2]string{entry, r.ID}] = true
				}
			}
			if !seenGroups[groupKey] {
				compiled[entry].ExitGroups = append(compiled[entry].ExitGroups, cg)
				seenGroups[groupKey] = true
			}
		}
	}
	if err := p.compileTunnelRotations(ctx, tx, compiled); err != nil {
		return err
	}
	return gateTunnelRules(ctx, tx, compiled, capable, generations, byGroup, legacyReady)
}
func (p *Panel) pairCredential(ctx context.Context, tx *sql.Tx, entry, exit string) (string, error) {
	name := "tunnel-pair:" + entry + ":" + exit
	var sealed []byte
	err := tx.QueryRowContext(ctx, "SELECT credential_ciphertext FROM tunnel_pairs WHERE entry_id=? AND exit_id=?", entry, exit).Scan(&sealed)
	if err == nil {
		plain, err := p.secrets.(*sealedSecrets).open(name, sealed)
		return string(plain), err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	token, _, err := p.randomCredential()
	if err != nil {
		return "", err
	}
	sealed, err = p.secrets.(*sealedSecrets).seal(name, []byte(token))
	if err != nil {
		return "", err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO tunnel_pairs(entry_id,exit_id,credential_ciphertext) VALUES(?,?,?)", entry, exit, sealed)
	return token, err
}
func (p *Panel) decodeSnapshot(id string, data []byte, encrypted bool, snapshot *controlpb.ConfigSnapshot) error {
	if encrypted {
		var err error
		data, err = p.secrets.(*sealedSecrets).open("node-snapshot:"+id, data)
		if err != nil {
			return err
		}
	}
	return proto.Unmarshal(data, snapshot)
}
