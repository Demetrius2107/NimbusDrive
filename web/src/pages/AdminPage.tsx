import { useState, useCallback, useEffect } from 'react'
import { Card, Table, Button, Space, App, Spin, Tabs, Tag, InputNumber, Popconfirm } from 'antd'
import {
  TeamOutlined, FileSearchOutlined, AuditOutlined, LockOutlined, UnlockOutlined,
} from '@ant-design/icons'
import {
  listUsers, updateUserStatus, updateUserQuota, listFiles, listLogs,
  type AdminUser, type AdminFile, type OperationLog,
} from '../lib/admin'
import { palette } from '../theme'

export function AdminPage() {
  return (
    <Card
      title={
        <Space>
          <AuditOutlined style={{ color: palette.primary }} />
          <span style={{ fontWeight: 600 }}>管理后台</span>
        </Space>
      }
    >
      <Tabs
        defaultActiveKey="users"
        items={[
          { key: 'users', label: <span><TeamOutlined /> 用户管理</span>, children: <UsersTab /> },
          { key: 'files', label: <span><FileSearchOutlined /> 文件审计</span>, children: <FilesTab /> },
          { key: 'logs', label: <span><AuditOutlined /> 操作日志</span>, children: <LogsTab /> },
        ]}
      />
    </Card>
  )
}

// --- 用户管理 Tab ---

function UsersTab() {
  const { message, modal } = App.useApp()
  const [users, setUsers] = useState<AdminUser[]>([])
  const [loading, setLoading] = useState(false)
  const [total, setTotal] = useState(0)
  const [page, setPage] = useState(1)

  const load = useCallback(async (p: number) => {
    setLoading(true)
    try {
      const resp = await listUsers({ page: p, page_size: 50 })
      setUsers(resp.users)
      setTotal(resp.total)
      setPage(p)
    } catch (e) {
      const err = e as Error
      message.error(`加载用户列表失败：${err.message}`)
    } finally {
      setLoading(false)
    }
  }, [message])

  useEffect(() => { load(1) }, [load])

  const handleToggleStatus = async (user: AdminUser) => {
    const newStatus = user.status === 1 ? 2 : 1
    try {
      await updateUserStatus(user.id, newStatus)
      message.success(newStatus === 2 ? '已封禁' : '已解封')
      load(page)
    } catch (e) {
      const err = e as Error
      message.error(`操作失败：${err.message}`)
    }
  }

  const handleQuota = (user: AdminUser) => {
    let quota = user.storage_quota
    modal.confirm({
      title: `调整「${user.username}」的配额`,
      content: (
        <InputNumber
          defaultValue={user.storage_quota}
          min={0}
          step={1024 * 1024 * 1024}
          style={{ width: '100%', marginTop: 12 }}
          formatter={(v) => v ? `${(v / 1024 / 1024 / 1024).toFixed(1)} GB` : '0'}
          parser={(v) => v ? Math.round(parseFloat(v.replace(/[^\d.]/g, '')) * 1024 * 1024 * 1024) : 0}
          onChange={(v) => { quota = v ?? 0 }}
        />
      ),
      onOk: async () => {
        try {
          await updateUserQuota(user.id, quota)
          message.success('配额已更新')
          load(page)
        } catch (e) {
          const err = e as Error
          message.error(`操作失败：${err.message}`)
          return Promise.reject(e)
        }
      },
    })
  }

  const columns = [
    { title: 'ID', dataIndex: 'id', width: 70 },
    { title: '用户名', dataIndex: 'username', width: 120 },
    { title: '邮箱', dataIndex: 'email', width: 200 },
    {
      title: '配额', key: 'quota', width: 120,
      render: (_: unknown, u: AdminUser) => (
        <span style={{ fontSize: 13 }}>
          {(u.used_storage / 1024 / 1024 / 1024).toFixed(1)} / {(u.storage_quota / 1024 / 1024 / 1024).toFixed(1)} GB
        </span>
      ),
    },
    {
      title: '状态', key: 'status', width: 90,
      render: (_: unknown, u: AdminUser) =>
        u.status === 1
          ? <Tag color={palette.primary}>正常</Tag>
          : <Tag color="#ef4444">封禁</Tag>,
    },
    {
      title: '管理员', key: 'is_admin', width: 80,
      render: (_: unknown, u: AdminUser) => u.is_admin ? <Tag color={palette.accent}>是</Tag> : <span style={{ color: palette.textSecondary }}>—</span>,
    },
    {
      title: '操作', key: 'action', width: 180,
      render: (_: unknown, u: AdminUser) => (
        <Space>
          <Popconfirm
            title={u.status === 1 ? '确认封禁？' : '确认解封？'}
            onConfirm={() => handleToggleStatus(u)}
          >
            <Button
              size="small"
              type="text"
              danger={u.status === 1}
              icon={u.status === 1 ? <LockOutlined /> : <UnlockOutlined />}
            >
              {u.status === 1 ? '封禁' : '解封'}
            </Button>
          </Popconfirm>
          <Button size="small" type="text" onClick={() => handleQuota(u)}>
            配额
          </Button>
        </Space>
      ),
    },
  ]

  return (
    <Spin spinning={loading}>
      <Table
        columns={columns}
        dataSource={users.map((u) => ({ ...u, key: u.id }))}
        pagination={{
          current: page,
          total,
          pageSize: 50,
          onChange: (p) => load(p),
        }}
        locale={{ emptyText: '暂无用户' }}
      />
    </Spin>
  )
}

// --- 文件审计 Tab ---

function FilesTab() {
  const { message } = App.useApp()
  const [files, setFiles] = useState<AdminFile[]>([])
  const [loading, setLoading] = useState(false)
  const [total, setTotal] = useState(0)
  const [page, setPage] = useState(1)

  const load = useCallback(async (p: number) => {
    setLoading(true)
    try {
      const resp = await listFiles({ page: p, page_size: 50 })
      setFiles(resp.files)
      setTotal(resp.total)
      setPage(p)
    } catch (e) {
      const err = e as Error
      message.error(`加载文件列表失败：${err.message}`)
    } finally {
      setLoading(false)
    }
  }, [message])

  useEffect(() => { load(1) }, [load])

  const columns = [
    { title: 'ID', dataIndex: 'id', width: 70 },
    { title: '文件名', dataIndex: 'name', width: 200 },
    { title: '用户 ID', dataIndex: 'user_id', width: 90 },
    {
      title: '大小', dataIndex: 'size', width: 100,
      render: (s: number, f: AdminFile) => f.is_folder ? '—' : formatSize(s),
    },
    { title: '类型', dataIndex: 'mime_type', width: 140 },
    {
      title: '状态', dataIndex: 'status', width: 100,
      render: (s: string) => {
        const colors: Record<string, string> = { completed: palette.primary, init: '#f59e0b', uploading: palette.accent }
        return <Tag color={colors[s] ?? '#9ca3af'}>{s}</Tag>
      },
    },
    {
      title: '创建时间', dataIndex: 'created_at', width: 180,
      render: (v: string) => <span style={{ color: palette.textSecondary, fontSize: 13 }}>{new Date(v).toLocaleString('zh-CN')}</span>,
    },
  ]

  return (
    <Spin spinning={loading}>
      <Table
        columns={columns}
        dataSource={files.map((f) => ({ ...f, key: f.id }))}
        pagination={{
          current: page,
          total,
          pageSize: 50,
          onChange: (p) => load(p),
        }}
        locale={{ emptyText: '暂无文件' }}
      />
    </Spin>
  )
}

// --- 操作日志 Tab ---

function LogsTab() {
  const { message } = App.useApp()
  const [logs, setLogs] = useState<OperationLog[]>([])
  const [loading, setLoading] = useState(false)
  const [total, setTotal] = useState(0)
  const [page, setPage] = useState(1)

  const load = useCallback(async (p: number) => {
    setLoading(true)
    try {
      const resp = await listLogs({ page: p, page_size: 50 })
      setLogs(resp.logs)
      setTotal(resp.total)
      setPage(p)
    } catch (e) {
      const err = e as Error
      message.error(`加载操作日志失败：${err.message}`)
    } finally {
      setLoading(false)
    }
  }, [message])

  useEffect(() => { load(1) }, [load])

  const columns = [
    { title: 'ID', dataIndex: 'id', width: 70 },
    {
      title: '操作', dataIndex: 'action', width: 180,
      render: (a: string) => <code style={{ fontSize: 12, color: palette.primary }}>{a}</code>,
    },
    { title: '操作者 ID', dataIndex: 'actor_id', width: 100 },
    { title: '类型', dataIndex: 'actor_type', width: 80 },
    {
      title: '目标', key: 'target', width: 140,
      render: (_: unknown, l: OperationLog) =>
        l.target_type ? `${l.target_type}:${l.target_id ?? ''}` : <span style={{ color: palette.textSecondary }}>—</span>,
    },
    {
      title: 'IP', dataIndex: 'ip', width: 130,
      render: (v?: string) => v ? <code style={{ fontSize: 12 }}>{v}</code> : <span style={{ color: palette.textSecondary }}>—</span>,
    },
    {
      title: '详情', dataIndex: 'detail', width: 200,
      render: (d?: Record<string, unknown>) =>
        d ? <code style={{ fontSize: 12, color: palette.textSecondary }}>{JSON.stringify(d)}</code> : <span style={{ color: palette.textSecondary }}>—</span>,
    },
    {
      title: '时间', dataIndex: 'created_at', width: 180,
      render: (v: string) => <span style={{ color: palette.textSecondary, fontSize: 13 }}>{new Date(v).toLocaleString('zh-CN')}</span>,
    },
  ]

  return (
    <Spin spinning={loading}>
      <Table
        columns={columns}
        dataSource={logs.map((l) => ({ ...l, key: l.id }))}
        pagination={{
          current: page,
          total,
          pageSize: 50,
          onChange: (p) => load(p),
        }}
        locale={{ emptyText: '暂无操作日志' }}
      />
    </Spin>
  )
}

function formatSize(bytes: number): string {
  if (bytes === 0) return '0 B'
  const k = 1024
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  const i = Math.floor(Math.log(bytes) / Math.log(k))
  return `${(bytes / Math.pow(k, i)).toFixed(1)} ${units[i]}`
}
