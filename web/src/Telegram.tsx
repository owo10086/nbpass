import { useEffect, useRef, useState } from 'react';
import { Alert, Button, Form, Input, Skeleton, Space, Switch, Table } from 'antd';
import { PageHeader, SettingsSection } from './PageHeader';
import { api, APIError } from './api';

type TelegramStatus = {
  enabled: boolean; configured: boolean; reachability: 'unknown' | 'reachable' | 'unreachable';
  last_attempt_at: string | null; last_success_at: string | null; pending: number; expired: number;
  failures: { incident_id: string; attempted_at: string; attempt: number; message: string }[];
};
type Settings = { enabled: boolean; bot_token?: string; chat_id?: string };
const date = (value: string | null) => value ? new Date(value).toLocaleString() : '暂无';

export function Telegram({ onSessionExpired }: { onSessionExpired: () => void }) {
  const [status, setStatus] = useState<TelegramStatus>();
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const [saving, setSaving] = useState(false);
  const [testing, setTesting] = useState(false);
  const [form] = Form.useForm<Settings>();
  const expired = useRef(onSessionExpired);
  expired.current = onSessionExpired;

  function failed(e: unknown) {
    if (e instanceof APIError && (e.status === 401 || e.status === 428)) expired.current();
    else setError((e as Error).message);
  }

  useEffect(() => {
    let active = true;
    let timer: ReturnType<typeof setTimeout>;
    async function load() {
      try {
        const result = await api<TelegramStatus>('/settings/telegram');
        if (!active) return;
        setStatus(result); setError('');
      } catch (e) { if (active) failed(e); }
      finally { if (active) timer = setTimeout(() => void load(), 5000); }
    }
    void load();
    return () => { active = false; clearTimeout(timer); };
  }, [form]);

  async function save(values: Settings) {
    setSaving(true); setError(''); setNotice('');
    try {
      const body: Settings = { enabled: values.enabled };
      if (values.bot_token) body.bot_token = values.bot_token;
      if (values.chat_id) body.chat_id = values.chat_id;
      setStatus(await api<TelegramStatus>('/settings/telegram', 'PUT', body));
      form.setFieldsValue({ bot_token: undefined, chat_id: undefined });
      setNotice('Telegram 设置已保存。可以发送测试通知检查是否可达。');
    } catch (e) { failed(e); }
    finally { setSaving(false); }
  }

  async function test() {
    setTesting(true); setError(''); setNotice('');
    try {
      await api('/settings/telegram/test', 'POST');
      setNotice('测试通知已排队，发送结果会自动更新。');
    } catch (e) { failed(e); }
    finally { setTesting(false); }
  }

  return <>
    <PageHeader title="Telegram 通知" description="节点离线满 2 分钟后通知，恢复上线时通知。同一事故的待发信息会合并，发送失败会自动重试。" />
    {error && <Alert title={error} type="error" showIcon className="message" />}
    {notice && <Alert title={notice} type="success" showIcon className="message" />}
    <SettingsSection title="通知设置" description="Bot token 和聊天目标保存后不会回显；留空保留原值。请先与机器人开始聊天，或将它加入目标群组并授予发送权限。">
      {!status ? <Skeleton active paragraph={{ rows: 3 }} title={false} /> : <Form form={form} layout="vertical" initialValues={{ enabled: status.enabled }} onFinish={save}>
        <Form.Item name="enabled" label="启用 Telegram 通知" valuePropName="checked"><Switch /></Form.Item>
        <Form.Item name="bot_token" label="Bot token"><Input.Password autoComplete="new-password" placeholder={status.configured ? '已配置，留空保留' : '填写机器人 token'} /></Form.Item>
        <Form.Item name="chat_id" label="聊天目标"><Input.Password autoComplete="new-password" placeholder={status.configured ? '已配置，留空保留' : '聊天 ID、群组 ID 或 @频道名'} /></Form.Item>
        <Space wrap><Button type="primary" htmlType="submit" loading={saving}>保存设置</Button><Button disabled={!status.enabled || saving} loading={testing} onClick={() => void test()}>发送测试通知</Button></Space>
      </Form>}
    </SettingsSection>
    <SettingsSection title="投递状态" description="可达性依据当前设置最近一次发送结果。失败从 5 秒开始退避重试，普通退避最长 5 分钟；待发超过 24 小时后过期。停用期间暂停发送。">
      {!status ? <Skeleton active paragraph={{ rows: 2 }} title={false} /> : <dl className="kv">
        <dt>通知</dt><dd><span className="status"><span className={`dot ${status.enabled ? 'ok' : ''}`} />{status.enabled ? '已启用' : '已停用'}</span></dd>
        <dt>可达性</dt><dd><span className="status"><span className={`dot ${status.reachability === 'reachable' ? 'ok' : status.reachability === 'unreachable' ? 'danger' : ''}`} />{{ reachable: '最近发送成功', unreachable: '最近发送失败', unknown: '尚未确认' }[status.reachability]}</span></dd>
        <dt>队列</dt><dd className="num">待发送 {status.pending} 条 · 已过期 {status.expired} 条</dd>
        <dt>最近尝试</dt><dd className="num">{date(status.last_attempt_at)}</dd>
        <dt>最近成功</dt><dd className="num">{date(status.last_success_at)}</dd>
      </dl>}
    </SettingsSection>
    <SettingsSection title="最近发送失败" wide>
      <div className="panel">
        <Table rowKey={(_, index) => String(index)} dataSource={status?.failures ?? []} pagination={false} scroll={{ x: 650 }} locale={{ emptyText: '暂无发送失败记录' }} columns={[
          { title: '时间', dataIndex: 'attempted_at', render: value => <span className="num">{date(value)}</span>, width: 190 },
          { title: '事故', dataIndex: 'incident_id', width: 190, render: value => <span className="mono">{value}</span> },
          { title: '尝试次数', dataIndex: 'attempt', width: 95, render: value => <span className="num">{value}</span> },
          { title: '原因', dataIndex: 'message' },
        ]} />
      </div>
    </SettingsSection>
  </>;
}
