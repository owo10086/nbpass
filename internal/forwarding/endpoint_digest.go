package forwarding

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"nbpass/internal/controlpb"
)

// Public is the only address identity in slice 02. Credentials, rotation phases
// and certificate bytes do not change the stable TLS identity (server_name).
func EndpointConfigDigest(ep *controlpb.TunnelEndpoint) string {
	data, _ := json.Marshal(struct {
		Node, Identity, Address string
		Port                    uint32
		Transport               controlpb.TunnelTransport
		ServerName              string
	}{ep.NodeId, "public", ep.Address, ep.Port, ep.Transport, ep.ServerName})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
