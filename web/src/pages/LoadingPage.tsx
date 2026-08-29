import { Skeleton } from 'antd'

export function LoadingPage() {
  return <div className="page loading-page"><Skeleton active paragraph={{ rows: 8 }} /></div>
}
