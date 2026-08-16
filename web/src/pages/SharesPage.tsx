import { Card, Empty } from 'antd'
import { ShareAltOutlined } from '@ant-design/icons'
import { palette } from '../theme'

export function SharesPage() {
  return (
    <Card
      title={
        <span style={{ display: 'flex', alignItems: 'center', gap: 8, fontWeight: 600 }}>
          <ShareAltOutlined style={{ color: palette.primary }} />
          我的分享
        </span>
      }
    >
      <Empty description="暂无分享（骨架页面）" />
    </Card>
  )
}
