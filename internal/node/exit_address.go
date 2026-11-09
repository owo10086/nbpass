package node

import (
	"nbpass/internal/controlpb"
	"nbpass/internal/forwarding"
)

// 旧节点的 JSON 缓存可能丢失未知 protobuf 字段但保留原摘要。
// 升级后不能仅凭相同 revision/digest 跳过快照，须先补齐地址版本。
func exitAddressVersionsApplied(engine *forwarding.Engine, snapshot *controlpb.ConfigSnapshot) bool {
	required := map[string]string{}
	for _, g := range snapshot.ExitGroups {
		for _, ep := range g.Exits {
			if ep.AddressVersion != "" {
				required[ep.NodeId] = ep.AddressVersion
			}
		}
	}
	if len(required) == 0 {
		return true
	}
	for _, g := range engine.State().ExitGroups {
		for _, ep := range g.Exits {
			if version, ok := required[ep.NodeId]; ok && version == ep.AddressVersion {
				delete(required, ep.NodeId)
			}
		}
	}
	return len(required) == 0
}
