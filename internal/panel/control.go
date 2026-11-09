package panel

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"nbpass/internal/controlpb"
	"nbpass/internal/publicip"
)

type controlSession struct {
	rotationRecheckCapable bool
	done                   chan struct{}
	once                   sync.Once
	instance               string
	changed                chan struct{}
	eventsLogged           bool
	dnsWatchCapable        bool
}

func (s *controlSession) stop() { s.once.Do(func() { close(s.done) }) }

type controlService struct {
	controlpb.UnimplementedControlServiceServer
	p *Panel
}

// RegisterControl 只注册明文 gRPC 服务；TLS 由部署边界负责。
func (p *Panel) RegisterControl(server grpc.ServiceRegistrar) {
	controlpb.RegisterControlServiceServer(server, &controlService{p: p})
}

func (s *controlService) Enroll(ctx context.Context, request *controlpb.EnrollRequest) (*controlpb.EnrollResponse, error) {
	if len(request.InstallationTicket) != 43 {
		return nil, status.Error(codes.Unauthenticated, "安装票据无效或过期")
	}
	p := s.p
	p.controlMu.Lock()
	defer p.controlMu.Unlock()
	token, digest, err := p.randomCredential()
	if err != nil {
		return nil, status.Error(codes.Unavailable, "无法创建节点凭据")
	}
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, status.Error(codes.Unavailable, "存储暂不可用")
	}
	defer tx.Rollback()
	ticketHash := sha256.Sum256([]byte(request.InstallationTicket))
	var id string
	// 消费票据与保存 token 必须在同一事务；失败时可安全重试，成功只返回一次。
	err = tx.QueryRowContext(ctx, "DELETE FROM installation_tickets WHERE ticket_hash = ? AND expires_at > ? RETURNING node_id", ticketHash[:], p.deps.Clock.Now().UnixNano()).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.Unauthenticated, "安装票据无效或过期")
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, "UPDATE nodes SET token_hash = ? WHERE id = ?", digest, id)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		return nil, status.Error(codes.Unavailable, "存储暂不可用")
	}
	return &controlpb.EnrollResponse{NodeId: id, Token: token}, nil
}

func tokenDigest(ctx context.Context) ([]byte, error) {
	values := metadata.ValueFromIncomingContext(ctx, "authorization")
	if len(values) != 1 || !strings.HasPrefix(values[0], "Bearer ") || len(values[0]) != 50 {
		return nil, status.Error(codes.Unauthenticated, "节点凭据无效")
	}
	digest := sha256.Sum256([]byte(strings.TrimPrefix(values[0], "Bearer ")))
	return digest[:], nil
}

type controlReport struct {
	report *controlpb.NodeReport
	err    error
}

func (s *controlService) Connect(stream grpc.BidiStreamingServer[controlpb.NodeReport, controlpb.PanelMessage]) error {
	digest, err := tokenDigest(stream.Context())
	if err != nil {
		return err
	}
	p := s.p
	session := &controlSession{done: make(chan struct{}), changed: make(chan struct{}, 1)}
	var id string
	p.controlMu.Lock()
	err = p.db.QueryRowContext(stream.Context(), "SELECT id FROM nodes WHERE token_hash = ?", digest).Scan(&id)
	if err == nil {
		if p.pendingControls[id] == nil {
			p.pendingControls[id] = make(map[*controlSession]struct{})
		}
		p.pendingControls[id][session] = struct{}{}
	}
	p.controlMu.Unlock()
	if errors.Is(err, sql.ErrNoRows) {
		return status.Error(codes.Unauthenticated, "节点凭据无效")
	}
	if err != nil {
		return status.Error(codes.Unavailable, "存储暂不可用")
	}
	defer func() {
		p.controlMu.Lock()
		delete(p.pendingControls[id], session)
		if len(p.pendingControls[id]) == 0 {
			delete(p.pendingControls, id)
		}
		p.controlMu.Unlock()
		session.stop()
	}()
	incoming := make(chan controlReport)
	go func() {
		for {
			report, err := stream.Recv()
			select {
			case incoming <- controlReport{report, err}:
			case <-session.done:
				return
			case <-stream.Context().Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	receive := func(d time.Duration) (*controlpb.NodeReport, error) {
		timer := p.deps.Clock.NewTimer(d)
		defer timer.Stop()
		select {
		case <-session.done:
			return nil, status.Error(codes.Unauthenticated, "控制会话已撤销或被新会话替代")
		case <-stream.Context().Done():
			return nil, stream.Context().Err()
		case <-timer.C():
			return nil, status.Error(codes.DeadlineExceeded, "节点活性超时")
		case message := <-incoming:
			return message.report, message.err
		}
	}
	first, err := receive(15 * time.Second)
	if err != nil {
		return err
	}
	if independentNodeReport(first) {
		return status.Error(codes.InvalidArgument, "首报告必须为活性报告")
	}
	if err := p.admitControl(stream.Context(), id, digest, session, first); err != nil {
		return err
	}
	defer func() {
		p.controlMu.Lock()
		defer p.controlMu.Unlock()
		if p.controls[id] == session {
			delete(p.controls, id)
			now := p.deps.Clock.Now()
			_, _ = p.db.Exec(`UPDATE nodes SET disconnected_at = CASE
				WHEN last_seen_at <= ? THEN last_seen_at ELSE ? END WHERE id = ?`, now.Add(-90*time.Second).UnixNano(), now.UnixNano(), id)
		}
	}()
	firstResponse, err := p.acceptReport(stream.Context(), id, digest, session, first)
	if err != nil {
		return err
	}
	send := func(ack *controlpb.PanelMessage, full bool) error {
		p.controlMu.Lock()
		err := p.validControl(stream.Context(), id, digest, session)
		response := ack
		if err == nil && full {
			response, err = p.controlMessage(stream.Context(), id)
			if response != nil && ack != nil {
				response.AcknowledgedEventIds = ack.AcknowledgedEventIds
				response.AcknowledgedSelfCheckIds = ack.AcknowledgedSelfCheckIds
				response.AcknowledgedCheckerIds = ack.AcknowledgedCheckerIds
			}
		}
		p.controlMu.Unlock()
		if err != nil {
			return err
		}
		sent := make(chan error, 1)
		timer := p.deps.Clock.NewTimer(15 * time.Second)
		defer timer.Stop()
		go func() {
			// 命令独立发送：不占快照空间，也不能阻塞后续完整配置/撤权同步。
			dnsTasks := response.DnsWatchTasks
			response.DnsWatchTasks = nil
			checkerTasks := response.CheckerTasks
			response.CheckerTasks = nil
			command := response.RotationCommand
			response.RotationCommand = nil
			err := stream.Send(response)
			if err == nil && command != nil {
				err = stream.Send(&controlpb.PanelMessage{ProtocolVersion: 1, DesiredRevision: response.DesiredRevision, RotationCommand: command})
			}
			if err == nil && len(dnsTasks) > 0 {
				err = stream.Send(&controlpb.PanelMessage{ProtocolVersion: 1, DnsWatchTasks: dnsTasks})
			}
			if err == nil && len(checkerTasks) > 0 {
				err = stream.Send(&controlpb.PanelMessage{ProtocolVersion: 1, CheckerTasks: checkerTasks})
			}
			sent <- err
		}()
		select {
		case <-session.done:
			return status.Error(codes.Unauthenticated, "控制会话已撤销")
		case <-stream.Context().Done():
			return stream.Context().Err()
		case <-timer.C():
			return status.Error(codes.DeadlineExceeded, "控制发送超时")
		case err := <-sent:
			return err
		}
	}
	if err := send(firstResponse, true); err != nil {
		return err
	}
	syncTimer := p.deps.Clock.NewTimer(60 * time.Second)
	defer func() { syncTimer.Stop() }()
	alive := p.deps.Clock.NewTimer(90 * time.Second)
	defer func() { alive.Stop() }()
	for {
		select {
		case <-session.done:
			return status.Error(codes.Unauthenticated, "控制会话已撤销")
		case <-stream.Context().Done():
			return stream.Context().Err()
		case <-alive.C():
			return status.Error(codes.DeadlineExceeded, "节点活性超时")
		case <-syncTimer.C():
			if err := send(nil, true); err != nil {
				return err
			}
			syncTimer = p.deps.Clock.NewTimer(60 * time.Second)
		case <-session.changed:
			if err := send(nil, true); err != nil {
				return err
			}
		case message := <-incoming:
			if message.err != nil {
				return message.err
			}
			response, err := p.acceptReport(stream.Context(), id, digest, session, message.report)
			if err != nil {
				return err
			}
			report := message.report
			heartbeat := !independentNodeReport(report) && report.AttemptedRevision == 0
			if heartbeat {
				alive.Stop()
				alive = p.deps.Clock.NewTimer(90 * time.Second)
			}
			// 配置确认没有回包；探针/事件回包不附配置，避免相互触发同步。
			if heartbeat || independentNodeReport(report) || len(response.AcknowledgedEventIds) > 0 {
				if err := send(response, heartbeat); err != nil {
					return err
				}
			}
		}
	}
}

// 独立上报不能续期活性、附带配置或打开跨节点授权门禁。
func independentNodeReport(report *controlpb.NodeReport) bool {
	return report.ProbeOnly || report.EventsOnly || report.RotationOnly || report.SelfCheckOnly || report.DnsWatchOnly || report.TrafficOnly || report.CheckerOnly
}

func validateReport(report *controlpb.NodeReport) error {
	if report.ProtocolVersion != 1 {
		return status.Error(codes.FailedPrecondition, "控制协议不兼容")
	}
	if len(report.ProcessInstance) == 0 || len(report.ProcessInstance) > 128 || len(report.SoftwareVersion) > 128 {
		return status.Error(codes.InvalidArgument, "节点报告无效")
	}
	if !report.TrafficOnly && report.PublicIpv4 != "" {
		if ip, err := publicip.Parse(report.PublicIpv4); err != nil || ip != report.PublicIpv4 {
			return status.Error(codes.InvalidArgument, "节点公网 IPv4 无效")
		}
	}
	return nil
}

// 首报告通过后才替换已有会话；已被替代的进程实例不能重连夺回控制权。
func (p *Panel) admitControl(ctx context.Context, id string, digest []byte, session *controlSession, first *controlpb.NodeReport) error {
	if err := validateReport(first); err != nil {
		return err
	}
	p.controlMu.Lock()
	defer p.controlMu.Unlock()
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return status.Error(codes.Unavailable, "存储暂不可用")
	}
	defer tx.Rollback()
	var previous string
	err = tx.QueryRowContext(ctx, "SELECT process_instance FROM nodes WHERE id = ? AND token_hash = ?", id, digest).Scan(&previous)
	if errors.Is(err, sql.ErrNoRows) {
		return status.Error(codes.Unauthenticated, "节点凭据无效")
	}
	if err != nil {
		return status.Error(codes.Unavailable, "存储暂不可用")
	}
	var retired int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM retired_control_instances WHERE node_id = ? AND process_instance = ?", id, first.ProcessInstance).Scan(&retired); err != nil {
		return status.Error(codes.Unavailable, "存储暂不可用")
	}
	if retired != 0 {
		return status.Error(codes.FailedPrecondition, "旧进程实例已被替代")
	}
	if first.TunnelRotationVersion != 1 {
		var activeRotations int
		err := tx.QueryRowContext(ctx, `SELECT count(*) FROM tunnel_rotations r WHERE r.phase<>'complete'
         AND (r.exit_id=? OR EXISTS(SELECT 1 FROM tunnel_rotation_pairs p WHERE p.exit_id=r.exit_id AND p.entry_id=?))`, id, id).Scan(&activeRotations)
		if err != nil {
			return status.Error(codes.Unavailable, "存储暂不可用")
		}
		if activeRotations > 0 {
			return status.Error(codes.FailedPrecondition, "进行中的出口轮换需要支持轮换的节点版本")
		}
	}
	if previous != "" && previous != first.ProcessInstance {
		_, err = tx.ExecContext(ctx, "INSERT OR IGNORE INTO retired_control_instances(node_id, process_instance) VALUES(?, ?)", id, previous)
	}
	if err == nil {
		if previous != first.ProcessInstance {
			_, err = tx.ExecContext(ctx, "UPDATE nodes SET process_instance=?,applied_revision=0,applied_digest='',attempted_revision=0,apply_error='' WHERE id=?", first.ProcessInstance, id)
			if err == nil {
				_, err = tx.ExecContext(ctx, "DELETE FROM node_rule_reports WHERE node_id=?", id)
			}
			// 保留历史实际规则以展示未完成的删除/移组撤销；清除报告确认已使其失效。
		} else {
			_, err = tx.ExecContext(ctx, "UPDATE nodes SET process_instance=? WHERE id=?", first.ProcessInstance, id)
		}
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, "UPDATE nodes SET tunnel_rotation_version=? WHERE id=?", first.TunnelRotationVersion, id)
	}
	var authorizationChanges []string
	var failoverChanged bool
	if err == nil {
		failoverChanged, err = p.admitExitFailover(ctx, tx, id, first.ExitFailoverVersion)
	}
	if err == nil {
		authorizationChanges, err = p.admitTunnelAuthorization(ctx, tx, id, previous != first.ProcessInstance, first.TunnelAuthorizationVersion, failoverChanged)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		return status.Error(codes.Unavailable, "存储暂不可用")
	}
	session.dnsWatchCapable = first.DnsWatchCapable
	session.instance = first.ProcessInstance
	delete(p.pendingControls[id], session)
	if len(p.pendingControls[id]) == 0 {
		delete(p.pendingControls, id)
	}
	if old := p.controls[id]; old != nil {
		old.stop()
	}
	p.controls[id] = session
	p.notifyConfiguration(authorizationChanges)
	return nil
}

func (p *Panel) validControl(ctx context.Context, id string, digest []byte, session *controlSession) error {
	if p.controls[id] != session {
		return status.Error(codes.Unauthenticated, "控制会话已失效")
	}
	var exists int
	err := p.db.QueryRowContext(ctx, "SELECT 1 FROM nodes WHERE id = ? AND token_hash = ?", id, digest).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return status.Error(codes.Unauthenticated, "节点凭据已撤销")
	}
	if err != nil {
		return status.Error(codes.Unavailable, "存储暂不可用")
	}
	return nil
}

func (p *Panel) acceptReport(ctx context.Context, id string, digest []byte, session *controlSession, report *controlpb.NodeReport) (*controlpb.PanelMessage, error) {
	p.controlMu.Lock()
	defer p.controlMu.Unlock()
	if err := p.validControl(ctx, id, digest, session); err != nil {
		return nil, err
	}
	if err := validateReport(report); err != nil {
		return nil, err
	}
	if session.instance != "" && session.instance != report.ProcessInstance {
		return nil, status.Error(codes.FailedPrecondition, "旧进程实例报告无效")
	}
	session.instance = report.ProcessInstance
	if report.CheckerOnly {
		ack, err := p.acceptCheckerResults(ctx, id, report.CheckerResults)
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, "检测点结果无效")
		}
		return &controlpb.PanelMessage{ProtocolVersion: 1, AcknowledgedCheckerIds: ack}, nil
	}
	if report.SelfCheckOnly {
		ack, err := p.acceptSelfChecks(ctx, id, report.SelfCheckResults)
		if err != nil {
			return nil, status.Error(codes.Unavailable, "自测结果无法保存")
		}
		return &controlpb.PanelMessage{ProtocolVersion: 1, AcknowledgedSelfCheckIds: ack}, nil
	}
	if report.DnsWatchOnly {
		if err := p.acceptDNSWatchResults(ctx, id, session.instance, report.DnsWatchResults); err != nil {
			return nil, status.Error(codes.InvalidArgument, "解析采样无法保存")
		}
		return &controlpb.PanelMessage{ProtocolVersion: 1}, nil
	}
	if report.TrafficOnly {
		return p.acceptTraffic(ctx, id, session.instance, report)
	}
	if report.RotationOnly {
		if err := p.acceptRotation(ctx, id, report.Rotation); err != nil {
			return nil, status.Error(codes.InvalidArgument, "换 IP 进度无法保存")
		}
		return &controlpb.PanelMessage{ProtocolVersion: 1}, nil
	}
	if report.ProbeOnly || !independentNodeReport(report) && report.AttemptedRevision == 0 {
		if err := p.storeProbe(ctx, id, session.instance, report.Probe); err != nil {
			return nil, status.Error(codes.Unavailable, "探针存储暂不可用")
		}
	}
	acked, err := p.acceptEvents(ctx, id, session, report)
	if err != nil {
		return nil, err
	}
	var desired, applied, attempted int64
	var appliedDigest, applyError string
	if err := p.db.QueryRowContext(ctx, "SELECT desired_revision,applied_revision,applied_digest,attempted_revision,apply_error FROM nodes WHERE id=?", id).Scan(&desired, &applied, &appliedDigest, &attempted, &applyError); err != nil {
		return nil, status.Error(codes.Unavailable, "存储暂不可用")
	}
	response := &controlpb.PanelMessage{ProtocolVersion: 1, DesiredRevision: uint64(desired), AcknowledgedEventIds: acked}
	// 独立探针/事件只消费自己的载荷，不能确认或回退配置结果。
	if independentNodeReport(report) {
		return response, nil
	}
	heartbeat := !independentNodeReport(report) && report.AttemptedRevision == 0
	if heartbeat {
		session.rotationRecheckCapable = report.RotationRecheckCapable
		if err := p.recordNodeIP(ctx, id, report.PublicIpv4); err != nil {
			return nil, status.Error(codes.Unavailable, "存储暂不可用")
		}
		if err := p.recordHeartbeat(ctx, id, report.SoftwareVersion, digest); err != nil {
			return nil, status.Error(codes.Unavailable, "存储暂不可用")
		}
	}
	// 只有当前期望版本与摘要的确认才更新应用结果；旧版本只证明活性。
	var expectedDigest string
	err = p.db.QueryRowContext(ctx, "SELECT digest FROM node_snapshots WHERE node_id=?", id).Scan(&expectedDigest)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.Unavailable, "存储暂不可用")
	}
	alreadyConfirmed := applied == desired
	if desired > 0 && expectedDigest != "" {
		if report.AppliedRevision == uint64(desired) && report.AppliedDigest == expectedDigest && report.ApplyError == "" {
			applied, appliedDigest, attempted, applyError = desired, expectedDigest, desired, ""
		} else if report.AttemptedRevision == uint64(desired) && (report.AttemptedDigest == "" || report.AttemptedDigest == expectedDigest) && applied != desired && report.ApplyError != "" {
			attempted, applyError = desired, report.ApplyError
			if len(applyError) > 1024 {
				applyError = applyError[:1024]
			}
		}
	}
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, status.Error(codes.Unavailable, "存储暂不可用")
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `UPDATE nodes SET applied_revision=?,applied_digest=?,attempted_revision=?,apply_error=? WHERE id=? AND token_hash=?`,
		applied, appliedDigest, attempted, applyError, id, digest)
	if err == nil {
		err = p.storeRuleResults(ctx, tx, id, report, uint64(desired), expectedDigest, alreadyConfirmed)
	}
	var authorizationChanges []string
	if err == nil {
		err = p.confirmExitAddresses(ctx, tx, id, report, uint64(desired), expectedDigest)
	}
	if err == nil {
		authorizationChanges, err = p.confirmTunnelAuthorizations(ctx, tx, id, report, uint64(desired), expectedDigest)
	}
	if err == nil {
		var rotationChanges []string
		rotationChanges, err = p.confirmTunnelRotation(ctx, tx, id, report, uint64(desired), expectedDigest)
		authorizationChanges = append(authorizationChanges, rotationChanges...)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		if status.Code(err) == codes.InvalidArgument {
			return nil, err
		}
		return nil, status.Error(codes.Unavailable, "存储暂不可用")
	}
	// 确认与入口快照一并提交后立即通知，不依赖心跳重试。
	p.notifyConfiguration(authorizationChanges)
	return response, nil
}
