package panel

import "time"

// 展示状态描述现有证据，不作为新的自动处理决策输入。
type operationReason struct {
	Code    string     `json:"code"`
	Message string     `json:"message"`
	Source  string     `json:"source"`
	NodeID  string     `json:"node_id,omitempty"`
	At      *time.Time `json:"observed_at"`
}

type operationRule struct {
	IncidentID    string                  `json:"incident_id,omitempty"`
	Configuration string                  `json:"-"`
	ExitGroupID   string                  `json:"exit_group_id,omitempty"`
	Evidence      []operationRuleEvidence `json:"evidence"`
	ID            string                  `json:"id"`
	Name          string                  `json:"name"`
	GroupID       string                  `json:"group_id,omitempty"`
	NodeID        string                  `json:"node_id,omitempty"`
	Enabled       bool                    `json:"enabled"`
	Revision      uint64                  `json:"revision"`
	Status        string                  `json:"status"`
	Reasons       []operationReason       `json:"reasons"`
	Members       []string                `json:"members"`
}

type operationGroup struct {
	ID             string            `json:"id"`
	Name           string            `json:"name"`
	Members        []string          `json:"members"`
	DesiredNode    string            `json:"desired_node"`
	DesiredVersion int64             `json:"desired_version"`
	ExpectedIP     string            `json:"expected_ip"`
	ConfirmedIP    string            `json:"confirmed_ip"`
	ConfirmedNode  string            `json:"confirmed_node"`
	ConfirmedAt    *time.Time        `json:"confirmed_at"`
	Stage          string            `json:"stage"`
	Propagation    string            `json:"propagation"`
	WatchID        string            `json:"watch_id"`
	Redundancy     string            `json:"redundancy"`
	Reasons        []operationReason `json:"reasons"`
	Bound          bool              `json:"bound"`
}

type operationNode struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Status    string     `json:"status"`
	Connected bool       `json:"connected"`
	LastSeen  *time.Time `json:"last_seen"`
	Blocked   string     `json:"blocked"`
	Revision  uint64     `json:"revision"`
}

type operationCounts struct {
	Nodes           int  `json:"nodes"`
	Entry           int  `json:"entry"`
	Exit            int  `json:"exit"`
	Rules           int  `json:"rules"`
	EnabledRules    int  `json:"enabled_rules"`
	AbnormalRules   int  `json:"abnormal_rules"`
	UnknownRules    int  `json:"unknown_rules"`
	DegradedGroups  int  `json:"degraded_groups"`
	ManualIncidents *int `json:"manual_incidents"`
	Alerts          int  `json:"alerts"`
}

type operationsView struct {
	CurrentIncidents []incidentRecord `json:"current_incidents"`
	RecentIncidents  []incidentRecord `json:"recent_incidents"`
	GeneratedAt      time.Time        `json:"generated_at"`
	Complete         bool             `json:"complete"`
	Counts           operationCounts  `json:"counts"`
	Nodes            []operationNode  `json:"nodes"`
	Groups           []operationGroup `json:"groups"`
	Rules            []operationRule  `json:"rules"`
	IncidentSync     *time.Time       `json:"incident_sync"`
	IncidentError    string           `json:"incident_error"`
}

type operationRuleEvidence struct {
	NodeID  string     `json:"node_id"`
	Desired uint64     `json:"desired_revision"`
	Applied uint64     `json:"applied_revision"`
	At      *time.Time `json:"checked_at"`
}
