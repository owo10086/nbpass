package forwarding

import (
	"fmt"
	"net"
	"strconv"
)

// 检测监听只完成 TCP 握手，立即关闭，不承载业务数据。
func (e *Engine) effectiveCheckPort(port uint32) uint32 {
	if port == 0 {
		return e.deps.DefaultCheckPort
	}
	return port
}

func (e *Engine) ListenCheckPort(port uint32) error {
	e.checkConfigured = true
	if e.checkListener != nil && e.checkPort == port {
		return nil
	}
	if e.checkListener != nil {
		e.checkListener.Close()
		e.checkListener = nil
	}
	e.checkPort = 0
	e.checkError = ""
	if port == 0 {
		return nil
	}
	listen := e.deps.CheckListen
	if listen == nil {
		listen = net.Listen
	}
	l, err := listen("tcp4", net.JoinHostPort("0.0.0.0", strconv.Itoa(int(port))))
	if err != nil {
		e.checkError = ErrorSummary(fmt.Sprintf("检测端口监听失败: %v", err), 1024)
		return fmt.Errorf("%s", e.checkError)
	}
	e.checkListener = l
	e.checkPort = port
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	return nil
}
