import { Layout, Menu, theme, Button, Space } from 'antd'
import { Outlet, useLocation, useNavigate } from 'react-router-dom'
import { CloudOutlined, FileOutlined, ShareAltOutlined, DashboardOutlined } from '@ant-design/icons'
import { useAuthStore } from '../stores/auth'

const { Header, Sider, Content } = Layout

export function AppLayout() {
  const navigate = useNavigate()
  const location = useLocation()
  const { username, logout } = useAuthStore()
  const { token: themeToken } = theme.useToken()

  const items = [
    { key: '/files', icon: <FileOutlined />, label: '文件' },
    { key: '/shares', icon: <ShareAltOutlined />, label: '分享' },
    { key: '/quota', icon: <DashboardOutlined />, label: '配额' },
  ]

  const handleLogout = () => {
    logout()
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
            fontSize: 18,
            fontWeight: 600,
            display: 'flex',
            alignItems: 'center',
            justifyContent: 'center',
            gap: 8,
          }}
        >
          <CloudOutlined />
          NimbusDrive
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
          <span style={{ fontWeight: 500 }}>个人网盘</span>
          <Space>
            <span style={{ color: themeToken.colorTextSecondary }}>{username ?? '未登录'}</span>
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
