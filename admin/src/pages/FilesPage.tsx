import { Card, Table } from 'antd'

interface FileRow {
  key: number
  id: number
  user: string
  name: string
  size: number
  status: string
  createdAt: string
}

export function FilesPage() {
  // TODO: useQuery 取 /admin/files
  const columns = [
    { title: 'ID', dataIndex: 'id' },
    { title: '所属用户', dataIndex: 'user' },
    { title: '文件名', dataIndex: 'name' },
    { title: '大小', dataIndex: 'size' },
    { title: '状态', dataIndex: 'status' },
    { title: '创建时间', dataIndex: 'createdAt' },
  ]
  return (
    <Card title="文件管理">
      <Table<FileRow> columns={columns} dataSource={[]} pagination={{ pageSize: 20 }} />
    </Card>
  )
}
