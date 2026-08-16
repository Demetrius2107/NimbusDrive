import { Card, Col, Row, Statistic } from 'antd'

export function DashboardPage() {
  // TODO: useQuery 取 /admin/dashboard/summary
  return (
    <Row gutter={[16, 16]}>
      <Col xs={24} sm={12} lg={6}>
        <Card>
          <Statistic title="用户总数" value={0} />
        </Card>
      </Col>
      <Col xs={24} sm={12} lg={6}>
        <Card>
          <Statistic title="存储总量" value={0} suffix="GB" />
        </Card>
      </Col>
      <Col xs={24} sm={12} lg={6}>
        <Card>
          <Statistic title="文件数" value={0} />
        </Card>
      </Col>
      <Col xs={24} sm={12} lg={6}>
        <Card>
          <Statistic title="活跃分享" value={0} />
        </Card>
      </Col>
    </Row>
  )
}
