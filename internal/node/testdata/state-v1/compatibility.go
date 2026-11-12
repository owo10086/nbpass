// Package compatibility owns format windows, independently of software versions.
package compatibility

import "nbpass/internal/controlpb"

const ControlCurrent uint32 = 2
const ControlPrevious uint32 = 1
const TunnelCurrent uint32 = 1
const StateCurrent uint32 = 1

// The previous software release uses the same tunnel and durable state formats.
// Keep writers at 1 until both the running and rollback binaries can read a successor.
func Current() *controlpb.Compatibility {
	return &controlpb.Compatibility{ControlMin: 1, ControlMax: 2, TunnelMin: 1, TunnelMax: 1, StateMin: 1, StateMax: 1, StateWrite: 1, Capabilities: []string{"upgrade", "tunnel_authorization", "exit_failover", "tunnel_rotation", "rotation_recheck", "dns_watch"}}
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
