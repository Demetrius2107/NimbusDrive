import { Card, Progress, Descriptions, Empty } from 'antd'
import { DashboardOutlined } from '@ant-design/icons'
import { palette } from '../theme'

export function QuotaPage() {
  // TODO: useQuery 取 /users/me/quota
  const used = 0
  const quota = 0
  const percent = quota > 0 ? (used / quota) * 100 : 0

  return (
    <Card
      title={
        <span style={{ display: 'flex', alignItems: 'center', gap: 8, fontWeight: 600 }}>
          <DashboardOutlined style={{ color: palette.primary }} />
          存储配额
        </span>
      }
    >
      {quota === 0 ? (
        <Empty description="配额未分配（骨架页面）" />
      ) : (
        <div style={{ display: 'flex', flexDirection: 'column', alignItems: 'center', padding: '24px 0' }}>
          <Progress
            type="circle"
            percent={Math.round(percent)}
            size={180}
            strokeColor={{ from: palette.primary, to: palette.accent }}
            trailColor="#ede9fe"
            format={(p) => (
              <div style={{ textAlign: 'center' }}>
                <div style={{ fontSize: 28, fontWeight: 700, color: '#1e1b4b' }}>{p}%</div>
                <div style={{ fontSize: 12, color: palette.textSecondary }}>已使用</div>
              </div>
            )}
          />
          <Descriptions column={2} style={{ marginTop: 32, width: '100%', maxWidth: 400 }}>
            <Descriptions.Item label="已用">
              <span style={{ fontWeight: 600, color: palette.primary }}>{formatSize(used)}</span>
            </Descriptions.Item>
            <Descriptions.Item label="总量">
              <span style={{ fontWeight: 600, color: palette.accent }}>{formatSize(quota)}</span>
            </Descriptions.Item>
          </Descriptions>
        </div>
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
