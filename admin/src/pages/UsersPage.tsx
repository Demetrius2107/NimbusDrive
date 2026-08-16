import { Card, Table, Tag } from 'antd'

interface UserRow {
  key: number
  username: string
  email: string
  quota: number
  used: number
  status: number
  createdAt: string
}

export function UsersPage() {
  // TODO: useQuery 取 /admin/users
  const columns = [
    { title: '用户名', dataIndex: 'username' },
    { title: '邮箱', dataIndex: 'email' },
    { title: '配额', dataIndex: 'quota' },
    { title: '已用', dataIndex: 'used' },
    {
      title: '状态',
      dataIndex: 'status',
      render: (s: number) =>
        s === 1 ? <Tag color="green">启用</Tag> : <Tag color="red">禁用</Tag>,
    },
    { title: '注册时间', dataIndex: 'createdAt' },
  ]
  return (
    <Card title="用户管理">
      <Table<UserRow> columns={columns} dataSource={[]} pagination={{ pageSize: 20 }} />
    </Card>
  )
}
