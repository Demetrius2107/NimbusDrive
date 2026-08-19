import { Card, Progress, Descriptions, Empty, Tag, Space, Tooltip } from 'antd'
import { DashboardOutlined, CloudUploadOutlined, CloudDownloadOutlined, SyncOutlined } from '@ant-design/icons'
import { palette } from '../theme'
import { useQuotaStore } from '../stores/quota'

export function QuotaPage() {
  // 推送通道由 AppLayout 统一建立（侧栏配额条也要用），这里只读 store。
  const {
    storageQuota,
    usedStorage,
    uploadBytes,
    uploadQuota,
    downloadBytes,
    downloadQuota,
    period,
    connState,
  } = useQuotaStore()

  const storagePercent = storageQuota > 0 ? (usedStorage / storageQuota) * 100 : 0
  const uploadPercent = uploadQuota > 0 ? (uploadBytes / uploadQuota) * 100 : 0
  const downloadPercent = downloadQuota > 0 ? (downloadBytes / downloadQuota) * 100 : 0

  return (
    <Space direction="vertical" size={16} style={{ width: '100%' }}>
      <Card
        title={
          <span style={{ display: 'flex', alignItems: 'center', gap: 8, fontWeight: 600 }}>
            <DashboardOutlined style={{ color: palette.primary }} />
            存储配额
            <ConnTag state={connState} />
          </span>
        }
      >
        {storageQuota === 0 ? (
          <Empty description="配额未分配" />
        ) : (
          <div style={{ display: 'flex', flexDirection: 'column', alignItems: 'center', padding: '24px 0' }}>
            <Progress
              type="circle"
              percent={Math.round(storagePercent)}
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
                <span style={{ fontWeight: 600, color: palette.primary }}>{formatSize(usedStorage)}</span>
              </Descriptions.Item>
              <Descriptions.Item label="总量">
                <span style={{ fontWeight: 600, color: palette.accent }}>{formatSize(storageQuota)}</span>
              </Descriptions.Item>
            </Descriptions>
          </div>
        )}
      </Card>

      <Card
        title={
          <span style={{ display: 'flex', alignItems: 'center', gap: 8, fontWeight: 600 }}>
            <CloudUploadOutlined style={{ color: palette.primary }} />
            月度传输配额
            {period && <Tag style={{ marginLeft: 4 }}>{period}</Tag>}
          </span>
        }
      >
        <Space direction="vertical" size={20} style={{ width: '100%' }}>
          <TransferBar
            icon={<CloudUploadOutlined style={{ color: palette.primary }} />}
            label="上传"
            used={uploadBytes}
            quota={uploadQuota}
            percent={uploadPercent}
          />
          <TransferBar
            icon={<CloudDownloadOutlined style={{ color: palette.accent }} />}
            label="下载"
            used={downloadBytes}
            quota={downloadQuota}
            percent={downloadPercent}
          />
        </Space>
      </Card>
    </Space>
  )
}

// ConnTag 展示推送连接状态。
function ConnTag({ state }: { state: string }) {
  if (state === 'connected') {
    return (
      <Tag icon={<SyncOutlined spin />} color="success" style={{ marginLeft: 8 }}>
        实时
      </Tag>
    )
  }
  if (state === 'reconnecting') {
    return (
      <Tag icon={<SyncOutlined spin />} color="warning" style={{ marginLeft: 8 }}>
        重连中
      </Tag>
    )
  }
  if (state === 'polling') {
    return (
      <Tooltip title="实时推送不可用，已降级为定时刷新">
        <Tag color="default" style={{ marginLeft: 8 }}>
          轮询
        </Tag>
      </Tooltip>
    )
  }
  return null
}

function TransferBar({
  icon,
  label,
  used,
  quota,
  percent,
}: {
  icon: React.ReactNode
  label: string
  used: number
  quota: number
  percent: number
}) {
  return (
    <div>
      <div style={{ display: 'flex', justifyContent: 'space-between', marginBottom: 8 }}>
        <span style={{ display: 'flex', alignItems: 'center', gap: 6, fontWeight: 500, color: '#1e1b4b' }}>
          {icon}
          {label}
        </span>
        <span style={{ fontSize: 13, color: palette.textSecondary }}>
          {formatSize(used)} / {quota > 0 ? formatSize(quota) : '不限'}
        </span>
      </div>
      <Progress
        percent={Math.round(percent)}
        showInfo={false}
        strokeColor={{ from: palette.primary, to: palette.accent }}
        trailColor="#ede9fe"
      />
    </div>
  )
}

function formatSize(bytes: number): string {
  if (bytes === 0) return '0 B'
  const k = 1024
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  const i = Math.floor(Math.log(bytes) / Math.log(k))
  return `${(bytes / Math.pow(k, i)).toFixed(1)} ${units[i]}`
}
