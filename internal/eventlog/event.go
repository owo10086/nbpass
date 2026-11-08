// Package eventlog 定义面板与节点共用的事件契约；消息只应包含可展示的诊断信息。
package eventlog

import (
	"errors"
	"regexp"
	"time"
	"unicode/utf8"

	"nbpass/internal/controlpb"
)

const MaxBatch = 16
const BackfillWindow = 24 * time.Hour

var eventType = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

type Gap struct {
	Count  uint64    `json:"count"`
	From   time.Time `json:"from"`
	To     time.Time `json:"to"`
	Reason string    `json:"reason"`
}

type Event struct {
	ID             string    `json:"event_id"`
	Type           string    `json:"type"`
	OccurredAt     time.Time `json:"occurred_at"`
	ConfigRevision uint64    `json:"config_revision"`
	Message        string    `json:"message"`
	Gap            *Gap      `json:"gap,omitempty"`
}

func (e Event) Validate() error {
	if len(e.ID) == 0 || len(e.ID) > 128 || !utf8.ValidString(e.ID) || !eventType.MatchString(e.Type) || len(e.Message) > 2048 || !utf8.ValidString(e.Message) || e.OccurredAt.IsZero() || e.ConfigRevision > 1<<63-1 {
		return errors.New("事件字段无效或过大")
	}
	if e.Type == "event_gap" {
		if e.Gap == nil || e.Gap.Count == 0 || e.Gap.From.IsZero() || e.Gap.To.Before(e.Gap.From) || (e.Gap.Reason != "overflow" && e.Gap.Reason != "expired") {
			return errors.New("事件缺口无效")
		}
	} else if e.Gap != nil {
		return errors.New("普通事件不能包含缺口")
	}
	return nil
}

func (e Event) Proto() *controlpb.Event {
	p := &controlpb.Event{EventId: e.ID, Type: e.Type, OccurredAt: e.OccurredAt.UTC().Format(time.RFC3339Nano), ConfigRevision: e.ConfigRevision, Message: e.Message}
	if e.Gap != nil {
		p.Gap = &controlpb.EventGap{Count: e.Gap.Count, From: e.Gap.From.UTC().Format(time.RFC3339Nano), To: e.Gap.To.UTC().Format(time.RFC3339Nano), Reason: e.Gap.Reason}
	}
	return p
}

func FromProto(p *controlpb.Event) (Event, error) {
	if p == nil {
		return Event{}, errors.New("事件为空")
	}
	e := Event{ID: p.EventId, Type: p.Type, ConfigRevision: p.ConfigRevision, Message: p.Message}
	var err error
	if e.OccurredAt, err = time.Parse(time.RFC3339Nano, p.OccurredAt); err != nil {
		return e, err
	}
	if p.Gap != nil {
		e.Gap = &Gap{Count: p.Gap.Count, Reason: p.Gap.Reason}
		if e.Gap.From, err = time.Parse(time.RFC3339Nano, p.Gap.From); err != nil {
			return e, err
		}
		if e.Gap.To, err = time.Parse(time.RFC3339Nano, p.Gap.To); err != nil {
			return e, err
		}
	}
	return e, e.Validate()
}
