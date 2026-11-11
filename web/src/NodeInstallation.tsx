import { useEffect, useState } from 'react';
import { Alert, Button, Modal, Popconfirm, Space, Typography } from 'antd';
import { CodeOutlined, CopyOutlined, CheckOutlined, ReloadOutlined } from '@ant-design/icons';
import { api, APIError } from './api';
import { ColorTag } from './ColorTag';
import { FormSection } from './FormSection';
import './servers.css';

export type Installation = { id: string; installation_command: string; ticket_expires_at: string };

// 安装票据只在创建或重置时返回；打开弹窗本身不重置凭据，也不缓存到浏览器存储。
export function NodeInstallation({ node, initialTicket, onClose, onSaved, onError }: {
  node: { id: string; name: string }; initialTicket?: Installation; onClose: () => void; onSaved: () => Promise<void>; onError: (error: unknown) => void;
}) {
  const [ticket, setTicket] = useState(initialTicket);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [now, setNow] = useState(Date.now());
  useEffect(() => {
    const timer = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(timer);
  }, []);
  const expired = !!ticket && now >= Date.parse(ticket.ticket_expires_at);

  async function generate() {
    setBusy(true); setError('');
    try {
      setTicket(await api<Installation>(`/nodes/${node.id}/token/reset`, 'POST'));
      setNow(Date.now());
      await onSaved();
    } catch (error) {
      if (error instanceof APIError && (error.status === 401 || error.status === 428)) onError(error);
      else setError((error as Error).message);
    } finally { setBusy(false); }
  }

  return <Modal open title="安装脚本" width={800} onCancel={onClose} className="servers-modal servers-install" footer={<Button type="primary" onClick={onClose}>完成</Button>}>
    {error && <Alert type="error" showIcon title={error} className="message" />}
    <Alert type={expired ? 'warning' : 'info'} showIcon title={`${node.name} · 服务器安装`} description={ticket
      ? expired ? '安装票据已过期，请重新生成后再安装。' : `安装票据只能使用一次，有效至 ${new Date(ticket.ticket_expires_at).toLocaleString()}（生成后 30 分钟）。`
      : '生成安装脚本会重置本服务器凭据，断开当前控制连接并使旧凭据、旧安装票据失效。请准备好在目标服务器重新执行安装命令。'} />
    <div className="servers-install-platform"><span className="muted">适用系统</span><ColorTag>Linux</ColorTag><ColorTag>amd64</ColorTag></div>
    <FormSection title="安装命令" icon={<CodeOutlined />} description="以 root 身份在目标服务器终端执行。非 root 用户请先执行 sudo -i，安装后回到列表确认在线状态。">
      {ticket && !expired ? <pre className="servers-command" tabIndex={0} aria-label="服务器安装命令"><code>{ticket.installation_command}</code></pre>
        : <div className="servers-command servers-command-empty">{expired ? '票据已过期，请重新生成安装命令。' : '生成后可在此复制安装命令。'}</div>}
      <Space wrap className="servers-install-actions">
        {ticket && !expired && <Typography.Paragraph className="servers-install-copy" copyable={{ text: ticket.installation_command, tooltips: ['复制安装命令', '已复制'], icon: [<span key="copy"><CopyOutlined /> 复制安装命令</span>, <span key="copied"><CheckOutlined /> 已复制</span>] }} />}
        <Popconfirm title={ticket ? '重新生成安装脚本？' : '重置凭据并生成安装脚本？'} description="当前控制连接将断开，旧凭据和旧票据失效，需要重新执行新的安装命令。" okText="确认生成" onConfirm={generate}>
          <Button type={ticket && !expired ? 'default' : 'primary'} icon={<ReloadOutlined />} loading={busy}>{ticket ? '重新生成' : '生成安装脚本'}</Button>
        </Popconfirm>
      </Space>
    </FormSection>
  </Modal>;
}
