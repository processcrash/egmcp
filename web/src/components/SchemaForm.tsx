import { Form, Input, InputNumber, Switch } from 'antd';
import { useMemo } from 'react';

// JSON Schema (Draft 7 / 2020-12) subset that the form supports.
// Anything outside this subset is ignored — the connector accepts
// JSON anyway, so operators can hand-edit unmodelled fields later.
export type SchemaProperty = {
  type: 'string' | 'number' | 'integer' | 'boolean' | 'object' | 'array';
  title?: string;
  description?: string;
  default?: unknown;
  enum?: unknown[];
  const?: unknown;
  format?: string;
  minimum?: number;
  maximum?: number;
  minLength?: number;
  maxLength?: number;
  pattern?: string;
  properties?: Record<string, SchemaProperty>;
  required?: string[];
  items?: SchemaProperty;
};

export type JsonSchema = {
  type?: 'object';
  title?: string;
  description?: string;
  default?: unknown;
  properties?: Record<string, SchemaProperty>;
  required?: string[];
};

const SLUG_RE = /^[a-z][a-z0-9_-]{0,31}$/;

function validatePattern(value: string, pattern: string) {
  try {
    return new RegExp(pattern).test(value);
  } catch {
    return true;
  }
}

// Render one field for a single schema property. The AntD Form
// context is assumed to be present (SchemaForm wraps the result in
// <Form>).
function FieldForProperty({
  name,
  prop,
  required,
}: {
  name: string;
  prop: SchemaProperty;
  required: boolean;
}) {
  const label = prop.title ?? name;
  const help = prop.description;

  if (prop.type === 'boolean') {
    return (
      <Form.Item
        key={name}
        name={name}
        label={label}
        valuePropName="checked"
        rules={required ? [{ required: true, message: `${label} is required` }] : []}
        extra={help}
        initialValue={prop.default as boolean | undefined}
      >
        <Switch />
      </Form.Item>
    );
  }

  if (prop.type === 'number' || prop.type === 'integer') {
    return (
      <Form.Item
        key={name}
        name={name}
        label={label}
        rules={required ? [{ required: true, message: `${label} is required` }] : []}
        extra={help}
        initialValue={prop.default as number | undefined}
      >
        <InputNumber style={{ width: '100%' }} />
      </Form.Item>
    );
  }

  if (prop.type === 'object' || prop.type === 'array') {
    // For v1 we ask the user to paste raw JSON. Building nested
    // forms adds complexity we don't need yet.
    return (
      <Form.Item
        key={name}
        name={name}
        label={label}
        rules={required ? [{ required: true, message: `${label} is required` }] : []}
        extra={help ?? 'JSON object or array'}
        initialValue={
          prop.default !== undefined
            ? JSON.stringify(prop.default, null, 2)
            : prop.type === 'object'
            ? '{}'
            : '[]'
        }
      >
        <Input.TextArea rows={4} style={{ fontFamily: 'monospace' }} />
      </Form.Item>
    );
  }

  // String + enum special case.
  const isPassword =
    prop.format === 'password' ||
    name.toLowerCase().includes('password') ||
    name.toLowerCase().includes('secret') ||
    name.toLowerCase().endsWith('_key');
  const isSlug = name.toLowerCase().includes('slug');

  const rules = [] as Array<{ [k: string]: unknown }>;
  if (required) rules.push({ required: true, message: `${label} is required` });
  if (isSlug) rules.push({ pattern: SLUG_RE, message: `must match ${SLUG_RE}` });
  if (prop.pattern) {
    rules.push({
      validator: (_: unknown, v: string) =>
        validatePattern(v ?? '', prop.pattern!) ? Promise.resolve() : Promise.reject(new Error('pattern mismatch')),
    });
  }

  if (prop.enum && prop.enum.length > 0) {
    return (
      <Form.Item key={name} name={name} label={label} rules={rules} extra={help}>
        <Input />
      </Form.Item>
    );
  }

  return (
    <Form.Item key={name} name={name} label={label} rules={rules} extra={help}>
      <Input.Password
        {...(!isPassword ? { type: 'text' as const } : {})}
        autoComplete="off"
        placeholder={prop.default !== undefined ? String(prop.default) : undefined}
      />
    </Form.Item>
  );
}

export function SchemaForm({
  schema,
  form,
  initialValues,
}: {
  schema: JsonSchema;
  // SchemaForm renders fields derived from a JSON Schema, so the
  // form values are inherently dynamic. Callers pass a FormInstance
  // produced by AntD's useForm() (without a generic).
  form: ReturnType<typeof Form.useForm>[0];
  initialValues?: Record<string, unknown>;
}) {
  const properties = useMemo(() => schema.properties ?? {}, [schema]);
  const required = useMemo(() => new Set(schema.required ?? []), [schema]);

  return (
    <Form form={form} layout="vertical" initialValues={initialValues}>
      {Object.entries(properties).map(([name, prop]) => (
        <FieldForProperty key={name} name={name} prop={prop} required={required.has(name)} />
      ))}
    </Form>
  );
}

// Values are submitted in the same flat shape the backend expects.
// String-typed JSON object/array fields are parsed back into JSON
// before being sent to the server.
export function flattenFormValues(
  schema: JsonSchema,
  values: Record<string, unknown>
): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  const props = schema.properties ?? {};
  for (const [k, v] of Object.entries(values)) {
    const prop = props[k];
    if (prop && (prop.type === 'object' || prop.type === 'array') && typeof v === 'string') {
      try {
        out[k] = JSON.parse(v);
      } catch {
        out[k] = v;
      }
    } else {
      out[k] = v;
    }
  }
  return out;
}
