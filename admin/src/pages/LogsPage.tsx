import { Card, Table } from 'antd'

interface LogRow {
  key: number
  id: number
  actor: string
  action: string
  target: string
  ip: string
  createdAt: string
}

export function LogsPage() {
  // TODO: useQuery 取 /admin/logs
  const columns = [
    { title: 'ID', dataIndex: 'id' },
    { title: '操作者', dataIndex: 'actor' },
    { title: '动作', dataIndex: 'action' },
    { title: '对象', dataIndex: 'target' },
    { title: 'IP', dataIndex: 'ip' },
    { title: '时间', dataIndex: 'createdAt' },
  ]
  return (
    <Card title="操作日志">
      <Table<LogRow> columns={columns} dataSource={[]} pagination={{ pageSize: 20 }} />
    </Card>
  )
}
