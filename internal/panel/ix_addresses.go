package panel

import (
	"context"
	"net/netip"
	"strings"
)

type dedicatedAddress struct {
	ID         string `json:"id,omitempty"`
	Name       string `json:"name"`
	Address    string `json:"address"`
	PublicPort *int   `json:"public_port"`
}

func normalizeTunnelAddress(host string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
}
func validPublicPort(port *int) bool { return port == nil || *port >= 1 && *port <= 65535 }

// An address is a dialing choice on a server; listening/TLS/credentials remain server scoped.
func selectedTunnelAddress(v tunnelExitView, id string) (string, *int, bool) {
	if id == "public" {
		return v.Address, v.PublicPort, true
	}
	for _, a := range v.DedicatedAddresses {
		if a.ID == id {
			return a.Address, a.PublicPort, true
		}
	}
	return "", nil, false
}

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
		v.DedicatedAddresses = []dedicatedAddress{}
		exits = append(exits, v)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	byID := map[string]int{}
	for i, v := range exits {
		byID[v.NodeID] = i
	}
	rows, err = q.QueryContext(ctx, "SELECT node_id,id,name,address,public_port FROM tunnel_addresses ORDER BY node_id,position")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var node string
		var d dedicatedAddress
		if err = rows.Scan(&node, &d.ID, &d.Name, &d.Address, &d.PublicPort); err != nil {
			return nil, err
		}
		i := byID[node]
		exits[i].DedicatedAddresses = append(exits[i].DedicatedAddresses, d)
	}
	return exits, rows.Err()
}

func isIPv4Address(address string) bool {
	ip, err := netip.ParseAddr(address)
	return err == nil && ip.Is4()
}
