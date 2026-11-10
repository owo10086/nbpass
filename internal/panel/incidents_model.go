package panel

import "time"

type incidentRef struct {
	Kind   string `json:"kind"`
	ID     string `json:"id"`
	Name   string `json:"name"`
	Exists bool   `json:"exists"`
}
type incidentEntry struct {
	Sequence   int64      `json:"sequence"`
	Key        string     `json:"-"`
	Stage      string     `json:"stage"`
	Message    string     `json:"message"`
	Source     string     `json:"source"`
	Version    int64      `json:"version"`
	At         *time.Time `json:"occurred_at"`
	ObservedAt time.Time  `json:"observed_at"`
}
type incidentRecord struct {
	ID        string        `json:"id"`
	Kind      string        `json:"kind"`
	Title     string        `json:"title"`
	Impact    string        `json:"impact"`
	Handling  string        `json:"handling"`
	StartedAt time.Time     `json:"started_at"`
	UpdatedAt time.Time     `json:"updated_at"`
	EndedAt   *time.Time    `json:"ended_at"`
	Closure   string        `json:"closure"`
	Refs      []incidentRef `json:"refs"`
	Reasons   []string      `json:"reasons"`
	sourceKey string
}
type incidentObservation struct {
	ExistingOnly bool
	Pending      bool

	Key      string
	Kind     string
	Title    string
	Impact   string
	Handling string
	Closure  string
	Refs     []incidentRef
	Reasons  []string
	Entries  []incidentEntry
}
