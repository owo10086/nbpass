// Package timing 提供核心共享的时间依赖，业务期限不直接依赖墙上时钟。
package timing

import "time"

type Timer interface {
	C() <-chan time.Time
	Stop() bool
}

type Clock interface {
	Now() time.Time
	NewTimer(time.Duration) Timer
}
