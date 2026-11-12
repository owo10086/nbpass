import { useState } from 'react';
import { Button, Checkbox, Empty, Input, Modal, Space } from 'antd';
import { ArrowDownOutlined, ArrowLeftOutlined, ArrowRightOutlined, ArrowUpOutlined, SearchOutlined } from '@ant-design/icons';
import { ColorTag } from './ColorTag';
import { memberKey, moveMembers, replaceMemberAddresses, type MemberAddress, type EndpointOption } from './EndpointData';
import './Endpoints.css';

// 已选列表始终按表单中的成员 ID 顺序渲染；搜索只影响可见项，不改变主备顺序。
export function EndpointMembers({ value: members = [], onChange, options, disabled = false }: {
  value?: MemberAddress[]; onChange?: (ids: MemberAddress[]) => void; options: EndpointOption[]; disabled?: boolean;
}) {
  const [modal, contextHolder] = Modal.useModal();
  const value = members.map(memberKey);
  const changeOrder = (ids: string[]) => onChange?.(ids.map(id => members.find(member => memberKey(member) === id)!));
  const [queries, setQueries] = useState({ available: '', selected: '' });
  const [checked, setChecked] = useState<string[]>([]);
  const selected = members.map(member => options.find(option => option.id === memberKey(member)) ?? { ...member, id: memberKey(member), name: `${member.node_id} · ${member.address_id}`, address: '端点配置不可用，请移除后重新选择' });
  const available = options.filter(option => !value.includes(option.id));
  const matches = (option: EndpointOption, query: string) => `${option.id} ${option.name} ${option.address}`.toLowerCase().includes(query.trim().toLowerCase());
  const visibleAvailable = available.filter(option => matches(option, queries.available));
  const visibleSelected = selected.filter(option => matches(option, queries.selected));
  const checkedAvailable = visibleAvailable.filter(option => checked.includes(option.id)).map(option => option.id);
  const checkedSelected = visibleSelected.filter(option => checked.includes(option.id)).map(option => option.id);

  function transfer(ids: string[], direction: 'add' | 'remove') {
    const finish = (next: MemberAddress[]) => { onChange?.(next); setChecked(current => current.filter(id => !ids.includes(id))); };
    if (direction === 'remove') { finish(members.filter(member => !ids.includes(memberKey(member)))); return; }
    const additions = options.filter(option => ids.includes(option.id));
    if (new Set(additions.map(option => option.node_id)).size !== additions.length) {
      modal.warning({ title: '同一服务器只能选择一个地址', content: '请为每台服务器勾选一个公网或专线地址。' }); return;
    }
    const replacements = additions.filter(option => members.some(member => member.node_id === option.node_id && member.address_id !== option.address_id));
    const apply = () => finish(replaceMemberAddresses(members, additions.map(({ node_id, address_id }) => ({ node_id, address_id }))));
    if (replacements.length) modal.confirm({ title: '替换已选服务器的地址？', content: `${replacements.map(option => option.name).join('、')} 将替换该服务器现有地址，主备位置保持不变。`, okText: '替换地址', cancelText: '取消', onOk: apply });
    else apply();
  }
  function panel(side: 'available' | 'selected', items: EndpointOption[], total: number) {
    const ids = items.map(option => option.id);
    const count = ids.filter(id => checked.includes(id)).length;
    const right = side === 'selected';
    return <section className="endpoint-member-panel" aria-label={right ? '已选端点' : '可选端点'}>
      <div className="endpoint-member-head"><strong>{right ? '已选端点' : '可选端点'}</strong><ColorTag tone={right ? 'orange' : 'neutral'}>{total}</ColorTag></div>
      <div className="endpoint-member-tools">
        <Input aria-label={right ? '筛选已选端点' : '搜索可选端点'} placeholder={right ? '筛选已选端点' : '搜索名称、地址或 ID'} prefix={<SearchOutlined />} allowClear value={queries[side]} onChange={event => setQueries({ ...queries, [side]: event.target.value })} />
        <Checkbox disabled={disabled || !items.length} checked={!!items.length && count === items.length} indeterminate={count > 0 && count < items.length} onChange={event => setChecked(current => event.target.checked ? [...new Set([...current, ...ids])] : current.filter(id => !ids.includes(id)))}>全选当前结果</Checkbox>
      </div>
      <ul className="endpoint-member-list">
        {items.map(option => {
          const index = value.indexOf(option.id);
          return <li key={option.id} className={checked.includes(option.id) ? 'is-checked' : ''}>
            <Checkbox disabled={disabled} checked={checked.includes(option.id)} onChange={event => setChecked(current => event.target.checked ? [...current, option.id] : current.filter(id => id !== option.id))} aria-label={`选择端点 ${option.name}`} />
            {right && <span className="endpoint-member-index" title={index === 0 ? '主端点' : `备用端点 ${index}`}>{String(index + 1).padStart(2, '0')}</span>}
            <div className="endpoint-member-name"><strong>{option.name}</strong><span className="mono">{option.address}</span></div>
            {right ? <Space size={0}>
              <Button type="text" size="small" disabled={disabled || index === 0} icon={<ArrowUpOutlined />} aria-label={`上移端点 ${option.name}`} onClick={() => changeOrder(moveMembers(value, [option.id], 'up'))} />
              <Button type="text" size="small" disabled={disabled || index === value.length - 1} icon={<ArrowDownOutlined />} aria-label={`下移端点 ${option.name}`} onClick={() => changeOrder(moveMembers(value, [option.id], 'down'))} />
            </Space> : <ColorTag tone="blue">WSS</ColorTag>}
          </li>;
        })}
        {!items.length && <li className="endpoint-member-empty"><Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={queries[side] ? '没有匹配的端点' : right ? '请选择转发端点' : '暂无可选端点'} /></li>}
      </ul>
    </section>;
  }
  return <div className="endpoint-members">
    {contextHolder}
    <div className="endpoint-member-transfer">
      {panel('available', visibleAvailable, available.length)}
      <div className="endpoint-member-transfer-actions">
        <Button icon={<ArrowRightOutlined />} disabled={disabled || !checkedAvailable.length} onClick={() => transfer(checkedAvailable, 'add')}>添加</Button>
        <Button icon={<ArrowLeftOutlined />} disabled={disabled || !checkedSelected.length} onClick={() => transfer(checkedSelected, 'remove')}>移除</Button>
      </div>
      {panel('selected', visibleSelected, selected.length)}
    </div>
    <div className="endpoint-member-order"><span>调整已选顺序：</span>
      <Button size="small" icon={<ArrowUpOutlined />} disabled={disabled || !checkedSelected.length} onClick={() => changeOrder(moveMembers(value, checkedSelected, 'up'))}>上移</Button>
      <Button size="small" icon={<ArrowDownOutlined />} disabled={disabled || !checkedSelected.length} onClick={() => changeOrder(moveMembers(value, checkedSelected, 'down'))}>下移</Button>
      <span>第一项为主端点，其余按顺序备用。</span>
    </div>
  </div>;
}
