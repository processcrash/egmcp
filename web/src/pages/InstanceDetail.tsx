import {
  Alert,
  Button,
  Card,
  Descriptions,
  Popconfirm,
  Space,
  Tag,
  Typography,
  message as antdMessage,
} from 'antd';
import {
  CopyOutlined,
  DeleteOutlined,
  PlayCircleOutlined,
} from '@ant-design/icons';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Link, useNavigate, useParams } from 'react-router-dom';
import { Instances, type Instance } from '../lib/api';

const { Title } = Typography;

export function InstanceDetailPage() {
  const { slug = '' } = useParams();
  const navigate = useNavigate();
  const qc = useQueryClient();

  const q = useQuery({
    queryKey: ['instance', slug],
    queryFn: () => Instances.get(slug),
    enabled: !!slug,
  });

  const test = useMutation({
    mutationFn: () => Instances.test(slug),
  });
  const del = useMutation({
    mutationFn: () => Instances.delete(slug),
    onSuccess: () => {
      antdMessage.success('deleted');
      qc.invalidateQueries({ queryKey: ['instances'] });
      navigate('/instances');
    },
  });

  if (q.isLoading) return <p>loading…</p>;
  if (q.isError)
    return <Alert type="error" message={(q.error as { message?: string })?.message ?? 'load failed'} />;
  if (!q.data) return null;

  const inst = q.data;
  return (
    <div>
      <Title level={3} style={{ marginTop: 0 }}>
        {inst.display_name || inst.slug}{' '}
        <small style={{ color: '#888' }}>{`/${inst.slug}`}</small>
      </Title>
      <Card style={{ marginBottom: 16 }}>
        <Descriptions column={3} bordered size="small">
          <Descriptions.Item label="Slug">{inst.slug}</Descriptions.Item>
          <Descriptions.Item label="Enabled">
            {inst.enabled ? <Tag color="green">enabled</Tag> : <Tag>disabled</Tag>}
          </Descriptions.Item>
          <Descriptions.Item label="API keys">
            {inst.api_keys?.length ?? 0}{' '}
            <Button
              size="small"
              icon={<CopyOutlined />}
              style={{ marginLeft: 8 }}
              onClick={() =>
                navigator.clipboard?.writeText(JSON.stringify({ mcpServers: { [inst.slug]: { type: 'http', url: `${location.origin}/mcp/${inst.slug}` } } }, null, 2))
              }
            >
              Copy JSON
            </Button>
          </Descriptions.Item>
        </Descriptions>
      </Card>

      <Card title="Connectors" style={{ marginBottom: 16 }}>
        {inst.connectors.map((c) => (
          <Card.Grid key={c.name} hoverable={false} style={{ width: '33%', textAlign: 'left' }}>
            <Title level={5} style={{ marginTop: 0 }}>
              {c.name}{' '}
              <Tag>{c.type}</Tag>
            </Title>
            <pre
              style={{
                background: '#fafafa',
                padding: 8,
                borderRadius: 4,
                margin: 0,
                fontSize: 12,
                maxHeight: 200,
                overflow: 'auto',
              }}
            >
              {JSON.stringify(c.config, null, 2)}
            </pre>
          </Card.Grid>
        ))}
      </Card>

      <Space style={{ marginBottom: 24 }}>
        <Button
          icon={<PlayCircleOutlined />}
          loading={test.isPending}
          onClick={async () => {
            try {
              const r = await test.mutateAsync();
              const failed = r.filter((x) => x.status !== 'ok');
              if (failed.length === 0) antdMessage.success('all connectors healthy');
              else antdMessage.warning(`${failed.length} connector(s) failed`);
              qc.setQueryData(['instance-test', slug], r);
            } catch {
              /* surfaced via mutation state */
            }
          }}
        >
          Test connectors
        </Button>
        {test.data && (
          <Space size={4} wrap>
            {test.data.map((r) => (
              <Tag key={r.name} color={r.status === 'ok' ? 'green' : 'red'}>
                {r.name}: {r.status}
                {r.error ? ` — ${r.error}` : ''}
              </Tag>
            ))}
          </Space>
        )}
        <Popconfirm
          title="Delete this instance?"
          description="The YAML file will be removed and connectors will shut down."
          onConfirm={() => del.mutate()}
        >
          <Button danger icon={<DeleteOutlined />}>
            Delete
          </Button>
        </Popconfirm>
        <Link to="/instances">
          <Button>Back to list</Button>
        </Link>
      </Space>
    </div>
  );
}
