import { Layout, Menu, Button, Avatar, Progress } from 'antd'
import { Outlet, useLocation, useNavigate } from 'react-router-dom'
import { FileOutlined, ShareAltOutlined, DashboardOutlined, ThunderboltOutlined, LogoutOutlined, DeleteOutlined, AuditOutlined } from '@ant-design/icons'
import { useAuthStore } from '../stores/auth'
import { useQuotaStore } from '../stores/quota'
import { useQuotaStream } from '../hooks/useQuotaStream'
import { palette } from '../theme'

const { Header, Sider, Content } = Layout

export function AppLayout() {
  const navigate = useNavigate()
  const location = useLocation()
  const { username, logout, isAdmin } = useAuthStore()
  // 建立配额推送通道：首屏 fetchMe + SSE 增量 + 降级轮询。
  // 统一在布局层建立，保证任意页面侧栏配额条都有实时数据。
  useQuotaStream()
  const { storageQuota, usedStorage } = useQuotaStore()

  const items = [
    { key: '/files', icon: <FileOutlined />, label: '文件' },
    { key: '/trash', icon: <DeleteOutlined />, label: '回收站' },
    { key: '/shares', icon: <ShareAltOutlined />, label: '分享' },
    { key: '/quota', icon: <DashboardOutlined />, label: '配额' },
    ...(isAdmin ? [{ key: '/admin', icon: <AuditOutlined />, label: '管理后台' }] : []),
  ]

  const handleLogout = () => {
    logout()
    navigate('/login', { replace: true })
  }

  // 侧栏底部迷你配额条（真实数据，由 SSE/轮询驱动）。
  const quotaPercent = storageQuota > 0 ? Math.round((usedStorage / storageQuota) * 100) : 0

  return (
    <Layout style={{ minHeight: '100vh' }}>
      <Sider
        breakpoint="lg"
        collapsedWidth="0"
        width={232}
        style={{
          background: palette.gradientSider,
          display: 'flex',
          flexDirection: 'column',
          position: 'sticky',
          top: 0,
          height: '100vh',
          overflow: 'auto',
        }}
      >
        {/* Logo 区：渐变图标 + 品牌名 */}
        <div
          style={{
            height: 60,
            display: 'flex',
            alignItems: 'center',
            gap: 10,
            padding: '0 20px',
            flexShrink: 0,
          }}
        >
          <div
            style={{
              width: 32,
              height: 32,
              borderRadius: 9,
              background: palette.gradientLogo,
              display: 'flex',
              alignItems: 'center',
              justifyContent: 'center',
              boxShadow: '0 2px 12px rgba(139, 92, 246, 0.4)',
            }}
          >
            <ThunderboltOutlined style={{ color: '#fff', fontSize: 16 }} />
          </div>
          <span style={{ color: '#fff', fontSize: 17, fontWeight: 700, letterSpacing: 0.5 }}>
            NimbusDrive
          </span>
        </div>

        <Menu
          theme="dark"
          mode="inline"
          selectedKeys={[location.pathname]}
          items={items}
          onClick={({ key }) => navigate(key)}
          style={{ background: 'transparent', borderInlineEnd: 'none', marginTop: 8, flex: 1 }}
        />

        {/* 侧栏底部迷你配额条 */}
        <div
          style={{
            padding: '16px 20px',
            borderTop: '1px solid rgba(255,255,255,0.08)',
            flexShrink: 0,
          }}
        >
          <div style={{ display: 'flex', justifyContent: 'space-between', marginBottom: 6 }}>
            <span style={{ color: palette.textOnDark, fontSize: 12, opacity: 0.7 }}>存储空间</span>
            <span style={{ color: palette.textOnDark, fontSize: 12 }}>{quotaPercent}%</span>
          </div>
          <Progress
            percent={quotaPercent}
            showInfo={false}
            size="small"
            strokeColor={{ from: palette.primary, to: palette.accent }}
            trailColor="rgba(255,255,255,0.1)"
          />
        </div>
      </Sider>

      <Layout>
        <Header
          style={{
            background: palette.bgGlass,
            backdropFilter: 'blur(12px)',
            WebkitBackdropFilter: 'blur(12px)',
            padding: '0 28px',
            display: 'flex',
            justifyContent: 'space-between',
            alignItems: 'center',
            borderBottom: '1px solid rgba(99, 102, 241, 0.08)',
            position: 'sticky',
            top: 0,
            zIndex: 10,
          }}
        >
          <span style={{ fontWeight: 600, fontSize: 15, color: '#1e1b4b' }}>个人网盘</span>
          <div style={{ display: 'flex', alignItems: 'center', gap: 12 }}>
            <Avatar
              size={30}
              style={{
                background: palette.gradientPrimary,
                fontSize: 13,
                fontWeight: 600,
              }}
            >
              {(username ?? 'U').charAt(0).toUpperCase()}
            </Avatar>
            <span style={{ color: palette.textSecondary, fontSize: 13 }}>{username ?? '未登录'}</span>
            <Button
              size="small"
              type="text"
              icon={<LogoutOutlined />}
              onClick={handleLogout}
              style={{ color: palette.textSecondary }}
            />
          </div>
        </Header>
        <Content style={{ margin: 24 }}>
          <Outlet />
        </Content>
      </Layout>
    </Layout>
  )
}
