import axios, { AxiosError, AxiosInstance } from 'axios';

const STORAGE_KEY = 'egmcp.token';

export function getStoredToken(): string | null {
  return localStorage.getItem(STORAGE_KEY);
}

export function setStoredToken(token: string | null): void {
  if (token === null) {
    localStorage.removeItem(STORAGE_KEY);
  } else {
    localStorage.setItem(STORAGE_KEY, token);
  }
}

export const api: AxiosInstance = axios.create({
  baseURL: '/api/v1',
  timeout: 15_000,
});

// Attach JWT to every request that isn't /auth/login or /auth/refresh.
api.interceptors.request.use((config) => {
  const t = getStoredToken();
  if (t && config.headers) {
    config.headers.Authorization = `Bearer ${t}`;
  }
  return config;
});

// Surface backend error messages on the rejection chain.
api.interceptors.response.use(
  (resp) => resp,
  (err: AxiosError<{ error?: { code?: string; message?: string } }>) => {
    const code = err.response?.data?.error?.code;
    const message = err.response?.data?.error?.message ?? err.message;
    return Promise.reject(Object.assign(err, { code, message }));
  }
);

// ─────────────────────────────────────────────────────────────────────
// Typed helpers
// ─────────────────────────────────────────────────────────────────────

export type Instance = {
  slug: string;
  display_name?: string;
  enabled: boolean;
  api_keys?: string[];
  connectors: Array<{
    type: string;
    name: string;
    config: Record<string, unknown>;
  }>;
};

export type ConnectorDescriptor = {
  name: string;
  displayName: string;
  description: string;
  capabilities: string[];
  configSchema: unknown;
};

export type LoginResponse = {
  token: string;
  expires_at: string;
  username: string;
  ttl_seconds: number;
};

export const Auth = {
  async login(username: string, password: string): Promise<LoginResponse> {
    const r = await api.post<LoginResponse>('/auth/login', { username, password });
    setStoredToken(r.data.token);
    return r.data;
  },
  async me(): Promise<{ username: string; subject: string }> {
    const r = await api.get<{ username: string; subject: string }>('/me');
    return r.data;
  },
  async logout(): Promise<void> {
    setStoredToken(null);
  },
};

export const Instances = {
  async list(): Promise<Instance[]> {
    const r = await api.get<{ instances: Instance[] }>('/instances');
    return r.data.instances;
  },
  async get(slug: string): Promise<Instance> {
    const r = await api.get<Instance>(`/instances/${encodeURIComponent(slug)}`);
    return r.data;
  },
  async create(inst: Instance): Promise<Instance> {
    const r = await api.post<Instance>('/instances', inst);
    return r.data;
  },
  async replace(slug: string, inst: Instance): Promise<Instance> {
    const r = await api.put<Instance>(`/instances/${encodeURIComponent(slug)}`, inst);
    return r.data;
  },
  async delete(slug: string): Promise<void> {
    await api.delete(`/instances/${encodeURIComponent(slug)}`);
  },
  async test(slug: string): Promise<Array<{ name: string; status: 'ok' | 'fail'; error?: string }>> {
    const r = await api.post<{ results: Array<{ name: string; status: 'ok' | 'fail'; error?: string }> }>(
      `/instances/${encodeURIComponent(slug)}/test`
    );
    return r.data.results;
  },
};

export const Connectors = {
  async builtin(): Promise<ConnectorDescriptor[]> {
    const r = await api.get<{ connectors: ConnectorDescriptor[] }>('/connectors/builtin');
    return r.data.connectors;
  },
};
