package clusterharness

import (
	"fmt"
	"google.golang.org/grpc"
	"nbpass/internal/controlpb"
	"sync/atomic"
)

// TransformControlReports injects wire faults without replacing the real node runtime.
// Passing nil restores unmodified delivery.
func (h *Harness) TransformControlReports(transform func(*controlpb.NodeReport)) {
	if transform == nil {
		h.controlReportTransform.Store(nil)
	} else {
		h.controlReportTransform.Store(&transform)
	}
}

// RepeatRotationCommands 在真实 gRPC 发送边界重复同一命令；节点仍为真实实现。
func (h *Harness) RepeatRotationCommands(enabled bool) { h.repeatRotationCommands.Store(enabled) }

// DropUpgradeCommands simulates a lost task packet before it reaches the updater.
func (h *Harness) DropUpgradeCommands(enabled bool) { h.dropUpgradeCommands.Store(enabled) }

type repeatedRotationStream struct {
	grpc.ServerStream
	h        *Harness
	protocol atomic.Uint32
}

func (s *repeatedRotationStream) RecvMsg(message any) error {
	if err := s.ServerStream.RecvMsg(message); err != nil {
		return err
	}
	if report, ok := message.(*controlpb.NodeReport); ok {
		if transform := s.h.controlReportTransform.Load(); transform != nil {
			(*transform)(report)
		}
		s.protocol.Store(report.ProtocolVersion)
	}
	return nil
}

func (s *repeatedRotationStream) SendMsg(message any) error {
	if m, ok := message.(*controlpb.PanelMessage); ok && m.UpgradeOnly && len(m.UpgradeManifest) > 0 && s.h.dropUpgradeCommands.Load() {
		return nil
	}
	if m, ok := message.(*controlpb.PanelMessage); ok && s.protocol.Load() != 0 && m.ProtocolVersion != s.protocol.Load() {
		return fmt.Errorf("回包控制协议 %d 与节点 %d 不符", m.ProtocolVersion, s.protocol.Load())
	}
	if err := s.ServerStream.SendMsg(message); err != nil {
		return err
	}
	if m, ok := message.(*controlpb.PanelMessage); ok && m.RotationCommand != nil && s.h.repeatRotationCommands.Load() {
		return s.ServerStream.SendMsg(message)
	}
	return nil
}

func (h *Harness) repeatControlDelivery(srv any, stream grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	return handler(srv, &repeatedRotationStream{ServerStream: stream, h: h})
}
