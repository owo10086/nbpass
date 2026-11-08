package clusterharness

import (
	"context"
	"nbpass/internal/probe"
)

// 测试台默认不读取宿主机资源；具体场景在 StartNode 中注入受控采集器。
type emptyProbe struct{}

func (emptyProbe) Collect(context.Context) (probe.Values, error) { return probe.Values{}, nil }
