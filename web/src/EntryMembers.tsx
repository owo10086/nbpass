import { Button, Form, Select, Space, Typography } from 'antd';

// 数组顺序就是入口优先级；显式上移/下移避免多选控件隐式排序。
export function EntryMembers({ nodes }: { nodes: { id: string; name: string }[] }) {
  return <Form.List name="members" rules={[{ validator: (_, values) => values?.length ? Promise.resolve() : Promise.reject(new Error('请至少添加一个成员')) }]}>
    {(fields, { add, remove, move }, { errors }) => <>
      <Typography.Paragraph type="secondary">所有成员应用相同规则并监听。第一位为首选入口；调整排序后保存生效。</Typography.Paragraph>
      {fields.map((field, index) => <div key={field.key} style={{ marginBottom: 12 }}>
        <Typography.Text>{index === 0 ? '1 · 首选入口' : `${index + 1} · 入口成员`}</Typography.Text>
        <Form.Item name={field.name} rules={[{ required: true, message: '请选择入口节点' }]} style={{ marginBottom: 4 }}>
          <Select showSearch optionFilterProp="label" options={nodes.map(n => ({ value: n.id, label: n.name }))} placeholder="选择入口节点" />
        </Form.Item>
        <Space wrap>
          <Button size="small" disabled={index === 0} onClick={() => move(index, index - 1)}>上移</Button>
          <Button size="small" disabled={index === fields.length - 1} onClick={() => move(index, index + 1)}>下移</Button>
          <Button size="small" danger disabled={fields.length === 1} onClick={() => remove(index)}>移除</Button>
        </Space>
      </div>)}
      <Button disabled={fields.length >= 64} onClick={() => add()}>添加成员（最多 64 个）</Button>
      <Form.ErrorList errors={errors} />
    </>}
  </Form.List>;
}
