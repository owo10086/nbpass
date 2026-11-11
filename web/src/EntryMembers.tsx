import { Button, Form, Space, Transfer, Typography } from 'antd';
import { ArrowDownOutlined, ArrowUpOutlined } from '@ant-design/icons';
import { ColorTag } from './ColorTag';
import { updateMemberSelection } from './entryGroupMembers';
import './EntryGroups.css';

// 数组顺序就是入口优先级；显式上移/下移避免多选控件隐式排序。
export function EntryMembers({ nodes, all = false, lockedMembers = [] }: { nodes: { id: string; name: string }[]; all?: boolean; lockedMembers?: string[] }) {
  return <>
    <Typography.Paragraph type="secondary">{all
      ? '所有服务器应用相同端口配置。自动摘除按组内顺序保留；已绑定记录的成员须在「DNS 状态 → 受管 DNS 与发布」中移出。'
      : '所有服务器应用相同端口配置。第一位为首选入口；可用上移、下移调整优先级，保存后生效。'}</Typography.Paragraph>
    <Form.Item name="members" rules={[{ validator: (_, values: string[] | undefined) => {
      if (!values?.length) return Promise.reject(new Error('请至少选择一台服务器'));
      if (values.length > 64) return Promise.reject(new Error('最多选择 64 台服务器'));
      return Promise.resolve();
    } }]}>
      <MemberTransfer nodes={nodes} all={all} lockedMembers={lockedMembers} />
    </Form.Item>
  </>;
}

function MemberTransfer({ nodes, all, lockedMembers, value = [], onChange, disabled }: {
  nodes: { id: string; name: string }[]; all: boolean; lockedMembers: string[]; value?: string[]; onChange?: (ids: string[]) => void; disabled?: boolean;
}) {
  const data = [...nodes, ...value.filter(id => !nodes.some(n => n.id === id)).map(id => ({ id, name: id }))]
    .map(n => ({ key: n.id, title: n.name, disabled: lockedMembers.includes(n.id) || (!value.includes(n.id) && value.length >= 64) }));
  function move(id: string, offset: number) {
    const next = [...value], index = next.indexOf(id);
    if (index < 0 || index + offset < 0 || index + offset >= next.length) return;
    [next[index], next[index + offset]] = [next[index + offset], next[index]];
    onChange?.(next);
  }
  return <Transfer className="sg-transfer" dataSource={data} targetKeys={value} disabled={disabled}
    showSearch titles={['可选服务器', '已选服务器']}
    locale={{ searchPlaceholder: '搜索名称或 ID', itemUnit: '台', itemsUnit: '台', notFoundContent: '暂无服务器' }}
    filterOption={(input, item) => `${item.title} ${item.key}`.toLowerCase().includes(input.toLowerCase())}
    onChange={keys => onChange?.(updateMemberSelection(value, keys.map(String)))}
    render={item => {
      const index = value.indexOf(item.key);
      return { value: item.title, label: <div className="sg-transfer-row">
        <span className="sg-transfer-name">{index >= 0 && <span className="sg-member-index">{String(index + 1).padStart(2, '0')}</span>}{item.title}
          {index === 0 && !all && <ColorTag tone="orange">首选</ColorTag>}
          {lockedMembers.includes(item.key) && <span className="sg-help">记录已绑定</span>}
        </span>
        {index >= 0 && <Space size={0} onClick={e => e.stopPropagation()}>
          <Button type="text" size="small" icon={<ArrowUpOutlined />} aria-label={`上移 ${item.title}`} disabled={disabled || index === 0 || undefined} onClick={() => move(item.key, -1)} />
          <Button type="text" size="small" icon={<ArrowDownOutlined />} aria-label={`下移 ${item.title}`} disabled={disabled || index === value.length - 1 || undefined} onClick={() => move(item.key, 1)} />
        </Space>}
      </div> };
    }} />;
}
