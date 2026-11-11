import { Alert, Button, Descriptions, Drawer, Space, Tabs, Typography } from 'antd';
import { CloudServerOutlined, CodeOutlined, DashboardOutlined, GlobalOutlined } from '@ant-design/icons';
import { ColorTag } from './ColorTag';
import { FormSection } from './FormSection';
import { StatusDot } from './StatusDot';
import { ConnectionsCell, CpuCell, MemoryCell, NetworkCell, uptime } from './NodeMetrics';
import { format } from './ProbeDetails';
import { NodeUpgrade } from './NodeUpgrade';
import { serverGroupsFor, type ServerGroup, type ServerRow } from './NodeServerData';
import './servers.css';

export type ServerAction = 'edit' | 'region' | 'record' | 'rotation' | 'maintenance' | 'ports' | 'delete' | 'install' | 'revoke' | 'probe' | 'self-check' | 'upgrade' | 'details';
export type ServerDetailTab = 'overview' | 'upgrade';

export function serverTone(node: Pick<ServerRow, 'status' | 'connected'>) {
  if (node.status === 'online') return node.connected ? 'ok' : 'danger';
  if (node.status === 'offline') return 'danger';
  if (node.status === 'maintenance') return 'warn';
  return 'muted';
}

export function NodeServerDetails({ node, groups, initialTab, error, onClose, onAction, onSaved, onError }: {
  node: ServerRow; groups: ServerGroup[]; initialTab: ServerDetailTab; error: string; onClose: () => void; onAction: (key: ServerAction) => void; onSaved: () => Promise<void>; onError: (error: unknown) => void;
}) {
  const memberships = serverGroupsFor(node.id, groups);
  return <Drawer open title={`${node.name} · 服务器详情`} size={760} onClose={onClose} className="servers-drawer">
    {error && <Alert type="error" showIcon title={error} className="message" />}
    <Tabs defaultActiveKey={initialTab} items={[
      { key: 'overview', label: '服务器概况', children: <>
        <FormSection title="基本信息" icon={<CloudServerOutlined />}>
          <Descriptions column={1} size="small" items={[
            { key: 'id', label: '服务器 ID', children: <Typography.Text className="mono servers-full-id" copyable>{node.id}</Typography.Text> },
            { key: 'status', label: '状态', children: <Space wrap><StatusDot tone={serverTone(node)}>{node.status_label}</StatusDot><span className="muted">{node.connected ? '控制连接正常' : '控制连接已中断'}</span></Space> },
            { key: 'maintenance', label: '维护截止', children: node.maintenance_until ? new Date(node.maintenance_until).toLocaleString() : '未开启维护' },
            { key: 'group', label: '服务器组', children: <Space wrap>{memberships.length ? memberships.map(group => <ColorTag key={group.id}>{group.name}</ColorTag>) : <span className="muted">未分组</span>}</Space> },
            { key: 'seen', label: '最近活性', children: node.last_seen_at ? new Date(node.last_seen_at).toLocaleString() : '尚未接入' },
            { key: 'uptime', label: '进程运行时长', children: node.probe?.uptime_seconds == null ? '—' : uptime(node.probe.uptime_seconds) },
          ]} />
          <Space wrap className="servers-section-actions"><Button onClick={() => onAction('edit')}>编辑服务器</Button><Button onClick={() => onAction('self-check')}>自检与被墙认定</Button><Button onClick={() => onAction('maintenance')}>{node.status === 'maintenance' ? '结束维护' : '维护 10 分钟'}</Button></Space>
        </FormSection>
        <FormSection title="地区与入口地址" icon={<GlobalOutlined />}>
          <Descriptions column={1} size="small" items={[
            { key: 'ip', label: '上报 IPv4', children: <span className="mono">{node.public_ipv4 || '尚未查询到 IP'}</span> },
            { key: 'confirmed', label: '确认 IPv4', children: <span className="mono">{node.confirmed_ipv4 || '地址待确认'}</span> },
            { key: 'region', label: '地区', children: `${node.region.name}（${node.region_source === 'manual' ? '人工覆盖' : '自动识别'}）` },
            { key: 'automatic-region', label: '自动识别地区', children: node.automatic_region.name },
            { key: 'checker', label: '国内检测点', children: <StatusDot tone={node.domestic_checker ? 'ok' : 'muted'}>{node.checker_label}</StatusDot> },
            { key: 'domain', label: '受管域名', children: node.managed_record?.hostname || '尚未绑定' },
            { key: 'record', label: '记录状态', children: node.managed_record ? `${node.managed_record.current_enabled ? '启用' : '暂停'} · ${node.record_status}` : '—' },
            { key: 'record-ip', label: '记录当前地址', children: <span className="mono">{node.managed_record?.current_value || '—'}</span> },
          ]} />
          {node.public_ipv4 && node.public_ipv4 !== node.confirmed_ipv4 && <p className="servers-warning">上报地址与确认地址不一致，请在受管记录中核对并同步地址。</p>}
          {node.managed_record && node.confirmed_ipv4 && node.managed_record.current_value !== node.confirmed_ipv4 && <p className="servers-warning">记录地址与确认地址不一致，等待同步。</p>}
          <Space wrap className="servers-section-actions"><Button onClick={() => onAction('region')}>地区与检测点</Button><Button onClick={() => onAction('record')}>受管记录与地址</Button><Button onClick={() => onAction('rotation')}>换 IP / 自动轮换</Button></Space>
        </FormSection>
        <FormSection title="实时资源" icon={<DashboardOutlined />}>
          <Descriptions column={{ xs: 1, sm: 2 }} size="small" items={[
            { key: 'cpu', label: 'CPU', children: <CpuCell probe={node.probe} /> },
            { key: 'memory', label: '内存', children: <MemoryCell probe={node.probe} /> },
            { key: 'network', label: '网络速率', children: <NetworkCell probe={node.probe} /> },
            { key: 'connections', label: '业务连接', children: <ConnectionsCell probe={node.probe} /> },
            { key: 'disk', label: '根文件系统', children: <span className="num">{format(node.probe?.disk_used_bytes, 'B')} / {format(node.probe?.disk_total_bytes, 'B')}</span> },
          ]} />
          <Button className="servers-section-actions" onClick={() => onAction('probe')}>探针指标与 24 小时曲线</Button>
        </FormSection>
        <FormSection title="配置同步" icon={<CodeOutlined />}>
          <Descriptions column={1} size="small" items={[
            { key: 'status', label: '配置状态', children: <StatusDot tone={node.config_status === '正常' ? 'ok' : node.apply_error ? 'danger' : 'warn'}>{node.config_status}</StatusDot> },
            { key: 'revision', label: '配置版本', children: <span className="mono">已应用 {node.applied_revision} / 期望 {node.desired_revision}</span> },
          ]} />
          {node.apply_error && <p className="servers-warning">{node.apply_error}</p>}
          <Button className="servers-section-actions" onClick={() => onAction('ports')}>监听端口设置</Button>
        </FormSection>
      </> },
      { key: 'upgrade', label: '版本与升级', children: <FormSection title="程序版本" icon={<CodeOutlined />} description="升级状态随服务器列表每 10 秒刷新。离线服务器恢复控制连接后才能升级。">
        <Descriptions column={1} size="small" items={[
          { key: 'current', label: '服务器版本', children: <span className="mono">{node.software_version || '尚未接入'}</span> },
          { key: 'panel', label: '面板版本', children: <span className="mono">{node.panel_version}</span> },
        ]} />
        <NodeUpgrade node={node} onSaved={onSaved} onError={onError} />
      </FormSection> },
    ]} />
  </Drawer>;
}
