import { Layout, Menu, theme, Button, Space } from 'antd'
import { Outlet, useLocation, useNavigate } from 'react-router-dom'
import {
  DashboardOutlined,
  TeamOutlined,
  FileOutlined,
  DatabaseOutlined,
  AuditOutlined,
} from '@ant-design/icons'

const { Header, Sider, Content } = Layout

export function AdminLayout() {
  const navigate = useNavigate()
  const location = useLocation()
  const { token: themeToken } = theme.useToken()

  const items = [
    { key: '/dashboard', icon: <DashboardOutlined />, label: '总览' },
    { key: '/users', icon: <TeamOutlined />, label: '用户管理' },
    { key: '/files', icon: <FileOutlined />, label: '文件管理' },
    { key: '/hashes', icon: <DatabaseOutlined />, label: '哈希池' },
    { key: '/logs', icon: <AuditOutlined />, label: '操作日志' },
  ]

  const handleLogout = () => {
    localStorage.removeItem('nimbus_admin_token')
    navigate('/login', { replace: true })
  }

  return (
    <Layout style={{ minHeight: '100vh' }}>
      <Sider breakpoint="lg" collapsedWidth="0">
        <div
          style={{
            height: 48,
            margin: 16,
            color: '#fff',
            fontSize: 16,
            fontWeight: 600,
            display: 'flex',
            alignItems: 'center',
            justifyContent: 'center',
          }}
        >
          NimbusDrive 管理后台
        </div>
        <Menu
          theme="dark"
          mode="inline"
          selectedKeys={[location.pathname]}
          items={items}
          onClick={({ key }) => navigate(key)}
        />
      </Sider>
      <Layout>
        <Header
          style={{
            background: themeToken.colorBgContainer,
            padding: '0 24px',
            display: 'flex',
            justifyContent: 'space-between',
            alignItems: 'center',
          }}
        >
          <span style={{ fontWeight: 500 }}>管理控制台</span>
          <Space>
            <Button size="small" onClick={handleLogout}>
              退出
            </Button>
          </Space>
        </Header>
        <Content style={{ margin: 24 }}>
          <Outlet />
        </Content>
      </Layout>
    </Layout>
  )
}
