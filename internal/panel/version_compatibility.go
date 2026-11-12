package panel

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"slices"

	"google.golang.org/protobuf/proto"
	"nbpass/internal/compatibility"
	"nbpass/internal/controlpb"
)

const versionCompatibilityMigration = `CREATE TABLE node_compatibility (
 node_id TEXT PRIMARY KEY REFERENCES nodes(id) ON DELETE CASCADE, report BLOB NOT NULL
);`

type nodeCompatibility struct {
	ProcessInstance string                   `json:"process_instance,omitempty"`
	ControlProtocol uint32                   `json:"control_protocol"`
	Legacy          bool                     `json:"legacy"`
	Node            *controlpb.Compatibility `json:"node"`
	Panel           *controlpb.Compatibility `json:"panel"`
	Capabilities    []string                 `json:"capabilities"`
}

func resolveCompatibility(r *controlpb.NodeReport) (nodeCompatibility, error) {
	local := compatibility.Current()
	peer := r.Compatibility
	legacy := peer == nil
	if legacy {
		if r.ProtocolVersion != 1 {
			return nodeCompatibility{}, errors.New("当前控制协议需要兼容范围声明")
		}
		peer = &controlpb.Compatibility{ControlMin: 1, ControlMax: 1, TunnelMin: 1, TunnelMax: 1, StateMin: 1, StateMax: 1, StateWrite: 1}
	} else if !compatibility.Contains(peer.ControlMin, peer.ControlMax, r.ProtocolVersion) || peer.TunnelMin == 0 || peer.TunnelMin > peer.TunnelMax || !compatibility.Contains(peer.StateMin, peer.StateMax, peer.StateWrite) || len(peer.Capabilities) > 32 {
		return nodeCompatibility{}, errors.New("兼容范围声明无效")
	}
	caps := []string{}
	for _, c := range local.Capabilities {
		supported := compatibility.Supports(peer, c)
		if legacy {
			// Protocol 1 has no extensible declaration: only its explicit flags
			// prove the corresponding pre-IX capability, never future names.
			switch c {
			case "upgrade":
				supported = r.UpgradeCapable
			case "dns_watch":
				supported = r.DnsWatchCapable
			case "rotation_recheck":
				supported = r.RotationRecheckCapable
			case "tunnel_authorization":
				supported = r.TunnelAuthorizationVersion == 1
			case "exit_failover":
				supported = r.ExitFailoverVersion == 1
			case "tunnel_rotation":
				supported = r.TunnelRotationVersion == 1
			}
		}
		if c == "tunnel_authorization" || c == "exit_failover" || c == "tunnel_rotation" || c == compatibility.IXAddressDialing || c == compatibility.IXMemberIdentity || c == compatibility.ForwardChains {
			supported = supported && compatibility.Contains(peer.TunnelMin, peer.TunnelMax, compatibility.TunnelCurrent)
		}
		if c == compatibility.IXAddressDialing || c == compatibility.IXMemberIdentity || c == compatibility.OutboundIPv4 || c == compatibility.ForwardChains {
			supported = supported && compatibility.CompatibleState(peer)
		}
		if supported {
			caps = append(caps, c)
		}
	}
	return nodeCompatibility{ProcessInstance: r.ProcessInstance, ControlProtocol: r.ProtocolVersion, Legacy: legacy, Node: proto.Clone(peer).(*controlpb.Compatibility), Panel: local, Capabilities: caps}, nil
}
func (c nodeCompatibility) supports(capability string) bool {
	for _, v := range c.Capabilities {
		if v == capability {
			return true
		}
	}
	return false
}

// Capability declarations constrain existing feature flags; they never grant a feature.
func (c nodeCompatibility) constrain(r *controlpb.NodeReport) *controlpb.NodeReport {
	r = proto.Clone(r).(*controlpb.NodeReport)
	r.UpgradeCapable = r.UpgradeCapable && c.supports("upgrade") && compatibility.CompatibleState(c.Node)
	r.DnsWatchCapable = r.DnsWatchCapable && c.supports("dns_watch")
	r.RotationRecheckCapable = r.RotationRecheckCapable && c.supports("rotation_recheck")
	tunnel := compatibility.Contains(c.Node.GetTunnelMin(), c.Node.GetTunnelMax(), compatibility.TunnelCurrent)
	if !tunnel || !c.supports("tunnel_authorization") {
		r.TunnelAuthorizationVersion = 0
	}
	if !tunnel || !c.supports("exit_failover") {
		r.ExitFailoverVersion = 0
	}
	if !tunnel || !c.supports("tunnel_rotation") {
		r.TunnelRotationVersion = 0
	}
	return r
}
func (p *Panel) getNodeCompatibility(w http.ResponseWriter, r *http.Request) {
	var raw []byte
	err := p.db.QueryRowContext(r.Context(), "SELECT report FROM node_compatibility WHERE node_id=?", r.PathValue("id")).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		var count int
		if p.db.QueryRowContext(r.Context(), "SELECT count(*) FROM nodes WHERE id=?", r.PathValue("id")).Scan(&count) != nil || count == 0 {
			problem(w, 404, "节点不存在")
			return
		}
		respond(w, 200, map[string]any{"panel": compatibility.Current(), "node": nil, "capabilities": []string{}})
		return
	}
	if err != nil {
		problem(w, 503, "无法读取节点兼容范围")
		return
	}
	var view nodeCompatibility
	if json.Unmarshal(raw, &view) != nil {
		problem(w, 503, "无法读取节点兼容范围")
		return
	}
	view.Panel = compatibility.Current()
	respond(w, 200, view)
}
func (p *Panel) saveNodeCompatibility(ctx context.Context, tx *sql.Tx, id string, c nodeCompatibility) error {
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO node_compatibility(node_id,report) VALUES(?,?) ON CONFLICT(node_id) DO UPDATE SET report=excluded.report", id, raw)
	return err
}

func compatibilityChanged(ctx context.Context, tx *sql.Tx, id string, next nodeCompatibility) (bool, error) {
	var raw []byte
	err := tx.QueryRowContext(ctx, "SELECT report FROM node_compatibility WHERE node_id=?", id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	var previous nodeCompatibility
	if err := json.Unmarshal(raw, &previous); err != nil {
		return false, err
	}
	oldCaps, nextCaps := slices.Clone(previous.Capabilities), slices.Clone(next.Capabilities)
	slices.Sort(oldCaps)
	slices.Sort(nextCaps)
	return previous.ControlProtocol != next.ControlProtocol || previous.Legacy != next.Legacy || !slices.Equal(oldCaps, nextCaps), nil
}
