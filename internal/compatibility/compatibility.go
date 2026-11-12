// Package compatibility owns format windows, independently of software versions.
package compatibility

import "nbpass/internal/controlpb"

const ControlCurrent uint32 = 2
const ControlPrevious uint32 = 1
const TunnelCurrent uint32 = 1
const StateCurrent uint32 = 2

// Reserved IX capabilities are advertised only by the slice implementing them.
const (
	IXAddressDialing = "ix_address_dialing"
	IXMemberIdentity = "ix_member_identity"
	OutboundIPv4     = "outbound_ipv4"
	ForwardChains    = "forward_chains"
)

// State 2 is a one-way upgrade: read 1/2, write only 2. Old readers reject 2
// before restoring forwarding; cross-format installs require the reinstall flow.
func Current() *controlpb.Compatibility {
	return &controlpb.Compatibility{ControlMin: 1, ControlMax: 2, TunnelMin: 1, TunnelMax: 1, StateMin: 1, StateMax: 2, StateWrite: 2, Capabilities: []string{"upgrade", "tunnel_authorization", "exit_failover", "tunnel_rotation", "rotation_recheck", "dns_watch", IXAddressDialing}}
}
func Contains(min, max, version uint32) bool {
	return min > 0 && min <= max && min <= version && version <= max
}
func Supports(p *controlpb.Compatibility, capability string) bool {
	for _, c := range p.GetCapabilities() {
		if c == capability {
			return true
		}
	}
	return false
}
func CompatibleState(p *controlpb.Compatibility) bool {
	return p != nil && Contains(p.StateMin, p.StateMax, StateCurrent) && p.StateWrite == StateCurrent
}
