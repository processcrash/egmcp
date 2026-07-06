import { Alert, Button, Card, Form, Input, Typography } from 'antd';
import { LockOutlined, UserOutlined } from '@ant-design/icons';
import { useState } from 'react';
import { Navigate, useLocation, useNavigate } from 'react-router-dom';
import { useAuth } from '../lib/auth';

const { Title } = Typography;

export function LoginPage() {
  const { user, login } = useAuth();
  const navigate = useNavigate();
  const loc = useLocation();
  const [error, setError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);

  if (user) {
    const next = (loc.state as { from?: string } | null)?.from ?? '/instances';
    return <Navigate to={next} replace />;
  }

  return (
    <div
      style={{
        display: 'grid',
        placeItems: 'center',
        minHeight: '100vh',
        background: '#f5f5f5',
      }}
    >
      <Card style={{ width: 380 }}>
        <Title level={3} style={{ marginTop: 0 }}>
          egmcp
        </Title>
        <p style={{ color: '#888' }}>Sign in to manage your MCP instances.</p>
        {error && <Alert type="error" message={error} style={{ marginBottom: 16 }} />}
        <Form
          layout="vertical"
          onFinish={async ({ username, password }: { username: string; password: string }) => {
            setError(null);
            setSubmitting(true);
            try {
              await login(username, password);
              navigate('/instances', { replace: true });
            } catch (err: unknown) {
              const msg = (err as { message?: string }).message ?? 'login failed';
              setError(msg);
            } finally {
              setSubmitting(false);
            }
          }}
        >
          <Form.Item
            name="username"
            label="Username"
            rules={[{ required: true, message: 'username is required' }]}
          >
            <Input prefix={<UserOutlined />} autoComplete="username" />
          </Form.Item>
          <Form.Item
            name="password"
            label="Password"
            rules={[{ required: true, message: 'password is required' }]}
          >
            <Input.Password prefix={<LockOutlined />} autoComplete="current-password" />
          </Form.Item>
          <Button type="primary" htmlType="submit" loading={submitting} block>
            Sign in
          </Button>
        </Form>
      </Card>
    </div>
  );
}
