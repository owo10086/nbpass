package panel

import (
	"context"
	"database/sql"
	"errors"

	"google.golang.org/protobuf/proto"
	"nbpass/internal/compatibility"
	"nbpass/internal/controlpb"
	"nbpass/internal/forwarding"
)

// compileTunnels 给入口只发相关出口证书/节点对凭据，给出口发私钥及允许的规则目标。
// 节点 revision 不参与跨节点授权；独立授权代次和应用确认控制入口门禁。
func (p *Panel) compileTunnels(ctx context.Context, tx *sql.Tx, rules []forwarding.Rule, members map[string][]string, compiled map[string]*controlpb.ConfigSnapshot, plans map[[2]string]capabilityPlan) error {
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
	caps, err := loadCapabilitySet(ctx, tx)
	if err != nil {
		return err
	}
	addresses, err := loadTunnelExits(ctx, tx)
	if err != nil {
		return err
	}
	generations := map[string]uint64{}
	for _, r := range rules {
		generation, err := updateAuthorizationGeneration(ctx, tx, r, forwarding.RuleEntryNodes(r, members), byGroup[r.ExitGroupID].Members)
		if err != nil {
			return err
		}
		generations[r.ID] = generation
	}
	rows, err := tx.QueryContext(ctx, "SELECT node_id,address,listen_port,transport,server_name,certificate_pem,private_key_ciphertext,address_version,public_port FROM tunnel_exits ORDER BY node_id")
	if err != nil {
		return err
	}
	endpoints := map[[2]string]*controlpb.TunnelEndpoint{}
	newDialing := map[[2]string]bool{}
	for rows.Next() {
		var id, address, transport, name, addressVersion string
		var port uint32
		var publicPort *uint32
		var cert, sealed []byte
		if err := rows.Scan(&id, &address, &port, &transport, &name, &cert, &sealed, &addressVersion, &publicPort); err != nil {
			rows.Close()
			return err
		}
		key, err := p.secrets.(*sealedSecrets).open("tunnel-key:"+id, sealed)
		if err != nil {
			rows.Close()
			return err
		}
		compiled[id].TunnelServer = &controlpb.TunnelServer{ListenPort: port, Transport: controlpb.TunnelTransport_TUNNEL_TRANSPORT_WSS, ServerName: name, CertificatePem: cert, PrivateKeyPem: key}
		newDialing[[2]string{id, "public"}] = publicPort != nil || !isIPv4Address(address)
		if publicPort != nil {
			port = *publicPort
		}
		endpoints[[2]string{id, "public"}] = &controlpb.TunnelEndpoint{NodeId: id, Address: address, AddressVersion: addressVersion, Port: port, Transport: controlpb.TunnelTransport_TUNNEL_TRANSPORT_WSS, ServerName: name, CertificatePem: cert}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, v := range addresses {
		for _, a := range v.DedicatedAddresses {
			ep := proto.Clone(endpoints[[2]string{v.NodeID, "public"}]).(*controlpb.TunnelEndpoint)
			ep.AddressId, ep.Address, ep.AddressVersion = a.ID, a.Address, ""
			ep.Port = uint32(v.ListenPort)
			if a.PublicPort != nil {
				ep.Port = uint32(*a.PublicPort)
			}
			key := [2]string{v.NodeID, a.ID}
			endpoints[key] = ep
			newDialing[key] = a.PublicPort != nil || !isIPv4Address(a.Address)
		}
	}
	legacy := map[[3]string]bool{}
	if len(endpoints) > 0 {
		legacy, err = p.legacyTunnelAuthorizations(ctx, tx, rules, members)
		if err != nil {
			return err
		}
	}
	legacyReady := map[[2]string]bool{}
	pairs := map[string]bool{}
	for _, r := range rules {
		if !r.Enabled || r.Kind != forwarding.Tunnel {
			continue
		}
		if r.ChainID != "" {
			if err := p.compileChain(ctx, tx, r, forwarding.RuleEntryNodes(r, members), byGroup, compiled, plans, endpoints, pairs, generations[r.ID]); err != nil {
				return err
			}
			continue
		}
		g, ok := byGroup[r.ExitGroupID]
		if !ok {
			return errors.New("隧道规则引用的出口组不存在")
		}
		for _, entry := range forwarding.RuleEntryNodes(r, members) {
			plan := plans[[2]string{entry, r.ID}]
			if plan.Blocked {
				continue
			}
			cg := &controlpb.ExitGroup{Id: g.ID, Strategy: controlpb.ExitStrategy_EXIT_STRATEGY_PRIMARY_BACKUP}
			for _, member := range g.MemberAddresses {
				exit := member.NodeID
				if !plan.allows(exit) {
					continue
				}
				pairKey := entry + ":" + exit
				token, err := p.pairCredential(ctx, tx, entry, exit)
				if err != nil {
					return err
				}
				key := [2]string{exit, member.AddressID}
				if endpoints[key] == nil {
					return &forwardingError{400, "端点组地址不存在或不属于该服务器"}
				}
				ep := proto.Clone(endpoints[key]).(*controlpb.TunnelEndpoint)
				ep.EntryId, ep.Credential = entry, token
				// Real 02 consumers keep their exact public shape and digest contract.
				identity := caps[entry].capabilities[compatibility.IXMemberIdentity]
				if identity {
					ep.AddressId = member.AddressID
				}
				if identity || caps[entry].capabilities[compatibility.IXAddressDialing] || newDialing[key] {
					ep.EndpointConfigDigest = forwarding.EndpointConfigDigest(ep)
				}
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
				} else if len(g.Members) == 1 && legacy[[3]string{exit, entry, r.ID}] {
					server.Grants = append(server.Grants, &controlpb.TunnelGrant{EntryId: entry, Rule: r.Config()})
					legacyReady[[2]string{entry, r.ID}] = true
				}
			}
			mergeChainGroup(compiled[entry], cg, g)
		}
	}
	if err := p.compileTunnelRotations(ctx, tx, compiled); err != nil {
		return err
	}
	return gateTunnelRules(ctx, tx, compiled, capable, generations, byGroup, legacyReady, plans)
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
