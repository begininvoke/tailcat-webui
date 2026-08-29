import { Flex, Space, Typography } from 'antd'
import type { ReactNode } from 'react'

export function PageHeader({ title, subtitle, actions }: { title: ReactNode; subtitle: ReactNode; actions?: ReactNode }) {
  return (
    <Flex className="page-header" align="flex-start" justify="space-between" gap={16} wrap="wrap">
      <Space direction="vertical" size={2}>
        <Typography.Title level={1}>{title}</Typography.Title>
        <Typography.Text type="secondary">{subtitle}</Typography.Text>
      </Space>
      {actions}
    </Flex>
  )
}
