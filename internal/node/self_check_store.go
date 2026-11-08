package node

import "context"

// SelfCheckStore 独立保存自测配置、序号及未确认结果，不复用事件确认。
type SelfCheckStore interface {
	LoadSelfChecks(context.Context) ([]byte, error)
	SaveSelfChecks(context.Context, []byte) error
}
