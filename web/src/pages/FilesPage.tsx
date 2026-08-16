import { useState } from 'react'
import { Card, Upload, Button, Table, Progress, App, Space } from 'antd'
import { UploadOutlined, FolderOutlined, FileOutlined, DownloadOutlined } from '@ant-design/icons'
import { uploadFile, type UploadProgress } from '../lib/uploader'
import { downloadFile, extractDownloadError } from '../lib/downloader'
import { palette } from '../theme'

interface FileRow {
  key: string
  fileId: number | null
  name: string
  size: number
  percent: number
  phase: UploadProgress['phase']
}

export function FilesPage() {
  const { message } = App.useApp()
  const [rows, setRows] = useState<FileRow[]>([])
  const [downloading, setDownloading] = useState<Set<string>>(new Set())

  const handleUpload = async (file: File) => {
    const key = `${file.name}-${file.size}-${Date.now()}`
    setRows((prev) => [
      { key, fileId: null, name: file.name, size: file.size, percent: 0, phase: 'hashing' },
      ...prev,
    ])
    try {
      const result = await uploadFile({
        file,
        onProgress: (p) => {
          setRows((prev) =>
            prev.map((r) => (r.key === key ? { ...r, percent: p.percent, phase: p.phase } : r)),
          )
        },
      })
      // 上传成功后记录 fileId，供下载使用。
      setRows((prev) =>
        prev.map((r) => (r.key === key ? { ...r, fileId: result.fileId } : r)),
      )
      message.success(result.instant ? `${file.name} 秒传成功` : `${file.name} 上传完成`)
    } catch (e) {
      const err = e as Error
      setRows((prev) =>
        prev.map((r) => (r.key === key ? { ...r, phase: 'error', percent: 0 } : r)),
      )
      message.error(`${file.name} 上传失败：${err.message}`)
    }
    return false // 阻止 antd 默认上传行为
  }

  const handleDownload = async (row: FileRow) => {
    if (!row.fileId) {
      message.warning('文件尚未上传完成，无法下载')
      return
    }
    setDownloading((prev) => new Set(prev).add(row.key))
    try {
      await downloadFile({ fileId: row.fileId, fileName: row.name })
      message.success(`${row.name} 下载已开始`)
    } catch (e) {
      const msg = await extractDownloadError(e)
      message.error(`${row.name} 下载失败：${msg}`)
    } finally {
      setDownloading((prev) => {
        const next = new Set(prev)
        next.delete(row.key)
        return next
      })
    }
  }

  const columns = [
    {
      title: '文件名',
      dataIndex: 'name',
      render: (n: string) => (
        <Space>
          <FolderOutlined style={{ color: palette.accent }} />
          {n}
        </Space>
      ),
    },
    {
      title: '大小',
      dataIndex: 'size',
      render: (s: number) => <span style={{ color: palette.textSecondary }}>{formatSize(s)}</span>,
    },
    {
      title: '进度',
      dataIndex: 'percent',
      render: (p: number, r: FileRow) =>
        r.phase === 'error' ? (
          <span style={{ color: '#ef4444', fontSize: 13 }}>失败</span>
        ) : (
          <Progress
            percent={Math.round(p)}
            size="small"
            strokeColor={{ from: palette.primary, to: palette.accent }}
          />
        ),
    },
    {
      title: '操作',
      key: 'action',
      width: 80,
      render: (_: unknown, r: FileRow) => (
        <Button
          type="text"
          size="small"
          icon={<DownloadOutlined />}
          loading={downloading.has(r.key)}
          disabled={r.fileId === null || r.phase === 'error'}
          onClick={() => handleDownload(r)}
        />
      ),
    },
  ]

  return (
    <Card
      title={
        <span style={{ display: 'flex', alignItems: 'center', gap: 8, fontWeight: 600 }}>
          <FileOutlined style={{ color: palette.primary }} />
          我的文件
        </span>
      }
      extra={
        <Upload beforeUpload={handleUpload} showUploadList={false} multiple>
          <Button
            type="primary"
            icon={<UploadOutlined />}
            style={{
              background: palette.gradientPrimary,
              border: 'none',
              fontWeight: 600,
              boxShadow: '0 2px 10px rgba(99,102,241,0.3)',
            }}
          >
            上传文件
          </Button>
        </Upload>
      }
    >
      <Table
        columns={columns}
        dataSource={rows}
        pagination={{ pageSize: 20 }}
        locale={{ emptyText: '暂无文件，点击右上角上传' }}
      />
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
