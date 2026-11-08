package platform

import (
	"time"

	"nbpass/internal/timing"
)

type RealClock struct{}

func (RealClock) Now() time.Time { return time.Now() }

type realTimer struct{ *time.Timer }

func (t realTimer) C() <-chan time.Time                 { return t.Timer.C }
func (RealClock) NewTimer(d time.Duration) timing.Timer { return realTimer{time.NewTimer(d)} }
