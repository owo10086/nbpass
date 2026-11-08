//go:build !linux

package probe

import (
	"context"
	"nbpass/internal/timing"
)

type unsupportedSystem struct{}

// 生产部署平台为 Linux；其他平台保持缺测，不伪造 OS 数值。
func NewSystem(timing.Clock) Collector                            { return unsupportedSystem{} }
func (unsupportedSystem) Collect(context.Context) (Values, error) { return Values{}, nil }
