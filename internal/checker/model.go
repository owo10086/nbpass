// Package checker 定义国内检测点任务和同轮原始证据。
package checker

import "time"

type Task struct {
	ID             string    `json:"id"`
	RoundID        string    `json:"round_id"`
	TargetID       string    `json:"target_id"`
	CheckerID      string    `json:"checker_id"`
	TargetIP       string    `json:"target_ip"`
	TargetPort     int       `json:"target_port"`
	ConfigRevision uint64    `json:"config_revision"`
	ControlHost    string    `json:"control_host"`
	IssuedAt       time.Time `json:"issued_at"`
	Deadline       time.Time `json:"deadline"`
}
type Connection struct {
	Address    string `json:"address"`
	Connected  bool   `json:"connected"`
	Error      string `json:"error,omitempty"`
	DurationMS int64  `json:"duration_ms"`
}
type Result struct {
	Task           Task        `json:"task"`
	StartedAt      time.Time   `json:"started_at"`
	FinishedAt     time.Time   `json:"finished_at"`
	Control        Connection  `json:"control"`
	Target         *Connection `json:"target,omitempty"`
	ExecutionError string      `json:"execution_error,omitempty"`
}
