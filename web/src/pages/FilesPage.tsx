import { useState } from 'react'
import { Card, Upload, Button, Table, Progress, App, Space } from 'antd'
import { UploadOutlined, FolderOutlined } from '@ant-design/icons'
import { uploadFile, type UploadProgress } from '../lib/uploader'

interface FileRow {
  key: string
  name: string
  size: number
  percent: number
  phase: UploadProgress['phase']
}

export function FilesPage() {
  const { message } = App.useApp()
  const [rows, setRows] = useState<FileRow[]>([])

  const handleUpload = async (file: File) => {
    const key = `${file.name}-${file.size}-${Date.now()}`
    setRows((prev) => [
      { key, name: file.name, size: file.size, percent: 0, phase: 'hashing' },
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

  const columns = [
    { title: '文件名', dataIndex: 'name', render: (n: string) => <Space><FolderOutlined />{n}</Space> },
    {
      title: '大小',
      dataIndex: 'size',
      render: (s: number) => formatSize(s),
    },
    {
      title: '进度',
      dataIndex: 'percent',
      render: (p: number, r: FileRow) =>
        r.phase === 'error' ? <span style={{ color: 'red' }}>失败</span> : <Progress percent={Math.round(p)} size="small" />,
    },
  ]

  return (
    <Card
      title="我的文件"
      extra={
        <Upload
          beforeUpload={handleUpload}
          showUploadList={false}
          multiple
        >
          <Button type="primary" icon={<UploadOutlined />}>
            上传文件
          </Button>
        </Upload>
      }
    >
      <Table columns={columns} dataSource={rows} pagination={{ pageSize: 20 }} />
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
