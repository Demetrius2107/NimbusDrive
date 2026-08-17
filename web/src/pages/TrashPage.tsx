import { useState, useEffect, useCallback } from 'react'
import { Card, Table, Button, Space, App, Spin, Popconfirm } from 'antd'
import { DeleteOutlined, RestOutlined, FileOutlined, FolderOutlined } from '@ant-design/icons'
import { listTrash, restoreFile, permanentDelete, type FileNode } from '../lib/files'
import { palette } from '../theme'

export function TrashPage() {
  const { message } = App.useApp()
  const [items, setItems] = useState<FileNode[]>([])
  const [loading, setLoading] = useState(false)
  const [restoring, setRestoring] = useState<Set<number>>(new Set())
  const [deleting, setDeleting] = useState<Set<number>>(new Set())

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const resp = await listTrash(1, 100)
      setItems(resp.files)
    } catch (e) {
      const err = e as Error
      message.error(`加载回收站失败：${err.message}`)
    } finally {
      setLoading(false)
    }
  }, [message])

  useEffect(() => {
    load()
  }, [load])

  const handleRestore = async (file: FileNode) => {
    setRestoring((prev) => new Set(prev).add(file.id))
    try {
      await restoreFile(file.id)
      message.success(`「${file.name}」已恢复`)
      load()
    } catch (e) {
      const err = e as Error
      message.error(`恢复失败：${err.message}`)
    } finally {
      setRestoring((prev) => {
        const next = new Set(prev)
        next.delete(file.id)
        return next
      })
    }
  }

  const handlePermanentDelete = async (file: FileNode) => {
    setDeleting((prev) => new Set(prev).add(file.id))
    try {
      await permanentDelete(file.id)
      message.success(`「${file.name}」已彻底删除`)
      load()
    } catch (e) {
      const err = e as Error
      message.error(`删除失败：${err.message}`)
    } finally {
      setDeleting((prev) => {
        const next = new Set(prev)
        next.delete(file.id)
        return next
      })
    }
  }

  const columns = [
    {
      title: '文件名',
      dataIndex: 'name',
      render: (name: string, file: FileNode) => (
        <Space>
          {file.is_folder ? (
            <FolderOutlined style={{ color: palette.accent }} />
          ) : (
            <FileOutlined style={{ color: palette.primary }} />
          )}
          <span>{name}</span>
        </Space>
      ),
    },
    {
      title: '大小',
      dataIndex: 'size',
      width: 120,
      render: (size: number, file: FileNode) =>
        file.is_folder ? (
          <span style={{ color: palette.textSecondary }}>—</span>
        ) : (
          <span style={{ color: palette.textSecondary }}>{formatSize(size)}</span>
        ),
    },
    {
      title: '删除时间',
      dataIndex: 'deleted_at',
      width: 200,
      render: (v?: string) =>
        v ? (
          <span style={{ color: palette.textSecondary }}>
            {new Date(v).toLocaleString('zh-CN')}
          </span>
        ) : (
          <span style={{ color: palette.textSecondary }}>—</span>
        ),
    },
    {
      title: '操作',
      key: 'action',
      width: 200,
      render: (_: unknown, file: FileNode) => (
        <Space>
          <Button
            size="small"
            icon={<RestOutlined />}
            loading={restoring.has(file.id)}
            onClick={() => handleRestore(file)}
          >
            恢复
          </Button>
          <Popconfirm
            title="彻底删除"
            description={`「${file.name}」将被永久删除，无法恢复。确认？`}
            okText="永久删除"
            okType="danger"
            cancelText="取消"
            onConfirm={() => handlePermanentDelete(file)}
          >
            <Button
              size="small"
              danger
              icon={<DeleteOutlined />}
              loading={deleting.has(file.id)}
            >
              彻底删除
            </Button>
          </Popconfirm>
        </Space>
      ),
    },
  ]

  return (
    <Card
      title={
        <Space>
          <DeleteOutlined style={{ color: palette.accent }} />
          <span style={{ fontWeight: 600 }}>回收站</span>
        </Space>
      }
      extra={
        <span style={{ color: palette.textSecondary, fontSize: 13 }}>
          回收站中的文件可恢复，彻底删除后不可找回
        </span>
      }
    >
      <Spin spinning={loading}>
        <Table
          columns={columns}
          dataSource={items.map((f) => ({ ...f, key: f.id }))}
          pagination={{ pageSize: 20 }}
          locale={{ emptyText: '回收站为空' }}
        />
      </Spin>
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
