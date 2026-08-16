import { Card, Progress, Descriptions, Empty } from 'antd'

export function QuotaPage() {
  // TODO: useQuery 取 /users/me/quota
  const used = 0
  const quota = 0
  const percent = quota > 0 ? (used / quota) * 100 : 0

  return (
    <Card title="存储配额">
      {quota === 0 ? (
        <Empty description="配额未分配（骨架页面）" />
      ) : (
        <>
          <Progress percent={Math.round(percent)} status={percent >= 95 ? 'exception' : 'active'} />
          <Descriptions column={2} style={{ marginTop: 16 }}>
            <Descriptions.Item label="已用">{formatSize(used)}</Descriptions.Item>
            <Descriptions.Item label="总量">{formatSize(quota)}</Descriptions.Item>
          </Descriptions>
        </>
      )}
    </Card>
  )
}

function formatSize(bytes: number): string {
  if (bytes === 0) return '0 B'
  const k = 1024
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  const i = Math.floor(Math.log(bytes) / Math.log(k))
  return `${(bytes / Math.pow(k, i)).toFixed(1)} ${units[i]}`
}
