import { Card, Empty, Typography } from 'antd';
import { useQuery } from '@tanstack/react-query';

const { Title } = Typography;

export function PluginsPage() {
  // Real plugin management arrives in M6. The list endpoint
  // already returns an empty array (see internal/api).
  const list = useQuery({
    queryKey: ['plugins'],
    queryFn: async () => {
      const r = await fetch('/api/v1/plugins', {
        headers: { Authorization: `Bearer ${localStorage.getItem('egmcp.token') ?? ''}` },
      });
      if (!r.ok) return [];
      return r.json();
    },
  });
  const plugins = (list.data as { plugins?: unknown[] })?.plugins ?? [];

  return (
    <div>
      <Title level={3} style={{ marginTop: 0 }}>
        Plugins
      </Title>
      <Card>
        {plugins.length === 0 ? (
          <Empty
            description="No third-party connectors loaded. Plugin management lands in M6 — drop a built Go plugin (.so/.dll) into the data/plugins directory and restart."
          />
        ) : (
          <pre>{JSON.stringify(plugins, null, 2)}</pre>
        )}
      </Card>
    </div>
  );
}
