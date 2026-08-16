import { Card, Form, Input, Button, Typography, App } from 'antd'
import { useNavigate } from 'react-router-dom'

export function LoginPage() {
  const navigate = useNavigate()
  const { message } = App.useApp()

  const onFinish = (_values: { username: string; password: string }) => {
    // TODO: POST /auth/admin/login
    localStorage.setItem('nimbus_admin_token', 'skeleton-admin-token')
    message.success('登录成功（骨架态）')
    navigate('/dashboard', { replace: true })
  }

  return (
    <div
      style={{
        minHeight: '100vh',
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'center',
        background: '#f0f2f5',
      }}
    >
      <Card style={{ width: 380 }}>
        <Typography.Title level={3} style={{ textAlign: 'center', marginBottom: 24 }}>
          管理后台
        </Typography.Title>
        <Form layout="vertical" onFinish={onFinish}>
          <Form.Item label="管理员账号" name="username" rules={[{ required: true }]}>
            <Input />
          </Form.Item>
          <Form.Item label="密码" name="password" rules={[{ required: true }]}>
            <Input.Password />
          </Form.Item>
          <Form.Item style={{ marginBottom: 0 }}>
            <Button type="primary" htmlType="submit" block>
              登录
            </Button>
          </Form.Item>
        </Form>
      </Card>
    </div>
  )
}
