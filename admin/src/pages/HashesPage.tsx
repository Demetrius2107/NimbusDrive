import { Card, Table } from 'antd'

interface HashRow {
  key: string
  hash: string
  size: number
  refCount: number
  storagePath: string
  createdAt: string
}

export function HashesPage() {
  // TODO: useQuery 取 /admin/hashes（ref_count=0 的为可回收候选）
  const columns = [
    { title: '哈希', dataIndex: 'hash' },
    { title: '大小', dataIndex: 'size' },
    { title: '引用计数', dataIndex: 'refCount' },
    { title: '存储路径', dataIndex: 'storagePath' },
    { title: '创建时间', dataIndex: 'createdAt' },
  ]
  return (
    <Card title="哈希池（全局去重）">
      <Table<HashRow> columns={columns} dataSource={[]} pagination={{ pageSize: 20 }} />
    </Card>
  )
}
