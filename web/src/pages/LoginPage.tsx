import { Card, Form, Input, Button, Typography, App } from 'antd'
import { ThunderboltOutlined, UserOutlined, LockOutlined } from '@ant-design/icons'
import { useNavigate } from 'react-router-dom'
import { useAuthStore } from '../stores/auth'
import { http } from '../lib/api'
import { palette } from '../theme'

interface LoginResponse {
  token: string
  username: string
  is_admin: boolean
}

export function LoginPage() {
  const navigate = useNavigate()
  const { setAuth } = useAuthStore()
  const { message } = App.useApp()

  const onFinish = async (values: { username: string; password: string }) => {
    try {
      const resp = await http.post<{ code: string; message: string; data: LoginResponse }>(
        '/auth/login',
        values,
      )
      const { token, username, is_admin } = resp.data.data
      setAuth(token, username, is_admin)
      message.success('登录成功')
      navigate('/files', { replace: true })
    } catch (e) {
      const err = e as Error
      message.error(`登录失败：${err.message}`)
    }
  }

  return (
    <div
      style={{
        minHeight: '100vh',
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'center',
        background: palette.gradientLoginBg,
        position: 'relative',
        overflow: 'hidden',
      }}
    >
      {/* 装饰性光斑 */}
      <div
        style={{
          position: 'absolute',
          width: 400,
          height: 400,
          borderRadius: '50%',
          background: 'radial-gradient(circle, rgba(167,139,250,0.3) 0%, transparent 70%)',
          top: '-10%',
          right: '-5%',
        }}
      />
      <div
        style={{
          position: 'absolute',
          width: 300,
          height: 300,
          borderRadius: '50%',
          background: 'radial-gradient(circle, rgba(59,130,246,0.25) 0%, transparent 70%)',
          bottom: '-10%',
          left: '-5%',
        }}
      />

      <Card
        style={{
          width: 400,
          background: 'rgba(255,255,255,0.92)',
          backdropFilter: 'blur(20px)',
          WebkitBackdropFilter: 'blur(20px)',
          border: '1px solid rgba(255,255,255,0.5)',
          boxShadow: '0 8px 32px rgba(30,27,75,0.25)',
          borderRadius: 18,
          position: 'relative',
          zIndex: 1,
        }}
      >
        {/* Logo */}
        <div style={{ display: 'flex', flexDirection: 'column', alignItems: 'center', marginBottom: 28 }}>
          <div
            style={{
              width: 52,
              height: 52,
              borderRadius: 14,
              background: palette.gradientPrimary,
              display: 'flex',
              alignItems: 'center',
              justifyContent: 'center',
              boxShadow: '0 4px 16px rgba(99,102,241,0.35)',
              marginBottom: 12,
            }}
          >
            <ThunderboltOutlined style={{ color: '#fff', fontSize: 24 }} />
          </div>
          <Typography.Title level={3} style={{ margin: 0, fontWeight: 700, color: '#1e1b4b' }}>
            NimbusDrive
          </Typography.Title>
          <Typography.Text style={{ color: palette.textSecondary, fontSize: 13 }}>
            你的云端文件，触手可及
          </Typography.Text>
        </div>

        <Form layout="vertical" onFinish={onFinish} initialValues={{ username: '', password: '' }}>
          <Form.Item label="用户名" name="username" rules={[{ required: true, message: '请输入用户名' }]}>
            <Input prefix={<UserOutlined style={{ color: '#c4b5fd' }} />} placeholder="用户名" size="large" />
          </Form.Item>
          <Form.Item label="密码" name="password" rules={[{ required: true, message: '请输入密码' }]}>
            <Input.Password prefix={<LockOutlined style={{ color: '#c4b5fd' }} />} placeholder="密码" size="large" />
          </Form.Item>
          <Form.Item style={{ marginBottom: 0, marginTop: 8 }}>
            <Button
              type="primary"
              htmlType="submit"
              block
              size="large"
              style={{
                background: palette.gradientPrimary,
                border: 'none',
                fontWeight: 600,
                height: 44,
                boxShadow: '0 4px 14px rgba(99,102,241,0.35)',
              }}
            >
              登录
            </Button>
          </Form.Item>
        </Form>
      </Card>
    </div>
  )
}
