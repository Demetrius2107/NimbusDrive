import { Result, Button } from 'antd'
import { useNavigate } from 'react-router-dom'
import { palette } from '../theme'

export function NotFoundPage() {
  const navigate = useNavigate()
  return (
    <Result
      status="404"
      title={<span style={{ color: '#1e1b4b' }}>404</span>}
      subTitle={<span style={{ color: palette.textSecondary }}>页面不存在</span>}
      extra={
        <Button
          type="primary"
          onClick={() => navigate('/')}
          style={{
            background: palette.gradientPrimary,
            border: 'none',
            fontWeight: 600,
            boxShadow: '0 2px 10px rgba(99,102,241,0.3)',
          }}
        >
          返回首页
        </Button>
      }
    />
  )
}
