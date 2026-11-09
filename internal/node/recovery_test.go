package node

import (
	"context"
	"crypto/rand"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"nbpass/internal/controlpb"
	"nbpass/internal/platform"
)

func TestRecoveryHistoryStartsBeforeFirstActionAndSurvivesRestart(t *testing.T) {
	deps := Dependencies{Clock: platform.RealClock{}, RotationStore: FileStore{Dir: filepath.Join(t.TempDir(), "node"), Random: rand.Reader}}
	r, err := openRotations(context.Background(), deps)
	if err != nil {
		t.Fatal(err)
	}
	first := r.recoveryReport(42, time.Now())
	r, err = openRotations(context.Background(), deps)
	if err != nil {
		t.Fatal(err)
	}
	second := r.recoveryReport(43, time.Now())
	if first.HistorySince == 0 || second.HistorySince != first.HistorySince || second.HighWater != 43 {
		t.Fatal("空动作历史未持久化覆盖范围", first, second)
	}
}

func TestRecoveryReportBoundsRecordsAndAdvertisesGap(t *testing.T) {
	now := time.Now()
	r := &rotationRunner{deps: Dependencies{Clock: platform.RealClock{}}, state: rotationJournal{HistorySince: now.Add(-24 * time.Hour).UnixNano(), Results: map[string]*controlpb.RotationProgress{}, AttemptedAt: map[string]int64{}}}
	for i := range 130 {
		id := fmt.Sprint(i)
		r.state.AttemptedAt[id] = now.Add(time.Duration(i-130) * time.Second).UnixNano()
		r.state.Results[id] = &controlpb.RotationProgress{ActionId: id, State: "changed", OldIp: "8.8.8.8", NewIp: "1.1.1.1", Sequence: 2}
	}
	report := r.recoveryReport(99, time.Now())
	if len(report.Actions) != 128 || proto.Size(report) > 48<<10 || report.HistorySince != now.Add(-129*time.Second).UnixNano()+1 {
		t.Fatal("截断历史没有明确缺口", len(report.Actions), report.HistorySince)
	}
}

func TestRecoveryAndEventBackfillFitControlMessageTogether(t *testing.T) {
	report := &controlpb.NodeReport{Recovery: &controlpb.RecoveryReport{HighWater: 999, HistorySince: 1, ObservedAt: 1000}}
	for i := range 16 {
		report.Events = append(report.Events, &controlpb.Event{EventId: fmt.Sprint(i), Message: strings.Repeat("e", 2048)})
	}
	for i := range 20 {
		report.Recovery.Actions = append(report.Recovery.Actions, &controlpb.RecoveryRotation{ActionId: fmt.Sprint(i), AttemptedAt: int64(i + 10), Progress: &controlpb.RotationProgress{RecheckRounds: [][]byte{[]byte(strings.Repeat("r", 2048))}}})
	}
	limitRecoveryReport(report)
	if proto.Size(report) > 60<<10 || report.Recovery.HistorySince <= 1 || len(report.Events) != 16 {
		t.Fatal("恢复补报挤爆控制消息或丢失事件确认")
	}
}
