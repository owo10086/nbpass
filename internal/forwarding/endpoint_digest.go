package forwarding

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"nbpass/internal/controlpb"
)

// Empty identities retain the exact slice-02 public digest. Credentials, rotation
// phases and certificate bytes do not change the stable TLS identity (server_name).
func EndpointConfigDigest(ep *controlpb.TunnelEndpoint) string {
	data, _ := json.Marshal(struct {
		Node, Identity, Address string
		Port                    uint32
		Transport               controlpb.TunnelTransport
		ServerName              string
	}{ep.NodeId, NormalizeAddressID(ep.AddressId), ep.Address, ep.Port, ep.Transport, ep.ServerName})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func NormalizeAddressID(id string) string {
	if id == "" {
		return "public"
	}
	return id
}

func endpointKey(ep *controlpb.TunnelEndpoint) [2]string {
	return [2]string{ep.NodeId, NormalizeAddressID(ep.AddressId)}
}
