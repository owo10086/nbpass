package clusterharness

import (
	"google.golang.org/grpc"
	"nbpass/internal/controlpb"
)

// RepeatRotationCommands 在真实 gRPC 发送边界重复同一命令；节点仍为真实实现。
func (h *Harness) RepeatRotationCommands(enabled bool) { h.repeatRotationCommands.Store(enabled) }

type repeatedRotationStream struct {
	grpc.ServerStream
	h *Harness
}

func (s repeatedRotationStream) SendMsg(message any) error {
	if err := s.ServerStream.SendMsg(message); err != nil {
		return err
	}
	if m, ok := message.(*controlpb.PanelMessage); ok && m.RotationCommand != nil && s.h.repeatRotationCommands.Load() {
		return s.ServerStream.SendMsg(message)
	}
	return nil
}

func (h *Harness) repeatControlDelivery(srv any, stream grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	return handler(srv, repeatedRotationStream{ServerStream: stream, h: h})
}
