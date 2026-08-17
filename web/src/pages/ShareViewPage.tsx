import { useState, useEffect } from 'react'
import { Card, Button, Input, Space, Spin, Result, App, Typography } from 'antd'
import {
  FileOutlined, LockOutlined, DownloadOutlined, LinkOutlined, ThunderboltOutlined,
} from '@ant-design/icons'
import { useParams } from 'react-router-dom'
import { getShare, validateShare, type Share } from '../lib/shares'
import { downloadFile } from '../lib/downloader'
import { palette } from '../theme'

const { Title, Text } = Typography

export function ShareViewPage() {
  const { id } = useParams<{ id: string }>()
  const { message } = App.useApp()
  const [share, setShare] = useState<Share | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [password, setPassword] = useState('')
  const [validated, setValidated] = useState(false)
  const [validating, setValidating] = useState(false)
  const [downloading, setDownloading] = useState(false)

  useEffect(() => {
    if (!id) return
    setLoading(true)
    getShare(id)
      .then((s) => {
        setShare(s)
        // 无密码分享自动视为已校验
        if (!s.has_password) {
          setValidated(true)
        }
      })
      .catch((e: Error) => {
        setError(e.message || '分享不存在或已失效')
      })
      .finally(() => setLoading(false))
  }, [id])

  const handleValidate = async () => {
    if (!id) return
    setValidating(true)
    try {
      await validateShare(id, password)
      setValidated(true)
      message.success('校验通过')
    } catch (e) {
      const err = e as Error
      message.error(err.message || '密码错误或分享已失效')
    } finally {
      setValidating(false)
    }
  }

  const handleDownload = async () => {
    if (!share) return
    setDownloading(true)
    try {
      await downloadFile({ fileId: share.file_id, fileName: `shared-${share.file_id}` })
      message.success('下载已开始')
    } catch (e) {
      const err = e as Error
      message.error(`下载失败：${err.message}`)
    } finally {
      setDownloading(false)
    }
  }

  if (loading) {
    return (
      <div style={{ minHeight: '100vh', display: 'flex', alignItems: 'center', justifyContent: 'center' }}>
        <Spin size="large" />
      </div>
    )
  }

  if (error || !share) {
    return (
      <div style={{ minHeight: '100vh', display: 'flex', alignItems: 'center', justifyContent: 'center', background: palette.gradientPage }}>
        <Card style={{ maxWidth: 480, width: '100%', borderRadius: 16 }}>
          <Result
            status="404"
            title="分享不存在"
            subTitle={error || '链接可能已过期或被取消'}
          />
        </Card>
      </div>
    )
  }

  return (
    <div style={{
      minHeight: '100vh',
      background: palette.gradientPage,
      display: 'flex',
      alignItems: 'center',
      justifyContent: 'center',
      padding: 24,
    }}>
      <Card
        style={{ maxWidth: 520, width: '100%', borderRadius: 16, boxShadow: '0 8px 32px rgba(99,102,241,0.12)' }}
      >
        <Space direction="vertical" size="large" style={{ width: '100%' }}>
          {/* 品牌 */}
          <div style={{ display: 'flex', alignItems: 'center', gap: 10, justifyContent: 'center' }}>
            <div style={{
              width: 36, height: 36, borderRadius: 10,
              background: palette.gradientLogo,
              display: 'flex', alignItems: 'center', justifyContent: 'center',
              boxShadow: '0 2px 12px rgba(139,92,246,0.4)',
            }}>
              <ThunderboltOutlined style={{ color: '#fff', fontSize: 18 }} />
            </div>
            <span style={{ fontWeight: 700, fontSize: 18, color: '#1e1b4b' }}>NimbusDrive</span>
          </div>

          <Title level={4} style={{ textAlign: 'center', marginBottom: 0, color: '#1e1b4b' }}>
            <LinkOutlined style={{ color: palette.primary, marginRight: 8 }} />
            有人向你分享了文件
          </Title>

          {/* 文件信息 */}
          <Card size="small" style={{ background: 'rgba(99,102,241,0.04)', borderRadius: 10 }}>
            <Space>
              <FileOutlined style={{ fontSize: 20, color: palette.primary }} />
              <Text strong>文件 ID: {share.file_id}</Text>
            </Space>
          </Card>

          {/* 元信息 */}
          <Space size="large" wrap>
            {share.has_password && (
              <Space>
                <LockOutlined style={{ color: palette.accent }} />
                <Text type="secondary">需要密码</Text>
              </Space>
            )}
            <Text type="secondary">
              访问次数: {share.access_count}{share.max_access != null ? ` / ${share.max_access}` : ' / ∞'}
            </Text>
            {share.expires_at && (
              <Text type="secondary">
                过期: {new Date(share.expires_at).toLocaleString('zh-CN')}
              </Text>
            )}
          </Space>

          {/* 密码校验或下载 */}
          {!validated ? (
            <Space direction="vertical" size="middle" style={{ width: '100%' }}>
              <Input.Password
                prefix={<LockOutlined />}
                placeholder="输入提取密码"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                onPressEnter={handleValidate}
                size="large"
              />
              <Button
                type="primary"
                size="large"
                block
                loading={validating}
                onClick={handleValidate}
                style={{ background: palette.gradientPrimary, border: 'none', fontWeight: 600 }}
              >
                验证并访问
              </Button>
            </Space>
          ) : (
            <Button
              type="primary"
              size="large"
              block
              icon={<DownloadOutlined />}
              loading={downloading}
              onClick={handleDownload}
              style={{ background: palette.gradientPrimary, border: 'none', fontWeight: 600, height: 48 }}
            >
              下载文件
            </Button>
          )}
        </Space>
      </Card>
    </div>
  )
}
