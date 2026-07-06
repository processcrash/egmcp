import { useMemo, useState } from 'react';
import {
  Alert,
  Button,
  Card,
  Form,
  Input,
  Select,
  Space,
  Steps,
  Switch,
  Typography,
  message as antdMessage,
} from 'antd';
import { useMutation, useQuery } from '@tanstack/react-query';
import { useNavigate } from 'react-router-dom';
import { Connectors, Instances, type ConnectorDescriptor, type Instance } from '../lib/api';
import { SchemaForm, flattenFormValues, type JsonSchema } from '../components/SchemaForm';

const { Title } = Typography;

export function InstanceCreatePage() {
  const navigate = useNavigate();
  const [step, setStep] = useState(0);

  const connectors = useQuery({
    queryKey: ['connectors', 'builtin'],
    queryFn: Connectors.builtin,
  });

  const [basic, setBasic] = useState<Pick<Instance, 'slug' | 'display_name' | 'enabled'>>({
    slug: '',
    display_name: '',
    enabled: true,
  });
  const [pickedType, setPickedType] = useState<string | null>(null);
  const [form] = Form.useForm();

  const picked: ConnectorDescriptor | undefined = useMemo(
    () => (pickedType ? connectors.data?.find((c) => c.name === pickedType) : undefined),
    [pickedType, connectors.data]
  );

  const create = useMutation({
    mutationFn: Instances.create,
    onSuccess: (created) => {
      antdMessage.success(`Created ${created.slug}`);
      navigate(`/instances/${created.slug}`);
    },
    onError: (err: unknown) => {
      const msg = (err as { message?: string }).message ?? 'create failed';
      antdMessage.error(msg);
    },
  });

  return (
    <div>
      <Title level={3} style={{ marginTop: 0 }}>
        New MCP instance
      </Title>
      <Card>
        <Steps
          current={step}
          items={[
            { title: 'Basics' },
            { title: 'Connector type' },
            { title: 'Connector config' },
          ]}
          style={{ marginBottom: 24 }}
        />

        {step === 0 && (
          <Form
            layout="vertical"
            initialValues={basic}
            onValuesChange={(_, v) => setBasic(v)}
          >
            <Form.Item
              name="slug"
              label="Slug"
              rules={[
                { required: true, message: 'slug is required' },
                { pattern: /^[a-z][a-z0-9_-]{0,31}$/, message: 'must match the slug format' },
              ]}
              extra="Becomes the MCP URL: /mcp/{slug}. Lowercase, kebab-case."
            >
              <Input placeholder="e.g. marketing" />
            </Form.Item>
            <Form.Item name="display_name" label="Display name">
              <Input placeholder="Human-readable label" />
            </Form.Item>
            <Form.Item name="enabled" label="Enabled" valuePropName="checked">
              <Switch />
            </Form.Item>
            <Space>
              <Button type="primary" disabled={!basic.slug} onClick={() => setStep(1)}>
                Next
              </Button>
              <Button onClick={() => navigate('/instances')}>Cancel</Button>
            </Space>
          </Form>
        )}

        {step === 1 && (
          <>
            {connectors.isLoading && <Alert message="Loading connectors…" type="info" />}
            {connectors.isError && (
              <Alert
                message="Could not load connectors"
                description={(connectors.error as { message?: string })?.message}
                type="error"
              />
            )}
            <Select
              style={{ minWidth: 320 }}
              placeholder="Choose a connector type"
              loading={connectors.isLoading}
              value={pickedType ?? undefined}
              onChange={(v) => setPickedType(v)}
            >
              {(connectors.data ?? []).map((c) => (
                <Select.Option key={c.name} value={c.name}>
                  {c.displayName || c.name} — {c.description}
                </Select.Option>
              ))}
            </Select>
            <div style={{ marginTop: 24 }}>
              <Space>
                <Button onClick={() => setStep(0)}>Back</Button>
                <Button type="primary" disabled={!pickedType} onClick={() => setStep(2)}>
                  Next
                </Button>
              </Space>
            </div>
          </>
        )}

        {step === 2 && picked && (
          <ConnectorStep
            picked={picked}
            form={form}
            submitting={create.isPending}
            onBack={() => setStep(1)}
            onSubmit={(cfg: Record<string, unknown>) => {
              create.mutate({
                ...basic,
                connectors: [{ type: picked.name, name: picked.name, config: cfg }],
              });
            }}
          />
        )}
      </Card>
    </div>
  );
}

function ConnectorStep({
  picked,
  form,
  submitting,
  onBack,
  onSubmit,
}: {
  picked: ConnectorDescriptor;
  form: ReturnType<typeof Form.useForm>[0];
  submitting: boolean;
  onBack: () => void;
  onSubmit: (cfg: Record<string, unknown>) => void;
}) {
  const schema = (picked.configSchema ?? { type: 'object' }) as JsonSchema;
  return (
    <>
      <Title level={5}>
        {picked.displayName || picked.name}
      </Title>
      {picked.description && <p style={{ color: '#888' }}>{picked.description}</p>}
      <SchemaForm schema={schema} form={form} />
      <Space style={{ marginTop: 16 }}>
        <Button onClick={onBack}>Back</Button>
        <Button
          type="primary"
          loading={submitting}
          onClick={async () => {
            try {
              const values = await form.validateFields();
              onSubmit(flattenFormValues(schema, values as Record<string, unknown>));
            } catch {
              // AntD already shows per-field validation errors.
            }
          }}
        >
          Create
        </Button>
      </Space>
    </>
  );
}
