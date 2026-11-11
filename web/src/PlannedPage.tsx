import { Empty } from 'antd';
import { PageHeader } from './PageHeader';

export function PlannedPage({ title, description }: { title: string; description: string }) {
  return <><PageHeader title={title} description={description} /><section className="planned-page"><Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={<><strong>规划中</strong><p>尚未开放</p></>} /></section></>;
}
