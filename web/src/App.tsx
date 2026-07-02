import { Button, Card, Space, Typography } from 'antd';
import { ApiOutlined } from '@ant-design/icons';

const { Title, Paragraph } = Typography;

export default function App() {
  const checkHealth = () => {
    fetch('/healthz')
      .then((r) => r.json())
      .then(console.log)
      .catch(console.error);
  };

  return (
    <div style={{ padding: 32, maxWidth: 720, margin: '0 auto' }}>
      <Title level={2}>
        <Space>
          <ApiOutlined />
          egmcp
        </Space>
      </Title>
      <Paragraph type="secondary">
        Admin console scaffold — login, instance management and connector
        configuration land in M1/M2.
      </Paragraph>
      <Card title="Status">
        <Button type="primary" onClick={checkHealth}>
          Check backend health
        </Button>
      </Card>
    </div>
  );
}
