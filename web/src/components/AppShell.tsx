import { Button, Layout, Menu, Space, Typography } from 'antd';
import { Link, Outlet, useLocation, useNavigate } from 'react-router-dom';
import {
  AppstoreOutlined,
  LogoutOutlined,
  PlusOutlined,
  SettingOutlined,
} from '@ant-design/icons';
import { useAuth } from '../lib/auth';

const { Header, Sider, Content } = Layout;

export function AppShell() {
  const { user, logout } = useAuth();
  const navigate = useNavigate();
  const loc = useLocation();

  const selected = loc.pathname.startsWith('/instances/new') || loc.pathname.startsWith('/instances/')
    ? 'instances'
    : 'plugins';

  return (
    <Layout style={{ minHeight: '100vh' }}>
      <Header style={{ display: 'flex', alignItems: 'center', paddingInline: 24 }}>
        <Space size="large" style={{ width: '100%', justifyContent: 'space-between' }}>
          <Typography.Title level={4} style={{ color: '#fff', margin: 0 }}>
            <Link to="/" style={{ color: '#fff' }}>egmcp</Link>
          </Typography.Title>
          <Space>
            <Typography.Text style={{ color: '#fff' }}>{user?.username}</Typography.Text>
            <Button
              size="small"
              icon={<LogoutOutlined />}
              onClick={async () => {
                await logout();
                navigate('/login');
              }}
            >
              Sign out
            </Button>
          </Space>
        </Space>
      </Header>
      <Layout>
        <Sider width={220} theme="light">
          <Menu
            mode="inline"
            selectedKeys={[selected]}
            items={[
              {
                key: 'instances',
                icon: <AppstoreOutlined />,
                label: <Link to="/instances">Instances</Link>,
              },
              {
                key: 'plugins',
                icon: <SettingOutlined />,
                label: <Link to="/plugins">Plugins</Link>,
              },
            ]}
          />
          <div style={{ position: 'absolute', bottom: 24, left: 16, right: 16 }}>
            <Button
              block
              type="primary"
              icon={<PlusOutlined />}
              onClick={() => navigate('/instances/new')}
            >
              New instance
            </Button>
          </div>
        </Sider>
        <Content style={{ padding: 24, background: '#f5f5f5' }}>
          <Outlet />
        </Content>
      </Layout>
    </Layout>
  );
}
