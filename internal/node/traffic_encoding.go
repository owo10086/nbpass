package node

import (
	"encoding/json"
	"errors"
	"time"

	"nbpass/internal/controlpb"
)

// 持久格式单独版本化，重复的实例/规则 ID 只保存一次。
// 每条记录依次为实例索引、规则索引、分钟、序号、上行、下行、连接数、采样时间、确认标志。
type trafficDiskState struct {
	Version   int         `json:"version"`
	Sequence  uint64      `json:"sequence"`
	Instances []string    `json:"instances"`
	Rules     []string    `json:"rules"`
	Records   [][9]uint64 `json:"records"`
}

func encodeTraffic(s trafficState) ([]byte, error) {
	disk := trafficDiskState{Version: 1, Sequence: s.Sequence, Records: make([][9]uint64, 0, len(s.Entries))}
	instances, rules := map[string]uint64{}, map[string]uint64{}
	for _, e := range s.Entries {
		v := e.Sample
		instance, ok := instances[v.ProcessInstance]
		if !ok {
			instance = uint64(len(disk.Instances))
			instances[v.ProcessInstance] = instance
			disk.Instances = append(disk.Instances, v.ProcessInstance)
		}
		rule, ok := rules[v.RuleId]
		if !ok {
			rule = uint64(len(disk.Rules))
			rules[v.RuleId] = rule
			disk.Rules = append(disk.Rules, v.RuleId)
		}
		at, err := time.Parse(time.RFC3339Nano, v.SampledAt)
		if err != nil {
			return nil, err
		}
		var acked uint64
		if e.Acked {
			acked = 1
		}
		disk.Records = append(disk.Records, [9]uint64{instance, rule, uint64(v.BucketUnix), v.Sequence, v.UploadBytes, v.DownloadBytes, v.ActiveConnections, uint64(at.UnixNano()), acked})
	}
	return json.Marshal(disk)
}

func decodeTraffic(raw []byte) (trafficState, error) {
	var disk trafficDiskState
	if err := json.Unmarshal(raw, &disk); err != nil {
		return trafficState{}, err
	}
	if disk.Version != 1 {
		return trafficState{}, errors.New("流量缓存格式版本无效")
	}
	s := trafficState{Sequence: disk.Sequence, Entries: make(map[string]trafficEntry, len(disk.Records))}
	for _, r := range disk.Records {
		if r[0] >= uint64(len(disk.Instances)) || r[1] >= uint64(len(disk.Rules)) || r[3] == 0 || r[3] > s.Sequence || r[8] > 1 {
			return trafficState{}, errors.New("流量缓存损坏")
		}
		v := &controlpb.RuleTraffic{ProcessInstance: disk.Instances[r[0]], RuleId: disk.Rules[r[1]], BucketUnix: int64(r[2]), Sequence: r[3], UploadBytes: r[4], DownloadBytes: r[5], ActiveConnections: r[6], SampledAt: time.Unix(0, int64(r[7])).UTC().Format(time.RFC3339Nano)}
		key := trafficKey(v)
		if _, ok := s.Entries[key]; ok {
			return trafficState{}, errors.New("流量缓存包含重复桶")
		}
		s.Entries[key] = trafficEntry{Sample: v, Acked: r[8] == 1}
	}
	return s, nil
}
