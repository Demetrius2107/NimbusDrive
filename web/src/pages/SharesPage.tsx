import { useState, useEffect, useCallback } from 'react'
import { Card, Table, Button, Space, App, Spin, Tag, Popconfirm, Tooltip } from 'antd'
import {
  ShareAltOutlined, DeleteOutlined, CopyOutlined, LockOutlined, UnlockOutlined,
} from '@ant-design/icons'
import { listShares, cancelShare, type Share } from '../lib/shares'
import { palette } from '../theme'

export function SharesPage() {
  const { message } = App.useApp()
  const [shares, setShares] = useState<Share[]>([])
  const [loading, setLoading] = useState(false)
  const [canceling, setCanceling] = useState<Set<string>>(new Set())

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const resp = await listShares(1, 100)
      setShares(resp.shares)
    } catch (e) {
      const err = e as Error
      message.error(`加载分享列表失败：${err.message}`)
    } finally {
      setLoading(false)
    }
  }, [message])

  useEffect(() => {
    load()
  }, [load])

  const handleCancel = async (share: Share) => {
    setCanceling((prev) => new Set(prev).add(share.id))
    try {
      await cancelShare(share.id)
      message.success('分享已取消')
      load()
    } catch (e) {
      const err = e as Error
      message.error(`取消失败：${err.message}`)
    } finally {
      setCanceling((prev) => {
        const next = new Set(prev)
        next.delete(share.id)
        return next
      })
    }
  }

  const handleCopyLink = (share: Share) => {
    const link = `${window.location.origin}/s/${share.id}`
    navigator.clipboard.writeText(link).then(
      () => message.success('链接已复制'),
      () => message.error('复制失败，请手动复制'),
    )
  }

  const statusTag = (status: string) => {
    const map: Record<string, { color: string; text: string }> = {
      ready: { color: palette.primary, text: '未访问' },
      active: { color: palette.accent, text: '活跃' },
      expired: { color: '#9ca3af', text: '已过期' },
      cancelled: { color: '#ef4444', text: '已取消' },
    }
    const cfg = map[status] ?? { color: '#9ca3af', text: status }
    return <Tag color={cfg.color} style={{ borderRadius: 4 }}>{cfg.text}</Tag>
  }

  const columns = [
    {
      title: '分享 ID',
      dataIndex: 'id',
      width: 180,
      render: (id: string) => (
        <span style={{ fontFamily: 'monospace', fontSize: 13, color: palette.textSecondary }}>
          {id}
        </span>
      ),
    },
    {
      title: '文件 ID',
      dataIndex: 'file_id',
      width: 100,
      render: (fid: number) => (
        <span style={{ color: palette.textSecondary }}>{fid}</span>
      ),
    },
    {
      title: '密码',
      key: 'password',
      width: 80,
      render: (_: unknown, s: Share) =>
        s.has_password ? (
          <LockOutlined style={{ color: palette.accent }} />
        ) : (
          <UnlockOutlined style={{ color: palette.textSecondary }} />
        ),
    },
    {
      title: '过期时间',
      dataIndex: 'expires_at',
      width: 180,
      render: (v?: string | null) =>
        v ? (
          <span style={{ color: palette.textSecondary, fontSize: 13 }}>
            {new Date(v).toLocaleString('zh-CN')}
          </span>
        ) : (
          <span style={{ color: palette.textSecondary }}>永久</span>
        ),
    },
    {
      title: '访问次数',
      key: 'access',
      width: 120,
      render: (_: unknown, s: Share) => (
        <span style={{ fontSize: 13 }}>
          {s.access_count}
          {s.max_access != null ? ` / ${s.max_access}` : ' / ∞'}
        </span>
      ),
    },
    {
      title: '状态',
      dataIndex: 'status',
      width: 100,
      render: statusTag,
    },
    {
      title: '创建时间',
      dataIndex: 'created_at',
      width: 180,
      render: (v?: string) =>
        v ? (
          <span style={{ color: palette.textSecondary, fontSize: 13 }}>
            {new Date(v).toLocaleString('zh-CN')}
          </span>
        ) : null,
    },
    {
      title: '操作',
      key: 'action',
      width: 160,
      render: (_: unknown, s: Share) => (
        <Space>
          <Tooltip title="复制分享链接">
            <Button
              size="small"
              type="text"
              icon={<CopyOutlined />}
              onClick={() => handleCopyLink(s)}
            />
          </Tooltip>
          <Popconfirm
            title="取消分享"
            description="取消后链接将立即失效，无法恢复。确认？"
            okText="取消分享"
            okType="danger"
            cancelText="保留"
            onConfirm={() => handleCancel(s)}
            disabled={s.status === 'cancelled'}
          >
            <Button
              size="small"
              danger
              type="text"
              icon={<DeleteOutlined />}
              loading={canceling.has(s.id)}
              disabled={s.status === 'cancelled'}
            />
          </Popconfirm>
        </Space>
      ),
    },
  ]

  return (
    <Card
      title={
        <Space>
          <ShareAltOutlined style={{ color: palette.primary }} />
          <span style={{ fontWeight: 600 }}>我的分享</span>
        </Space>
      }
      extra={
        <span style={{ color: palette.textSecondary, fontSize: 13 }}>
          在文件页右键菜单中创建分享
        </span>
      }
    >
      <Spin spinning={loading}>
        <Table
          columns={columns}
          dataSource={shares.map((s) => ({ ...s, key: s.id }))}
          pagination={{ pageSize: 20 }}
          locale={{ emptyText: '暂无分享，在文件页对文件发起分享' }}
        />
      </Spin>
    </Card>
  )
}
