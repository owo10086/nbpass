package forwarding

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"

	"google.golang.org/protobuf/proto"
	"nbpass/internal/controlpb"
)

// SnapshotDigest 使用确定性 protobuf 编码，包含 revision，不包含摘要自身。
func SnapshotDigest(snapshot *controlpb.ConfigSnapshot) string {
	copy := proto.Clone(snapshot).(*controlpb.ConfigSnapshot)
	copy.Digest = ""
	data, _ := proto.MarshalOptions{Deterministic: true}.Marshal(copy)
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}
func ValidateSnapshot(s *controlpb.ConfigSnapshot) error {
	if s == nil || s.Revision == 0 || s.Digest != SnapshotDigest(s) {
		return errors.New("配置快照版本或摘要无效")
	}
	ids := map[string]bool{}
	for _, r := range s.Rules {
		if r == nil || r.Id == "" || ids[r.Id] || (r.Kind != controlpb.RuleKind_RULE_KIND_DIRECT && r.Kind != controlpb.RuleKind_RULE_KIND_TUNNEL) || r.TargetFamily != controlpb.AddressFamily_ADDRESS_FAMILY_IPV4 || r.ListenPort < 1 || r.ListenPort > 65535 || r.TargetPort < 1 || r.TargetPort > 65535 {
			return errors.New("转发配置无效")
		}
		if err := ValidateTarget(r.TargetAddress, r.AllowPrivate); err != nil {
			return err
		}
		ids[r.Id] = true
	}
	return validateTunnelSnapshot(s)
}
