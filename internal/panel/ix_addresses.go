package panel

import (
	"context"
	"net/netip"
	"strings"
)

func normalizeTunnelAddress(host string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
}
func validPublicPort(port *int) bool { return port == nil || *port >= 1 && *port <= 65535 }

// The public API reads only public configuration, never keys or pair credentials.
func loadTunnelExits(ctx context.Context, q queryer) ([]tunnelExitView, error) {
	rows, err := q.QueryContext(ctx, "SELECT node_id,address,listen_port,transport,server_name,certificate_pem,public_port FROM tunnel_exits ORDER BY node_id")
	if err != nil {
		return nil, err
	}
	exits := []tunnelExitView{}
	for rows.Next() {
		var v tunnelExitView
		if err = rows.Scan(&v.NodeID, &v.Address, &v.ListenPort, &v.Transport, &v.ServerName, &v.CertificatePEM, &v.PublicPort); err != nil {
			break
		}
		exits = append(exits, v)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	return exits, nil
}

func isIPv4Address(address string) bool {
	ip, err := netip.ParseAddr(address)
	return err == nil && ip.Is4()
}
