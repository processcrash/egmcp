import { Button, Card, Empty, Space, Table, Tag, Typography } from 'antd';
import { useQuery } from '@tanstack/react-query';
import { Link, useNavigate } from 'react-router-dom';
import { PlusOutlined, ReloadOutlined } from '@ant-design/icons';
import { useState } from 'react';
import { Instances } from '../lib/api';
import { useAuth } from '../lib/auth';

const { Title } = Typography;

export function InstanceListPage() {
  const navigate = useNavigate();
  const { user } = useAuth();
  const [polling, setPolling] = useState(false);

  const list = useQuery({
    queryKey: ['instances'],
    queryFn: Instances.list,
    refetchInterval: polling ? 4000 : false,
  });

  return (
    <div>
      <Title level={3} style={{ marginTop: 0 }}>
        MCP Instances
      </Title>
      <Card>
        <Space style={{ marginBottom: 16 }}>
          <Button type="primary" icon={<PlusOutlined />} onClick={() => navigate('/instances/new')}>
            New instance
          </Button>
          <Button
            icon={<ReloadOutlined />}
            onClick={() => list.refetch()}
            loading={list.isFetching}
          >
            Refresh
          </Button>
          <Button onClick={() => setPolling((p) => !p)}>
            {polling ? 'Stop auto-refresh' : 'Auto-refresh'}
          </Button>
          <span style={{ color: '#999' }}>{user ? `signed in as ${user.username}` : null}</span>
        </Space>
        {list.isLoading ? null : list.data?.length === 0 ? (
          <Empty description="No instances yet — create one to get started" />
        ) : (
          <Table
            rowKey="slug"
            dataSource={list.data ?? []}
            pagination={false}
            columns={[
              { title: 'Slug', dataIndex: 'slug', render: (v) => <Link to={`/instances/${v}`}>{v}</Link> },
              { title: 'Display name', dataIndex: 'display_name' },
              {
                title: 'Status',
                dataIndex: 'enabled',
                render: (v) => (v ? <Tag color="green">enabled</Tag> : <Tag>disabled</Tag>),
              },
              {
                title: 'Connectors',
                dataIndex: 'connectors',
                render: (cs: Array<{ name: string }> | undefined) =>
                  cs?.length ? (
                    <Space size={4} wrap>
                      {cs.map((c) => (
                        <Tag key={c.name}>{c.name}</Tag>
                      ))}
                    </Space>
                  ) : (
                    <span style={{ color: '#aaa' }}>—</span>
                  ),
              },
            ]}
          />
        )}
      </Card>
    </div>
  );
}
