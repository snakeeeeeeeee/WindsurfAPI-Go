import React, { useMemo, useState } from "react";
import { createRoot } from "react-dom/client";
import { QueryClient, QueryClientProvider, useMutation, useQuery } from "@tanstack/react-query";
import { RouterProvider, createRootRoute, createRoute, createRouter, useNavigate, useRouterState } from "@tanstack/react-router";
import {
  flexRender,
  getCoreRowModel,
  getPaginationRowModel,
  getSortedRowModel,
  useReactTable,
  type ColumnDef,
  type Header,
  type RowSelectionState,
  type SortingState,
  type Table
} from "@tanstack/react-table";
import {
  Activity,
  AlertTriangle,
  BarChart3,
  CheckCircle2,
  Clock3,
  Database,
  Download,
  Gauge,
  KeyRound,
  Layers3,
  ListChecks,
  LogIn,
  LogOut,
  Network,
  Play,
  RefreshCcw,
  Router,
  Save,
  Search,
  Server,
  Settings,
  ShieldCheck,
  Moon,
  Sun,
  Trash2,
  Upload,
  UsersRound
} from "lucide-react";
import { Button } from "./components/ui/button";
import { ConfirmDialog } from "./components/ui/confirm-dialog";
import { Input } from "./components/ui/input";
import { Select } from "./components/ui/select";
import { Switch } from "./components/ui/switch";
import { Textarea } from "./components/ui/textarea";
import "./styles.css";

const queryClient = new QueryClient();
const apiKeyStorageKey = "windsurfapi.dashboard.apiKey";
const dashboardPasswordStorageKey = "windsurfapi.dashboard.password";
const themeStorageKey = "windsurfapi.dashboard.theme";

type DebugAccount = {
  id: number;
  email: string;
  user_id?: string;
  proxy_url?: string;
  proxy_url_set?: boolean;
  tier: string;
  plan_name?: string;
  model_config_count?: number;
  enabled: boolean;
  banned: boolean;
  notes?: string;
  token_set: boolean;
  inflight: number;
  rpm_used: number;
  rpm_limit: number;
  quota_daily_percent?: number;
  quota_weekly_percent?: number;
  quota_daily_reset_at?: string;
  quota_weekly_reset_at?: string;
  quota_score: number;
  prompt?: CreditBucket;
  flex?: CreditBucket;
  overage_balance?: number;
  plan_start?: string;
  plan_end?: string;
  health_checked_at?: string;
  rate_limited_until?: string;
  model_cooldowns?: Record<string, string>;
  model_breakers?: Record<string, string>;
  recent_errors?: Record<string, number>;
  blocked_models?: string[];
};

type CreditBucket = {
  limit?: number;
  used?: number;
  remaining?: number;
};

type SchedulerEvent = {
  time: string;
  account_id: number;
  model: string;
  class: string;
  message: string;
  cooldown_ms?: number;
};

type HealthSummary = {
  last_run_at?: string;
  last_duration_ms?: number;
  checked?: number;
  ok?: number;
  invalid?: number;
  failed?: number;
  last_error?: string;
};

type AccountsSnapshot = {
  accounts?: DebugAccount[];
  counts?: Record<string, number>;
  events?: SchedulerEvent[];
  health?: HealthSummary;
  coordinator?: Record<string, unknown>;
};

type DirectSnapshot = {
  protocol?: string;
  hosts?: string[];
  proxy_clients?: number;
  successes?: number;
  failures?: number;
  last_host?: string;
  last_proxy?: string;
  last_model?: string;
  last_error?: string;
  last_latency_ms?: number;
};

type ProxyEntry = {
  id: string;
  masked_url: string;
  enabled: boolean;
  inflight: number;
  successes: number;
  failures: number;
  last_error?: string;
  last_test_status?: string;
  last_test_latency_ms?: number;
  last_test_at?: string;
  cooldown_until?: string;
};

type ProxyBinding = {
  account_id: number;
  provider?: string;
  protocol?: string;
  host?: string;
  port?: number;
  username?: string;
  session_id?: string;
  egress_ip?: string;
  country?: string;
  region?: string;
  city?: string;
  isp_org?: string;
  status?: string;
  expires_at?: string;
  remaining_ms?: number;
  last_verified_at?: string;
  verify_error?: string;
  fail_count?: number;
  has_password?: boolean;
  masked_url?: string;
  created_at?: string;
  updated_at?: string;
};

type ProxyBindingSummary = {
  bound?: number;
  expiring_soon?: number;
  failed?: number;
  suspended?: number;
  unbound?: number;
};

type ProxySnapshot = {
  enabled?: boolean;
  persistent?: boolean;
  default?: string;
  rotate_on_error?: boolean;
  test_url?: string;
  account_binding?: boolean;
  auto_bind_new_accounts?: boolean;
  renew_before_ms?: number;
  max_bind_retries?: number;
  worker_interval_ms?: number;
  worker_batch_size?: number;
  worker_concurrency?: number;
  provider?: string;
  protocol?: string;
  host?: string;
  port?: number;
  username_template?: string;
  password_set?: boolean;
  region?: string;
  state?: string;
  ttl_minutes?: number;
  entries?: ProxyEntry[];
  bindings?: ProxyBinding[];
  summary?: ProxyBindingSummary;
};

type SchedulerSnapshot = {
  events?: SchedulerEvent[];
  health?: HealthSummary;
  coordinator?: Record<string, unknown>;
  reuse?: Record<string, unknown>;
  entries?: unknown[];
};

type CacheSnapshot = {
  enabled?: boolean;
  entries?: number;
  stats?: Record<string, number>;
  items?: unknown[];
};

type LSSnapshot = {
  entries?: unknown[];
  max_instances?: number;
};

type OverviewSnapshot = {
  counts?: Record<string, number>;
  rpm_used?: number;
  rpm_limit?: number;
  inflight?: number;
  avg_quota?: number;
  health?: HealthSummary;
  direct?: DirectSnapshot;
  coordinator?: Record<string, unknown>;
};

type DashboardModel = {
  id: string;
  provider?: string;
  model_uid?: string;
  model_enum?: number;
  credit?: number;
  family?: string;
  display_name?: string;
  visible?: boolean;
  deprecated?: boolean;
  supported?: boolean;
  direct_supported?: boolean;
  unsupported_reason?: string;
  notes?: string;
};

type ModelsSnapshot = {
  scope?: string;
  data?: DashboardModel[];
};

type RequestStats = {
  total?: number;
  success?: number;
  failed?: number;
  retried?: number;
  error_rate?: number;
  p50_ms?: number;
  p95_ms?: number;
  p99_ms?: number;
  by_route?: Record<string, number>;
  by_class?: Record<string, number>;
  by_account?: Record<string, number>;
  latency_buckets?: Record<string, number>;
  usage?: { input?: number; output?: number; cache_read?: number; total?: number };
  cache?: { reuse_hits?: number; reuse_hit_rate?: number; cache_read_tokens?: number; cache_read_ratio?: number };
  stream_count?: number;
  tool_call_count?: number;
  recent?: RequestEvent[];
};

type LogsSnapshot = {
  requests?: RequestEvent[];
  events?: SchedulerEvent[];
};

type RequestEvent = {
  time: string;
  req_id: string;
  route: string;
  model: string;
  caller_key_hash?: string;
  account_id?: number;
  attempt: number;
  status: string;
  http_status: number;
  error_class?: string;
  error?: string;
  retry: boolean;
  stream: boolean;
  latency_ms: number;
  send_ms: number;
  usage_input: number;
  usage_output: number;
  usage_cache_read?: number;
  tool_call_count: number;
  reuse_hit?: boolean;
  reuse_miss_reason?: string;
};

type AvailabilityRow = {
  type: "cooldown" | "breaker" | "error";
  account_id: number;
  email: string;
  model: string;
  value: string;
};

type ProbeResponse = {
  success?: boolean;
  model?: string;
  total?: number;
  results?: ProbeResult[];
  account_id?: number;
  email?: string;
  class?: string;
  error?: string;
  text?: string;
  elapsed_ms?: number;
};

type ProbeResult = {
  success?: boolean;
  account_id?: number;
  email?: string;
  model?: string;
  class?: string;
  error?: string;
  text?: string;
  elapsed_ms?: number;
};

type RuntimeConfigSnapshot = {
  server?: { port?: number; api_key_count?: number; max_request_body_bytes?: number };
  sqlite?: { path?: string };
  redis?: { addr?: string; db?: number; password_set?: boolean };
  chat?: { backend?: string };
  direct?: { hosts?: string[]; timeout_seconds?: number };
  health?: {
    enabled?: boolean;
    interval_seconds?: number;
    timeout_seconds?: number;
    mark_invalid_banned?: boolean;
    check_model_configs?: boolean;
    ready_require_check?: boolean;
    model?: string;
  };
  scheduler?: { redis_enabled?: boolean; redis_fail_closed?: boolean; max_inflight_per_account?: number; reservation_ttl_seconds?: number };
  usage?: {
    virtual_cache?: {
      enabled?: boolean;
      mode?: string;
      default_ttl?: string;
      uncached_input_tokens?: number;
      min_input_tokens?: number;
      max_input_tokens?: number;
      warmup_tokens?: number;
      min_creation_tokens?: number;
      max_creation_tokens?: number;
      creation_jitter_ratio?: number;
      burst_every_turns?: number;
      burst_min_tokens?: number;
      burst_max_tokens?: number;
    };
  };
  dashboard?: { enabled?: boolean; port?: number; password_set?: boolean };
  proxy?: {
    default?: string;
    dynamic?: string[];
    rotate_on_error?: boolean;
    allow_private?: boolean;
    test_url?: string;
    cooldown_seconds?: number;
    account_binding?: boolean;
    auto_bind_new_accounts?: boolean;
    renew_before_ms?: number;
    max_bind_retries?: number;
    worker_interval_ms?: number;
    worker_batch_size?: number;
    worker_concurrency?: number;
    provider?: string;
    protocol?: string;
    host?: string;
    port?: number;
    username_template?: string;
    password_set?: boolean;
    password?: string;
    region?: string;
    state?: string;
    ttl_minutes?: number;
  };
  log?: { level?: string };
  security?: {
    api_keys?: SecretStatus;
    dashboard_password?: SecretStatus;
    redis_password?: SecretStatus;
  };
  secrets?: {
    api_keys?: string[];
    dashboard_password?: string;
    redis_password?: string;
  };
};

type SecretStatus = {
  set?: boolean;
  safe?: boolean;
  source?: string;
  environment?: string;
  message?: string;
};

type AccountFormState = {
  email: string;
  token: string;
  tier: string;
  proxy_url: string;
  notes: string;
  enabled: boolean;
  banned: boolean;
};

type BulkImportResult = {
  imported?: number;
  failed?: number;
  total?: number;
  warnings?: string[];
};

type AuthState = {
  apiKey: string;
  dashboardPassword: string;
};

type LoginMode = "password" | "apiKey";

type LogFilters = {
  q: string;
  route: string;
  status: string;
  errorClass: string;
  model: string;
  accountID: string;
  stream: string;
  retry: string;
};

type DashboardView =
  | "overview"
  | "accounts"
  | "scheduler"
  | "availability"
  | "models"
  | "proxy"
  | "requests"
  | "settings"
  | "legacy";

const rootRoute = createRootRoute({ component: App });
const routeTree = rootRoute.addChildren([
  createRoute({ getParentRoute: () => rootRoute, path: "/" }),
  createRoute({ getParentRoute: () => rootRoute, path: "accounts" }),
  createRoute({ getParentRoute: () => rootRoute, path: "scheduler" }),
  createRoute({ getParentRoute: () => rootRoute, path: "availability" }),
  createRoute({ getParentRoute: () => rootRoute, path: "models" }),
  createRoute({ getParentRoute: () => rootRoute, path: "proxy" }),
  createRoute({ getParentRoute: () => rootRoute, path: "requests" }),
  createRoute({ getParentRoute: () => rootRoute, path: "settings" }),
  createRoute({ getParentRoute: () => rootRoute, path: "legacy" }),
  createRoute({ getParentRoute: () => rootRoute, path: "login" })
]);
const router = createRouter({ routeTree, basepath: "/dashboard" });

async function fetchJSON<T>(path: string, auth: AuthState): Promise<T> {
  if (!auth.apiKey && !auth.dashboardPassword) throw new Error("dashboard password or API key required");
  const response = await fetch(path, { headers: authHeaders(auth) });
  return parseJSONResponse<T>(response);
}

async function sendJSON<T>(path: string, auth: AuthState, method: string, body?: unknown): Promise<T> {
  if (!auth.apiKey && !auth.dashboardPassword) throw new Error("dashboard password or API key required");
  const response = await fetch(path, {
    method,
    headers: { ...authHeaders(auth), "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body)
  });
  return parseJSONResponse<T>(response);
}

function authHeaders(auth: AuthState): Record<string, string> {
  if (auth.dashboardPassword) {
    return { "X-Dashboard-Password": auth.dashboardPassword };
  }
  return { Authorization: `Bearer ${auth.apiKey}` };
}

async function parseJSONResponse<T>(response: Response): Promise<T> {
  const text = await response.text();
  const data = text ? JSON.parse(text) : {};
  if (!response.ok) {
    const message = data?.error?.message || data?.error || `${response.status} ${response.statusText}`;
    throw new Error(message);
  }
  return data;
}

function App() {
  const [apiKey, setApiKey] = useState(() => sessionStorage.getItem(apiKeyStorageKey) || "");
  const [dashboardPassword, setDashboardPassword] = useState(() => sessionStorage.getItem(dashboardPasswordStorageKey) || "");
  const [loginMode, setLoginMode] = useState<LoginMode>("password");
  const [loginSecret, setLoginSecret] = useState("");
  const [loginError, setLoginError] = useState("");
  const [theme, setTheme] = useState<"dark" | "light">(() => (localStorage.getItem(themeStorageKey) === "light" ? "light" : "dark"));
  const navigate = useNavigate();
  const currentPath = useRouterState({ select: (state) => state.location.pathname });
  const activeView = useRouterState({ select: (state) => dashboardViewFromPath(state.location.pathname) });
  const [query, setQuery] = useState("");
  const [logFilters, setLogFilters] = useState<LogFilters>(() => emptyLogFilters());
  const [proxyDraft, setProxyDraft] = useState("");
  const [selectedID, setSelectedID] = useState<number | null>(null);
  const [accountSelection, setAccountSelection] = useState<RowSelectionState>({});
  const [proxyAccountSelection, setProxyAccountSelection] = useState<RowSelectionState>({});
  const [clearCacheOpen, setClearCacheOpen] = useState(false);
  const [streamingLogs, setStreamingLogs] = useState(false);
  const [showAllModels, setShowAllModels] = useState(false);
  const [toast, setToast] = useState("");
  const authState = useMemo(() => ({ apiKey, dashboardPassword }), [apiKey, dashboardPassword]);
  const authEnabled = apiKey.length > 0 || dashboardPassword.length > 0;
  React.useEffect(() => {
    document.documentElement.dataset.theme = theme;
    localStorage.setItem(themeStorageKey, theme);
  }, [theme]);
  React.useEffect(() => {
    localStorage.removeItem(apiKeyStorageKey);
    localStorage.removeItem(dashboardPasswordStorageKey);
  }, []);
  React.useEffect(() => {
    const path = currentPath.replace(/^\/dashboard\/?/, "").replace(/^\//, "");
    if (!authEnabled && path !== "login") {
      void navigate({ to: "/login" });
    }
    if (authEnabled && path === "login") {
      void navigate({ to: "/" });
    }
  }, [authEnabled, currentPath, navigate]);

  const loginMutation = useMutation({
    mutationFn: async (payload: { mode: LoginMode; secret: string }) => {
      const secret = payload.secret.trim();
      if (!secret) throw new Error(payload.mode === "password" ? "请输入控制台密码" : "请输入接口密钥");
      const auth = payload.mode === "password" ? { apiKey: "", dashboardPassword: secret } : { apiKey: secret, dashboardPassword: "" };
      await fetchJSON<OverviewSnapshot>("/dashboard/api/overview", auth);
      return auth;
    },
    onSuccess: (auth) => {
      setLoginError("");
      setApiKey(auth.apiKey);
      setDashboardPassword(auth.dashboardPassword);
      if (auth.apiKey) sessionStorage.setItem(apiKeyStorageKey, auth.apiKey);
      else sessionStorage.removeItem(apiKeyStorageKey);
      if (auth.dashboardPassword) sessionStorage.setItem(dashboardPasswordStorageKey, auth.dashboardPassword);
      else sessionStorage.removeItem(dashboardPasswordStorageKey);
      setLoginSecret("");
      void navigate({ to: "/" });
      queryClient.clear();
    },
    onError: (error: Error) => setLoginError(humanizeDashboardError(error.message))
  });
  const logout = () => {
    setApiKey("");
    setDashboardPassword("");
    sessionStorage.removeItem(apiKeyStorageKey);
    sessionStorage.removeItem(dashboardPasswordStorageKey);
    queryClient.clear();
    setToast("");
    setLoginError("");
    void navigate({ to: "/" });
  };

  const overview = useQuery({
    queryKey: ["overview", authState],
    queryFn: () => fetchJSON<OverviewSnapshot>("/dashboard/api/overview", authState),
    enabled: authEnabled,
    refetchInterval: 5000
  });
  const accounts = useQuery({
    queryKey: ["accounts", authState],
    queryFn: () => fetchJSON<AccountsSnapshot>("/dashboard/api/accounts", authState),
    enabled: authEnabled,
    refetchInterval: 5000
  });
  const direct = useQuery({
    queryKey: ["direct", authState],
    queryFn: () => fetchJSON<DirectSnapshot>("/dashboard/api/direct", authState),
    enabled: authEnabled,
    refetchInterval: 5000
  });
  const proxy = useQuery({
    queryKey: ["proxy", authState],
    queryFn: () => fetchJSON<ProxySnapshot>("/dashboard/api/proxy", authState),
    enabled: authEnabled,
    refetchInterval: 10000
  });
  const scheduler = useQuery({
    queryKey: ["scheduler", authState],
    queryFn: () => fetchJSON<SchedulerSnapshot>("/dashboard/api/scheduler", authState),
    enabled: authEnabled,
    refetchInterval: 5000
  });
  const cache = useQuery({
    queryKey: ["cache", authState],
    queryFn: () => fetchJSON<CacheSnapshot>("/dashboard/api/cache", authState),
    enabled: authEnabled,
    refetchInterval: 10000
  });
  const legacy = useQuery({
    queryKey: ["ls", authState],
    queryFn: () => fetchJSON<LSSnapshot>("/dashboard/api/ls", authState),
    enabled: authEnabled,
    refetchInterval: 10000
  });
  const models = useQuery({
    queryKey: ["models", authState, showAllModels],
    queryFn: () => fetchJSON<ModelsSnapshot>(`/dashboard/api/models${showAllModels ? "?scope=all" : ""}`, authState),
    enabled: authEnabled,
    refetchInterval: 30000
  });
  const stats = useQuery({
    queryKey: ["stats", authState],
    queryFn: () => fetchJSON<RequestStats>("/dashboard/api/stats", authState),
    enabled: authEnabled,
    refetchInterval: 5000
  });
  const logs = useQuery({
    queryKey: ["logs", authState, logFilters],
    queryFn: () => fetchJSON<LogsSnapshot>(`/dashboard/api/logs?${logFilterParams(logFilters, "100").toString()}`, authState),
    enabled: authEnabled,
    refetchInterval: 5000
  });
  const runtimeConfig = useQuery({
    queryKey: ["runtime-config", authState],
    queryFn: () => fetchJSON<RuntimeConfigSnapshot>("/dashboard/api/config", authState),
    enabled: authEnabled,
    refetchInterval: 30000
  });

  React.useEffect(() => {
    if (!streamingLogs || !dashboardPassword) return;
    const params = logFilterParams(logFilters, "25");
    params.set("dashboard_password", dashboardPassword);
    const source = new EventSource(`/dashboard/api/logs/stream?${params.toString()}`);
    source.addEventListener("request", () => {
      void logs.refetch();
      void stats.refetch();
    });
    source.onerror = () => {
      setToast("日志流已断开");
      setStreamingLogs(false);
      source.close();
    };
    return () => source.close();
  }, [streamingLogs, dashboardPassword, logFilters, logs, stats]);

  const refreshAll = () => {
    void overview.refetch();
    void accounts.refetch();
    void direct.refetch();
    void proxy.refetch();
    void scheduler.refetch();
    void cache.refetch();
    void legacy.refetch();
    void models.refetch();
    void stats.refetch();
    void logs.refetch();
    void runtimeConfig.refetch();
  };
  const navigateView = (view: DashboardView) => {
    void navigate({ to: dashboardViewPath(view) });
    window.scrollTo({ top: 0, behavior: "smooth" });
  };
  const mutationOptions = {
    onSuccess: () => {
      setToast("已保存");
      refreshAll();
    },
    onError: (error: Error) => setToast(error.message)
  };
  const importAccount = useMutation({
    mutationFn: (payload: AccountFormState) =>
      sendJSON("/auth/accounts", authState, "POST", {
        email: payload.email,
        token: payload.token,
        tier: payload.tier,
      proxy_url: payload.proxy_url,
        notes: payload.notes,
        enabled: payload.enabled,
        banned: payload.banned
      }),
    ...mutationOptions
  });
  const importAccountText = useMutation({
    mutationFn: (text: string) => sendJSON<BulkImportResult>("/dashboard/api/import-accounts", authState, "POST", { text }),
    onSuccess: (result) => {
      const warningText = result.warnings?.length ? `，${result.warnings.length} 条警告` : "";
      setToast(`导入完成：成功 ${result.imported ?? 0} 个，失败 ${result.failed ?? 0} 个${warningText}`);
      refreshAll();
    },
    onError: (error: Error) => setToast(error.message)
  });
  const updateAccount = useMutation({
    mutationFn: ({ id, payload }: { id: number; payload: Partial<AccountFormState> }) =>
      sendJSON(`/auth/accounts/${id}`, authState, "PATCH", payload),
    ...mutationOptions
  });
  const updateBlockedModels = useMutation({
    mutationFn: ({ id, blocked_models }: { id: number; blocked_models: string[] }) =>
      sendJSON(`/auth/accounts/${id}/models`, authState, "PUT", { blocked_models }),
    ...mutationOptions
  });
  const refreshAccount = useMutation({
    mutationFn: (id: number) => sendJSON(`/dashboard/api/accounts/${id}/refresh-credits`, authState, "POST", { check_models: true }),
    ...mutationOptions
  });
  const refreshAccounts = useMutation({
    mutationFn: (ids: number[]) => sendJSON("/dashboard/api/accounts/refresh-credits", authState, "POST", { account_ids: ids, check_models: true }),
    ...mutationOptions
  });
  const bulkUpdateAccounts = useMutation({
    mutationFn: (payload: { ids: number[]; patch: Partial<AccountFormState> }) =>
      Promise.all(payload.ids.map((id) => sendJSON(`/auth/accounts/${id}`, authState, "PATCH", payload.patch))),
    ...mutationOptions
  });
  const bulkDeleteAccounts = useMutation({
    mutationFn: (ids: number[]) => Promise.all(ids.map((id) => sendJSON(`/auth/accounts/${id}`, authState, "DELETE"))),
    onSuccess: () => {
      setAccountSelection({});
      setSelectedID(null);
      mutationOptions.onSuccess();
    },
    onError: mutationOptions.onError
  });
  const deleteAccount = useMutation({
    mutationFn: (id: number) => sendJSON(`/auth/accounts/${id}`, authState, "DELETE"),
    onSuccess: () => {
      setSelectedID(null);
      mutationOptions.onSuccess();
    },
    onError: mutationOptions.onError
  });
  const updateModelAccess = useMutation({
    mutationFn: ({ id, payload }: { id: string; payload: Partial<DashboardModel> }) =>
      sendJSON(`/auth/models/${encodeURIComponent(id)}/access`, authState, "PATCH", {
        visible: payload.visible,
        enabled: payload.supported,
        deprecated: payload.deprecated,
        unsupported_reason: payload.unsupported_reason,
        notes: payload.notes
      }),
    ...mutationOptions
  });
  const resetModelAccess = useMutation({
    mutationFn: (id: string) => sendJSON(`/auth/models/${encodeURIComponent(id)}/access`, authState, "DELETE"),
    ...mutationOptions
  });
  const updateRuntimeConfig = useMutation({
    mutationFn: (payload: RuntimeConfigSnapshot) => sendJSON("/dashboard/api/config", authState, "PATCH", payload),
    ...mutationOptions
  });
  const addProxy = useMutation({
    mutationFn: (url: string) => sendJSON("/dashboard/api/proxy", authState, "POST", { url }),
    onSuccess: () => {
      setProxyDraft("");
      mutationOptions.onSuccess();
    },
    onError: mutationOptions.onError
  });
  const patchProxy = useMutation({
    mutationFn: (payload: { id: string; enabled?: boolean; cooldown_seconds?: number; test?: boolean }) =>
      sendJSON("/dashboard/api/proxy", authState, "PATCH", payload),
    ...mutationOptions
  });
  const generateProxy = useMutation({
    mutationFn: () => sendJSON("/dashboard/api/proxy/generate", authState, "POST"),
    ...mutationOptions
  });
  const deleteProxy = useMutation({
    mutationFn: (id: string) => sendJSON(`/dashboard/api/proxy?id=${encodeURIComponent(id)}`, authState, "DELETE"),
    ...mutationOptions
  });
  const bindProxyAccounts = useMutation({
    mutationFn: (payload: {
      account_ids: number[];
      proxy_id?: string;
      proxy_url?: string;
      action?: string;
      clear?: boolean;
      generate?: boolean;
      rotate?: boolean;
      dynamic?: boolean;
      verify?: boolean;
      suspend?: boolean;
      resume?: boolean;
    }) =>
      sendJSON("/dashboard/api/proxy/bind-accounts", authState, "POST", payload),
    ...mutationOptions
  });
  const proxyBindingAction = useMutation({
    mutationFn: ({ accountID, action }: { accountID: number; action: string }) =>
      sendJSON(`/dashboard/api/proxy/accounts/${accountID}/${action}`, authState, action === "clear" ? "DELETE" : "POST"),
    ...mutationOptions
  });
  const runProxyMaintenance = useMutation({
    mutationFn: () => sendJSON("/dashboard/api/proxy/bindings", authState, "POST"),
    ...mutationOptions
  });
  const clearAvailabilityCooldown = useMutation({
    mutationFn: ({ accountID, model }: { accountID: number; model: string }) =>
      sendJSON(`/dashboard/api/availability/accounts/${accountID}/models/${encodeURIComponent(model)}/cooldown`, authState, "DELETE"),
    ...mutationOptions
  });
  const clearAvailabilityBreaker = useMutation({
    mutationFn: ({ accountID, model }: { accountID: number; model: string }) =>
      sendJSON(`/dashboard/api/availability/accounts/${accountID}/models/${encodeURIComponent(model)}/breaker`, authState, "DELETE"),
    ...mutationOptions
  });
  const probeAvailabilityModel = useMutation({
    mutationFn: ({ model, accountIDs, limit }: { model: string; accountIDs?: number[]; limit?: number }) =>
      sendJSON<ProbeResponse>(`/dashboard/api/availability/models/${encodeURIComponent(model)}/probe`, authState, "POST", {
        account_ids: accountIDs,
        limit
      }),
    ...mutationOptions
  });
  const probeAvailabilityAccountModel = useMutation({
    mutationFn: ({ accountID, model }: { accountID: number; model: string }) =>
      sendJSON<ProbeResponse>(`/dashboard/api/availability/accounts/${accountID}/models/${encodeURIComponent(model)}/probe`, authState, "POST"),
    ...mutationOptions
  });
  const clearModelBreakers = useMutation({
    mutationFn: (model: string) => sendJSON(`/dashboard/api/availability/models/${encodeURIComponent(model)}/breaker`, authState, "DELETE"),
    ...mutationOptions
  });
  const pruneAvailability = useMutation({
    mutationFn: () => sendJSON("/dashboard/api/availability/prune", authState, "POST"),
    ...mutationOptions
  });
  const clearCache = useMutation({
    mutationFn: () => sendJSON("/dashboard/api/cache", authState, "DELETE"),
    onSuccess: () => {
      setClearCacheOpen(false);
      mutationOptions.onSuccess();
    },
    onError: mutationOptions.onError
  });

  const accountRows = accounts.data?.accounts ?? [];
  const modelRows = models.data?.data ?? [];
  const selected = selectedID == null ? accountRows[0] : accountRows.find((a) => a.id === selectedID) ?? null;
  const filteredAccounts = useMemo(() => filterAccounts(accountRows, query), [accountRows, query]);
  const selectedAccountIDs = useMemo(() => Object.keys(accountSelection).map(Number).filter((id) => Number.isFinite(id)), [accountSelection]);
  const selectedProxyAccountIDs = useMemo(() => Object.keys(proxyAccountSelection).map(Number).filter((id) => Number.isFinite(id)), [proxyAccountSelection]);
  const accountsBusy =
    importAccount.isPending ||
    importAccountText.isPending ||
    updateAccount.isPending ||
    updateBlockedModels.isPending ||
    deleteAccount.isPending ||
    refreshAccount.isPending ||
    refreshAccounts.isPending ||
    bulkUpdateAccounts.isPending ||
    bulkDeleteAccounts.isPending ||
    bindProxyAccounts.isPending ||
    generateProxy.isPending;
  const health = accounts.data?.health ?? overview.data?.health;
  const total = overview.data?.counts?.total ?? accountRows.length;
  const enabled = overview.data?.counts?.enabled ?? accountRows.filter((a) => a.enabled).length;
  const banned = overview.data?.counts?.banned ?? accountRows.filter((a) => a.banned).length;
  const inflight = overview.data?.inflight ?? accountRows.reduce((sum, a) => sum + a.inflight, 0);
  const rpmUsed = overview.data?.rpm_used ?? accountRows.reduce((sum, a) => sum + a.rpm_used, 0);
  const rpmLimit = overview.data?.rpm_limit ?? accountRows.reduce((sum, a) => sum + a.rpm_limit, 0);
  const avgQuota = overview.data?.avg_quota ?? (total ? accountRows.reduce((sum, a) => sum + a.quota_score, 0) / total : 0);

  if (!authEnabled) {
    return (
      <LoginScreen
        mode={loginMode}
        secret={loginSecret}
        error={loginError}
        busy={loginMutation.isPending}
        theme={theme}
        onModeChange={(mode) => {
          setLoginMode(mode);
          setLoginError("");
          setLoginSecret("");
        }}
        onSecretChange={setLoginSecret}
        onSubmit={() => loginMutation.mutate({ mode: loginMode, secret: loginSecret })}
        onToggleTheme={() => setTheme(theme === "dark" ? "light" : "dark")}
      />
    );
  }

  return (
    <div className="shell">
      <aside className="sidebar">
        <div className="brand">
          <div className="brandMark">W</div>
          <div>
            <div className="brandName">WindsurfAPI-Go</div>
            <div className="brandSub">直连模式控制台</div>
          </div>
        </div>
        <nav className="nav">
          <NavItem icon={<Gauge />} label="总览" active={activeView === "overview"} onClick={() => navigateView("overview")} />
          <NavItem icon={<UsersRound />} label="账号" active={activeView === "accounts"} onClick={() => navigateView("accounts")} />
          <NavItem icon={<Router />} label="调度" active={activeView === "scheduler"} onClick={() => navigateView("scheduler")} />
          <NavItem icon={<ShieldCheck />} label="可用性" active={activeView === "availability"} onClick={() => navigateView("availability")} />
          <NavItem icon={<Layers3 />} label="模型" active={activeView === "models"} onClick={() => navigateView("models")} />
          <NavItem icon={<Network />} label="代理" active={activeView === "proxy"} onClick={() => navigateView("proxy")} />
          <NavItem icon={<ListChecks />} label="请求" active={activeView === "requests"} onClick={() => navigateView("requests")} />
          <NavItem icon={<Settings />} label="设置" active={activeView === "settings"} onClick={() => navigateView("settings")} />
          <NavItem icon={<Server />} label="旧版 LS" active={activeView === "legacy"} onClick={() => navigateView("legacy")} muted />
        </nav>
      </aside>

      <main className="main">
        <header className="topbar">
          <div>
            <h1>运行控制台</h1>
            <p>账号池、调度、Direct 上游、模型访问和 legacy LS 状态。</p>
          </div>
          <div className="toolbar">
            <label className="search">
              <Search size={16} />
              <Input value={query} placeholder="搜索账号、模型或备注" onChange={(event) => setQuery(event.target.value)} />
            </label>
            <Button className="iconButton" variant="ghost" size="icon" aria-label="刷新" onClick={refreshAll}>
              <RefreshCcw size={17} />
            </Button>
            <Button
              className="iconButton"
              variant="ghost"
              size="icon"
              aria-label={theme === "dark" ? "切换亮色主题" : "切换深色主题"}
              title={theme === "dark" ? "切换亮色主题" : "切换深色主题"}
              onClick={() => setTheme(theme === "dark" ? "light" : "dark")}
            >
              {theme === "dark" ? <Sun size={17} /> : <Moon size={17} />}
            </Button>
            <Button className="secondaryButton" variant="secondary" onClick={logout}>
              <LogOut size={15} />
              退出
            </Button>
          </div>
        </header>

        {toast ? (
          <div className="toast" role="status">
            {toast}
            <Button variant="ghost" size="sm" onClick={() => setToast("")}>
              关闭
            </Button>
          </div>
        ) : null}

        <section className="metrics">
          <Metric title="账号总数" value={total} detail={`${enabled} enabled / ${banned} banned`} icon={<UsersRound />} />
          <Metric title="进行中" value={inflight} detail="当前请求占用" icon={<Activity />} tone={inflight > 0 ? "info" : "ok"} />
          <Metric title="RPM" value={`${rpmUsed}/${rpmLimit}`} detail="滑动窗口用量" icon={<BarChart3 />} />
          <Metric title="额度健康" value={`${avgQuota.toFixed(1)}%`} detail="账号池平均健康分" icon={<Gauge />} tone={avgQuota < 20 ? "warn" : "ok"} />
          <Metric title="P95" value={`${stats.data?.p95_ms ?? 0} ms`} detail={`${stats.data?.success ?? 0} 成功 / ${stats.data?.failed ?? 0} 失败`} icon={<Clock3 />} tone={(stats.data?.failed ?? 0) > 0 ? "warn" : "neutral"} />
        </section>

        {activeView === "overview" ? (
          <>
            <section className="grid two">
              <Panel title="直连上游" icon={<Database />}>
                <DirectStatus direct={direct.data} />
              </Panel>
              <Panel title="请求统计" icon={<BarChart3 />}>
                <StatsPanel stats={stats.data} />
              </Panel>
            </section>
            <section className="grid two">
              <Panel title="健康刷新" icon={<ShieldCheck />}>
                <HealthStatus health={health} />
              </Panel>
              <Panel title="部署状态" icon={<Server />}>
                <DeploymentStatus config={runtimeConfig.data} legacy={legacy.data} />
              </Panel>
            </section>
          </>
        ) : null}

        {activeView === "accounts" ? (
          <section className="grid two">
            <Panel title="账号池" icon={<UsersRound />}>
              <AccountTable
                rows={filteredAccounts}
                selectedID={selected?.id ?? null}
                rowSelection={accountSelection}
                loading={accounts.isLoading}
                error={accounts.error}
                onSelect={setSelectedID}
                onSelectionChange={setAccountSelection}
                onToggle={(account) =>
                  updateAccount.mutate({ id: account.id, payload: { enabled: !account.enabled, banned: false } })
                }
              />
            </Panel>
            <Panel title="账号管理" icon={<KeyRound />}>
              <AccountManager
                selected={selected}
                selectedIDs={selectedAccountIDs}
                proxies={proxy.data?.entries ?? []}
                models={modelRows}
                busy={accountsBusy}
                onImport={(payload) => importAccount.mutate(payload)}
                onImportText={(text) => importAccountText.mutate(text)}
                onUpdate={(id, payload) => updateAccount.mutate({ id, payload })}
                onBlockedModels={(id, blocked_models) => updateBlockedModels.mutate({ id, blocked_models })}
                onRefresh={(id) => refreshAccount.mutate(id)}
                onBulkRefresh={(ids) => refreshAccounts.mutate(ids)}
                onBulkPatch={(ids, patch) => bulkUpdateAccounts.mutate({ ids, patch })}
                onBulkDelete={(ids) => bulkDeleteAccounts.mutate(ids)}
                onBulkProxy={(ids, proxyID) => bindProxyAccounts.mutate({ account_ids: ids, proxy_id: proxyID })}
                onBulkClearProxy={(ids) => bindProxyAccounts.mutate({ account_ids: ids, clear: true })}
                onDelete={(id) => deleteAccount.mutate(id)}
              />
            </Panel>
          </section>
        ) : null}

        {activeView === "models" ? (
          <section className="grid one">
            <Panel title="模型目录" icon={<Layers3 />}>
              <ModelTable
                rows={modelRows}
                scope={models.data?.scope}
                showAll={showAllModels}
                onShowAllChange={setShowAllModels}
                selected={selected}
                loading={models.isLoading}
                busy={updateModelAccess.isPending || resetModelAccess.isPending}
                onPatch={(id, payload) => updateModelAccess.mutate({ id, payload })}
                onReset={(id) => resetModelAccess.mutate(id)}
              />
            </Panel>
          </section>
        ) : null}

        {activeView === "proxy" ? (
          <section className="grid two">
            <Panel title="动态代理" icon={<Network />}>
              <ProxyPanel
                snapshot={proxy.data}
                value={proxyDraft}
                accounts={accountRows}
                busy={addProxy.isPending || patchProxy.isPending || deleteProxy.isPending || bindProxyAccounts.isPending || proxyBindingAction.isPending || runProxyMaintenance.isPending}
                onChange={setProxyDraft}
                onAdd={() => addProxy.mutate(proxyDraft)}
                onGenerate={() => generateProxy.mutate()}
                onPatch={(id, payload) => patchProxy.mutate({ id, ...payload })}
                onDelete={(id) => deleteProxy.mutate(id)}
                selectedAccountIDs={selectedProxyAccountIDs}
                accountSelection={proxyAccountSelection}
                onAccountSelectionChange={setProxyAccountSelection}
                onBindAccounts={(proxyID, accountIDs) => bindProxyAccounts.mutate({ account_ids: accountIDs, proxy_id: proxyID })}
                onGenerateForAccounts={(accountIDs) => bindProxyAccounts.mutate({ account_ids: accountIDs, generate: true })}
                onDynamicBind={(accountIDs) => bindProxyAccounts.mutate({ account_ids: accountIDs, dynamic: true })}
                onDynamicRotate={(accountIDs) => bindProxyAccounts.mutate({ account_ids: accountIDs, dynamic: true, rotate: true })}
                onDynamicVerify={(accountIDs) => bindProxyAccounts.mutate({ account_ids: accountIDs, dynamic: true, verify: true })}
                onDynamicSuspend={(accountIDs) => bindProxyAccounts.mutate({ account_ids: accountIDs, dynamic: true, suspend: true })}
                onDynamicResume={(accountIDs) => bindProxyAccounts.mutate({ account_ids: accountIDs, dynamic: true, resume: true })}
                onDynamicClear={(accountIDs) => bindProxyAccounts.mutate({ account_ids: accountIDs, dynamic: true, clear: true })}
                onBindingAction={(accountID, action) => proxyBindingAction.mutate({ accountID, action })}
                onRunMaintenance={() => runProxyMaintenance.mutate()}
              />
            </Panel>
            <Panel title="代理状态" icon={<Network />}>
              <ProxyStatus proxy={proxy.data} direct={direct.data} />
            </Panel>
          </section>
        ) : null}

        {activeView === "scheduler" ? (
          <section className="grid two">
            <Panel title="调度事件" icon={<Clock3 />}>
              <EventList rows={(scheduler.data?.events ?? accounts.data?.events ?? []).slice(-20)} />
            </Panel>
            <Panel title="调度状态" icon={<Router />}>
              <SchedulerStatus
                scheduler={scheduler.data}
                overview={overview.data}
                runtime={runtimeConfig.data}
                stats={stats.data}
                cache={cache.data}
                busy={clearCache.isPending}
                onClearCache={() => setClearCacheOpen(true)}
              />
            </Panel>
          </section>
        ) : null}

        {activeView === "availability" ? (
          <section className="grid two">
            <Panel title="健康刷新" icon={<ShieldCheck />}>
              <HealthStatus health={health} />
            </Panel>
            <Panel title="账号可用性" icon={<UsersRound />}>
              <AvailabilityPanel
                accounts={accountRows}
                models={modelRows}
                selectedAccountIDs={selectedAccountIDs}
                busy={
                  clearAvailabilityCooldown.isPending ||
                  clearAvailabilityBreaker.isPending ||
                  clearModelBreakers.isPending ||
                  pruneAvailability.isPending ||
                  refreshAccounts.isPending ||
                  probeAvailabilityModel.isPending ||
                  probeAvailabilityAccountModel.isPending
                }
                onRefresh={() => refreshAccounts.mutate(accountRows.map((account) => account.id))}
                onPrune={() => pruneAvailability.mutate()}
                onProbeModel={(model, accountIDs) => probeAvailabilityModel.mutate({ model, accountIDs, limit: accountIDs.length ? undefined : 1 })}
                onProbeAccountModel={(accountID, model) => probeAvailabilityAccountModel.mutate({ accountID, model })}
                onClearCooldown={(accountID, model) => clearAvailabilityCooldown.mutate({ accountID, model })}
                onClearBreaker={(accountID, model) => clearAvailabilityBreaker.mutate({ accountID, model })}
                onClearModelBreakers={(model) => clearModelBreakers.mutate(model)}
              />
            </Panel>
          </section>
        ) : null}

        {activeView === "requests" ? (
          <>
            <section className="grid one">
              <Panel title="最近请求" icon={<ListChecks />}>
                <LogToolbar
                  filters={logFilters}
                  dashboardPassword={dashboardPassword}
                  streaming={streamingLogs}
                  onChange={setLogFilters}
                  onClear={() => setLogFilters(emptyLogFilters())}
                  onToggleStream={() => setStreamingLogs(!streamingLogs)}
                />
                <RequestList rows={logs.data?.requests ?? stats.data?.recent ?? []} />
              </Panel>
            </section>
            <section className="grid one">
              <Panel title="请求统计" icon={<BarChart3 />}>
                <StatsPanel stats={stats.data} />
              </Panel>
            </section>
          </>
        ) : null}

        {activeView === "settings" ? (
          <section className="grid two">
            <Panel title="运行配置" icon={<Settings />}>
              <SettingsPanel
                config={runtimeConfig.data}
                busy={updateRuntimeConfig.isPending}
                onSave={(payload) => updateRuntimeConfig.mutate(payload)}
              />
            </Panel>
            <Panel title="敏感配置" icon={<ShieldCheck />}>
              <SecretStatusList security={runtimeConfig.data?.security} />
            </Panel>
          </section>
        ) : null}

        {activeView === "legacy" ? (
          <section className="grid two">
            <Panel title="旧版 LS" icon={<Server />}>
              <StatusList
                rows={[
                  ["模式", "仅调试"],
                  ["实例数", String(legacy.data?.entries?.length ?? 0)],
                  ["最大实例数", String(legacy.data?.max_instances ?? 0)],
                  ["主链路", "直连模式"]
                ]}
              />
            </Panel>
            <Panel title="替代路线" icon={<ListChecks />}>
              <Roadmap />
            </Panel>
          </section>
        ) : null}
        <ConfirmDialog
          open={clearCacheOpen}
          title="清理复用缓存"
          description="确认清理当前进程内的 conversation reuse/cache 条目？正在处理的请求不会被中断，后续请求会重新调度账号。"
          confirmLabel="清理缓存"
          cancelLabel="取消"
          busy={clearCache.isPending}
          onCancel={() => setClearCacheOpen(false)}
          onConfirm={() => clearCache.mutate()}
        />
      </main>
    </div>
  );
}

function NavItem({
  icon,
  label,
  active,
  muted,
  onClick
}: {
  icon: React.ReactNode;
  label: string;
  active?: boolean;
  muted?: boolean;
  onClick: () => void;
}) {
  return (
    <button className={`navItem ${active ? "active" : ""} ${muted ? "muted" : ""}`} aria-current={active ? "page" : undefined} onClick={onClick}>
      {icon}
      <span>{label}</span>
    </button>
  );
}

function LoginScreen({
  mode,
  secret,
  error,
  busy,
  theme,
  onModeChange,
  onSecretChange,
  onSubmit,
  onToggleTheme
}: {
  mode: LoginMode;
  secret: string;
  error: string;
  busy: boolean;
  theme: "dark" | "light";
  onModeChange: (mode: LoginMode) => void;
  onSecretChange: (value: string) => void;
  onSubmit: () => void;
  onToggleTheme: () => void;
}) {
  return (
    <main className="loginPage">
      <section className="loginPanel" aria-labelledby="login-title">
        <div className="loginBrand">
          <div className="brandMark">W</div>
          <div>
            <strong>WindsurfAPI-Go</strong>
            <span>Direct-only 运维控制台</span>
          </div>
        </div>
        <div className="loginIntro">
          <h1 id="login-title">登录控制台</h1>
          <p>请输入控制台密码，验证成功后进入 Dashboard。接口密钥登录保留给本地调试。</p>
        </div>
        <div className="loginTabs" role="tablist" aria-label="登录方式">
          <button className={mode === "password" ? "active" : ""} type="button" onClick={() => onModeChange("password")}>
            控制台密码
          </button>
          <button className={mode === "apiKey" ? "active" : ""} type="button" onClick={() => onModeChange("apiKey")}>
            接口密钥
          </button>
        </div>
        <label className="loginField">
          <span>{mode === "password" ? "控制台密码" : "接口密钥"}</span>
          <Input
            type="password"
            value={secret}
            placeholder={mode === "password" ? "输入 Dashboard 密码" : "输入 API key"}
            autoFocus
            onChange={(event) => onSecretChange(event.target.value)}
            onKeyDown={(event) => {
              if (event.key === "Enter") onSubmit();
            }}
          />
        </label>
        {error ? <div className="loginError">{error}</div> : null}
        <Button className="loginButton" disabled={busy || !secret.trim()} onClick={onSubmit}>
          <LogIn size={16} />
          {busy ? "验证中..." : "进入控制台"}
        </Button>
        <div className="loginFooter">
          <button type="button" onClick={onToggleTheme}>
            {theme === "dark" ? <Sun size={14} /> : <Moon size={14} />}
            {theme === "dark" ? "亮色模式" : "深色模式"}
          </button>
          <span>本页不会主动加载账号数据，登录成功后才请求 Dashboard API。</span>
        </div>
      </section>
    </main>
  );
}

function Metric({
  title,
  value,
  detail,
  icon,
  tone = "neutral"
}: {
  title: string;
  value: string | number;
  detail: string;
  icon: React.ReactNode;
  tone?: "neutral" | "ok" | "warn" | "info";
}) {
  return (
    <article className={`metric ${tone}`}>
      <div className="metricHead">
        <span>{title}</span>
        {icon}
      </div>
      <strong>{value}</strong>
      <small>{detail}</small>
    </article>
  );
}

function Panel({ title, icon, children }: { title: string; icon: React.ReactNode; children: React.ReactNode }) {
  return (
    <section className="panel">
      <div className="panelHead">
        <div>
          {icon}
          <h2>{title}</h2>
        </div>
      </div>
      {children}
    </section>
  );
}

function DirectStatus({ direct }: { direct: DirectSnapshot | undefined }) {
  return (
    <StatusList
      rows={[
        ["协议", direct?.protocol || "grpc"],
        ["上游地址", (direct?.hosts ?? []).join(", ") || "未配置"],
        ["代理客户端", String(direct?.proxy_clients ?? 0)],
        ["最近上游", direct?.last_host || "无"],
        ["最近代理", direct?.last_proxy || "无"],
        ["失败次数", String(direct?.failures ?? 0)],
        ["成功次数", String(direct?.successes ?? 0)],
        ["最近延迟", `${direct?.last_latency_ms ?? 0} ms`],
        ["最近错误", direct?.last_error || "无"]
      ]}
    />
  );
}

function ProxyStatus({ proxy, direct }: { proxy: ProxySnapshot | undefined; direct: DirectSnapshot | undefined }) {
  return (
    <StatusList
      rows={[
        ["默认代理", proxy?.default || "无"],
        ["持久化", proxy?.persistent ? "开启" : "关闭"],
        ["动态代理数", String(proxy?.entries?.length ?? 0)],
        ["账号绑定", proxy?.account_binding ? "开启" : "关闭"],
        ["已绑定账号", String(proxy?.summary?.bound ?? 0)],
        ["失败绑定", String(proxy?.summary?.failed ?? 0)],
        ["自动绑定", proxy?.auto_bind_new_accounts ? "开启" : "关闭"],
        ["错误时轮换", proxy?.rotate_on_error ? "开启" : "关闭"],
        ["测试地址", proxy?.test_url || "-"],
        ["直连代理客户端", String(direct?.proxy_clients ?? 0)]
      ]}
    />
  );
}

function HealthStatus({ health }: { health: HealthSummary | undefined }) {
  return (
    <StatusList
      rows={[
        ["最近运行", health?.last_run_at || "未运行"],
        ["耗时", `${health?.last_duration_ms ?? 0} ms`],
        ["检查数", String(health?.checked ?? 0)],
        ["正常", String(health?.ok ?? 0)],
        ["无效", String(health?.invalid ?? 0)],
        ["失败", String(health?.failed ?? 0)],
        ["最近错误", health?.last_error || "无"]
      ]}
    />
  );
}

function SchedulerStatus({
  scheduler,
  overview,
  runtime,
  stats,
  cache,
  busy,
  onClearCache
}: {
  scheduler: SchedulerSnapshot | undefined;
  overview: OverviewSnapshot | undefined;
  runtime: RuntimeConfigSnapshot | undefined;
  stats: RequestStats | undefined;
  cache: CacheSnapshot | undefined;
  busy: boolean;
  onClearCache: () => void;
}) {
  return (
    <div className="manager">
      <StatusList
        rows={[
          ["协调器", formatBoolean(Boolean(scheduler?.coordinator?.enabled ?? overview?.coordinator?.enabled ?? false))],
          ["Redis 失败关闭", formatBoolean(Boolean(runtime?.scheduler?.redis_fail_closed ?? false))],
          ["Redis 调度", formatBoolean(Boolean(runtime?.scheduler?.redis_enabled ?? false))],
          ["会话复用", String(scheduler?.reuse?.enabled ?? "未知")],
          ["缓存条目", String(cache?.entries ?? 0)],
          ["缓存写入", String(cache?.stats?.stores ?? 0)],
          ["最近错误", formatMap(stats?.by_class)]
        ]}
      />
      <div className="buttonRow">
        <Button className="dangerButton" variant="destructive" disabled={busy || !cache?.entries} onClick={onClearCache}>
          <Trash2 size={15} />
          清理复用缓存
        </Button>
      </div>
    </div>
  );
}

function DeploymentStatus({ config, legacy }: { config: RuntimeConfigSnapshot | undefined; legacy: LSSnapshot | undefined }) {
  return (
    <StatusList
      rows={[
        ["接口密钥数", String(config?.server?.api_key_count ?? 0)],
        ["SQLite", config?.sqlite?.path || "-"],
        ["Redis", `${config?.redis?.addr || "-"} db=${config?.redis?.db ?? 0}`],
        ["Redis 密码", config?.redis?.password_set ? "已设置" : "未设置"],
        ["控制台密码", config?.dashboard?.password_set ? "已设置" : "未设置"],
        ["旧版 LS", `仅调试 · ${legacy?.entries?.length ?? 0} 个实例`]
      ]}
    />
  );
}

function LogToolbar({
  filters,
  dashboardPassword,
  streaming,
  onChange,
  onClear,
  onToggleStream
}: {
  filters: LogFilters;
  dashboardPassword: string;
  streaming: boolean;
  onChange: (filters: LogFilters) => void;
  onClear: () => void;
  onToggleStream: () => void;
}) {
  return (
    <div className="panelActions">
      <Input
        className="compactInput wide"
        value={filters.q}
        placeholder="过滤请求、模型、错误"
        onChange={(event) => onChange({ ...filters, q: event.target.value })}
      />
      <Select className="compactInput" value={filters.route} onChange={(event) => onChange({ ...filters, route: event.target.value })}>
        <option value="">路由：全部</option>
        <option value="chat">Chat</option>
        <option value="messages">Messages</option>
        <option value="responses">Responses</option>
      </Select>
      <Select className="compactInput" value={filters.status} onChange={(event) => onChange({ ...filters, status: event.target.value })}>
        <option value="">状态：全部</option>
        <option value="ok">成功</option>
        <option value="error">失败</option>
      </Select>
      <Select className="compactInput" value={filters.errorClass} onChange={(event) => onChange({ ...filters, errorClass: event.target.value })}>
        <option value="">错误：全部</option>
        <option value="rate_limit">限流</option>
        <option value="model_not_available">模型不可用</option>
        <option value="policy_blocked">策略拦截</option>
        <option value="ban_signal">封禁信号</option>
        <option value="upstream_transient">上游临时错误</option>
        <option value="transport">传输错误</option>
        <option value="fatal">致命错误</option>
      </Select>
      <Input
        className="compactInput"
        value={filters.model}
        placeholder="模型"
        onChange={(event) => onChange({ ...filters, model: event.target.value })}
      />
      <Input
        className="compactInput"
        value={filters.accountID}
        placeholder="账号 ID"
        inputMode="numeric"
        onChange={(event) => onChange({ ...filters, accountID: event.target.value })}
      />
      <Select className="compactInput" value={filters.stream} onChange={(event) => onChange({ ...filters, stream: event.target.value })}>
        <option value="">流式：全部</option>
        <option value="true">流式</option>
        <option value="false">非流式</option>
      </Select>
      <Select className="compactInput" value={filters.retry} onChange={(event) => onChange({ ...filters, retry: event.target.value })}>
        <option value="">重试：全部</option>
        <option value="true">有重试</option>
        <option value="false">无重试</option>
      </Select>
      <Button className="miniButton" variant="secondary" size="sm" onClick={onClear}>
        清空
      </Button>
      <a className="miniButton linkButton" href={dashboardExportURL("csv", dashboardPassword, filters)} target="_blank" rel="noreferrer">
        <Download size={13} />
        CSV
      </a>
      <a className="miniButton linkButton" href={dashboardExportURL("ndjson", dashboardPassword, filters)} target="_blank" rel="noreferrer">
        NDJSON
      </a>
      <Button className={`miniButton ${streaming ? "active" : ""}`} variant="secondary" size="sm" disabled={!dashboardPassword} onClick={onToggleStream}>
        {streaming ? "停止流" : "日志流"}
      </Button>
    </div>
  );
}

function AccountTable({
  rows,
  selectedID,
  rowSelection,
  loading,
  error,
  onSelect,
  onSelectionChange,
  onToggle
}: {
  rows: DebugAccount[];
  selectedID: number | null;
  rowSelection: RowSelectionState;
  loading: boolean;
  error: unknown;
  onSelect: (id: number) => void;
  onSelectionChange: React.Dispatch<React.SetStateAction<RowSelectionState>>;
  onToggle: (account: DebugAccount) => void;
}) {
  const [sorting, setSorting] = useState<SortingState>([]);
  const columns = useMemo<ColumnDef<DebugAccount>[]>(
    () => [
      {
        id: "select",
        header: ({ table }) => (
          <input
            type="checkbox"
            aria-label="选择当前页账号"
            checked={table.getIsAllPageRowsSelected()}
            ref={(node) => {
              if (node) node.indeterminate = table.getIsSomePageRowsSelected();
            }}
            onChange={table.getToggleAllPageRowsSelectedHandler()}
          />
        ),
        cell: ({ row }) => (
          <input
            type="checkbox"
            aria-label={`选择账号 ${row.original.id}`}
            checked={row.getIsSelected()}
            ref={(node) => {
              if (node) node.indeterminate = row.getIsSomeSelected();
            }}
            onClick={(event) => event.stopPropagation()}
            onChange={row.getToggleSelectedHandler()}
          />
        ),
        enableSorting: false
      },
      {
        accessorKey: "id",
        header: "ID"
      },
      {
        accessorKey: "email",
        header: "邮箱",
        cell: ({ row }) => <span className="wideCell">{row.original.email || "未知"}</span>
      },
      {
        accessorKey: "plan_name",
        header: "套餐",
        cell: ({ row }) => <AccountPlanBadge account={row.original} />
      },
      {
        accessorKey: "tier",
        header: "等级",
        cell: ({ row }) => formatTier(row.original.tier)
      },
      {
        accessorKey: "model_config_count",
        header: "模型能力",
        cell: ({ row }) => <ModelCapabilityBadge account={row.original} />
      },
      {
        id: "status",
        header: "状态",
        cell: ({ row }) => (
          <span className={`pill ${row.original.banned ? "bad" : row.original.enabled ? "good" : "warn"}`}>
            {row.original.banned ? "已封禁" : row.original.enabled ? "启用" : "停用"}
          </span>
        )
      },
      {
        accessorKey: "inflight",
        header: "进行中"
      },
      {
        id: "rpm",
        header: "RPM",
        cell: ({ row }) => `${row.original.rpm_used}/${row.original.rpm_limit}`
      },
      {
        accessorKey: "quota_score",
        header: "额度",
        cell: ({ row }) => formatPercent(row.original.quota_score)
      },
      {
        id: "blocked",
        header: "屏蔽模型",
        cell: ({ row }) => row.original.blocked_models?.length ?? 0
      },
      {
        id: "proxy",
        header: "代理",
        cell: ({ row }) => (row.original.proxy_url_set || row.original.proxy_url ? "已设置" : "无")
      },
      {
        id: "action",
        header: "操作",
        cell: ({ row }) => (
          <Button
            className="miniButton"
            variant="secondary"
            size="sm"
            onClick={(event) => {
              event.stopPropagation();
              onToggle(row.original);
            }}
          >
            {row.original.enabled ? "停用" : "启用"}
          </Button>
        )
      }
    ],
    [onToggle]
  );
  const table = useReactTable({
    data: rows,
    columns,
    state: { sorting, rowSelection },
    onSortingChange: setSorting,
    onRowSelectionChange: onSelectionChange,
    getCoreRowModel: getCoreRowModel(),
    getSortedRowModel: getSortedRowModel(),
    getPaginationRowModel: getPaginationRowModel(),
    getRowId: (row) => String(row.id)
  });
  React.useEffect(() => {
    table.setPageSize(10);
  }, [table]);
  if (loading) return <div className="empty">加载账号状态...</div>;
  if (error) return <div className="empty error">读取失败：{String((error as Error).message || error)}</div>;
  if (!rows.length) return <div className="empty">暂无账号。可以在右侧导入 token。</div>;
  return (
    <div className="tableWrap">
      <table>
        <thead>
          {table.getHeaderGroups().map((headerGroup) => (
            <tr key={headerGroup.id}>
              {headerGroup.headers.map((header) => (
                <SortableTH key={header.id} header={header} />
              ))}
            </tr>
          ))}
        </thead>
        <tbody>
          {table.getRowModel().rows.map((row) => (
            <tr key={row.id} className={selectedID === row.original.id ? "selectedRow" : ""} onClick={() => onSelect(row.original.id)}>
              {row.getVisibleCells().map((cell) => (
                <td key={cell.id}>{flexRender(cell.column.columnDef.cell, cell.getContext())}</td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
      <TablePager table={table} total={rows.length} />
    </div>
  );
}

function AccountManager({
  selected,
  selectedIDs,
  proxies,
  models,
  busy,
  onImport,
  onImportText,
  onUpdate,
  onBlockedModels,
  onRefresh,
  onBulkRefresh,
  onBulkPatch,
  onBulkDelete,
  onBulkProxy,
  onBulkClearProxy,
  onDelete
}: {
  selected: DebugAccount | null;
  selectedIDs: number[];
  proxies: ProxyEntry[];
  models: DashboardModel[];
  busy: boolean;
  onImport: (payload: AccountFormState) => void;
  onImportText: (text: string) => void;
  onUpdate: (id: number, payload: Partial<AccountFormState>) => void;
  onBlockedModels: (id: number, blockedModels: string[]) => void;
  onRefresh: (id: number) => void;
  onBulkRefresh: (ids: number[]) => void;
  onBulkPatch: (ids: number[], patch: Partial<AccountFormState>) => void;
  onBulkDelete: (ids: number[]) => void;
  onBulkProxy: (ids: number[], proxyID: string) => void;
  onBulkClearProxy: (ids: number[]) => void;
  onDelete: (id: number) => void;
}) {
  const [form, setForm] = useState<AccountFormState>(emptyForm());
  const [bulkImportText, setBulkImportText] = useState("");
  const [blockedDraft, setBlockedDraft] = useState("");
  const [deleteTarget, setDeleteTarget] = useState<DebugAccount | null>(null);
  const [bulkDeleteOpen, setBulkDeleteOpen] = useState(false);
  const [bulkProxyID, setBulkProxyID] = useState("");

  React.useEffect(() => {
    if (!selected) return;
    setDeleteTarget(null);
    setForm({
      email: selected.email || "",
      token: "",
      tier: selected.tier || "unknown",
      proxy_url: selected.proxy_url || "",
      notes: selected.notes || "",
      enabled: selected.enabled,
      banned: selected.banned
    });
    setBlockedDraft((selected.blocked_models ?? []).join("\n"));
  }, [selected?.id]);

  const selectedID = selected?.id ?? null;
  const hasBulk = selectedIDs.length > 0;
  return (
    <div className="manager">
      <div className="bulkBar">
        <strong>已选 {selectedIDs.length} 个账号</strong>
        <div className="inlineActions">
          <Button className="miniButton" variant="secondary" size="sm" disabled={busy || !hasBulk} onClick={() => onBulkRefresh(selectedIDs)}>
            批量刷新余额
          </Button>
          <Button className="miniButton" variant="secondary" size="sm" disabled={busy || !hasBulk} onClick={() => onBulkPatch(selectedIDs, { enabled: true, banned: false })}>
            批量启用
          </Button>
          <Button className="miniButton" variant="secondary" size="sm" disabled={busy || !hasBulk} onClick={() => onBulkPatch(selectedIDs, { enabled: false })}>
            批量停用
          </Button>
          <Button className="miniButton" variant="secondary" size="sm" disabled={busy || !hasBulk} onClick={() => onBulkPatch(selectedIDs, { enabled: false, banned: true })}>
            标记封禁
          </Button>
          <Select className="compactInput wide" value={bulkProxyID} onChange={(event) => setBulkProxyID(event.target.value)}>
            <option value="">选择代理绑定</option>
            {proxies.map((proxy) => (
              <option key={proxy.id} value={proxy.id}>
                {proxy.masked_url || proxy.id}
              </option>
            ))}
          </Select>
          <Button className="miniButton" variant="secondary" size="sm" disabled={busy || !hasBulk || !bulkProxyID} onClick={() => onBulkProxy(selectedIDs, bulkProxyID)}>
            绑定代理
          </Button>
          <Button className="miniButton" variant="secondary" size="sm" disabled={busy || !hasBulk} onClick={() => onBulkClearProxy(selectedIDs)}>
            清除代理
          </Button>
          <Button className="miniButton" variant="secondary" size="sm" disabled={busy || !hasBulk} onClick={() => setBulkDeleteOpen(true)}>
            批量删除
          </Button>
        </div>
      </div>
      <ConfirmDialog
        open={bulkDeleteOpen}
        title="批量删除账号"
        description={`确认删除已选 ${selectedIDs.length} 个账号？该操作只删除本地记录，不会回收上游 token。`}
        confirmLabel="删除账号"
        cancelLabel="取消"
        busy={busy}
        onCancel={() => setBulkDeleteOpen(false)}
        onConfirm={() => {
          onBulkDelete(selectedIDs);
          setBulkDeleteOpen(false);
        }}
      />

      <div className="importBox">
        <div className="blockedHeader">
          <strong>批量文本导入</strong>
          <Button className="miniButton" variant="secondary" size="sm" disabled={busy || !bulkImportText.trim()} onClick={() => {
            onImportText(bulkImportText);
            setBulkImportText("");
          }}>
            <Upload size={14} />
            导入文本账号
          </Button>
        </div>
        <Textarea
          value={bulkImportText}
          placeholder={"user1@mail.com----Windsurf@2025----devin-session-token$eyJhbGc...----auth1_xxxxxxxxxxxx\nuser2@mail.com----Windsurf@2025----devin-session-token$eyJhbGc...----auth1_xxxxxxxxxxxx"}
          onChange={(event) => setBulkImportText(event.target.value)}
        />
      </div>

      <AccountBalanceDetails account={selected} />

      <div className="formGrid">
        <Field label="邮箱">
          <Input value={form.email} onChange={(event) => setForm({ ...form, email: event.target.value })} />
        </Field>
        <Field label="等级">
          <Select value={form.tier} onChange={(event) => setForm({ ...form, tier: event.target.value })}>
            <option value="unknown">未知</option>
            <option value="free">免费</option>
            <option value="pro">专业</option>
          </Select>
        </Field>
        <Field label="Token">
          <Input
            value={form.token}
            type="password"
            placeholder={selected ? "留空则不修改" : "devin-session-token 或 api key"}
            onChange={(event) => setForm({ ...form, token: event.target.value })}
          />
        </Field>
        <Field label="代理地址">
          <Input value={form.proxy_url} placeholder="http://user:pass@host:port" onChange={(event) => setForm({ ...form, proxy_url: event.target.value })} />
        </Field>
        <Field label="备注">
          <Input value={form.notes} onChange={(event) => setForm({ ...form, notes: event.target.value })} />
        </Field>
        <div className="toggleRow">
          <label className="switchLabel">
            <Switch checked={form.enabled} onCheckedChange={(checked) => setForm({ ...form, enabled: checked })} />
            启用
          </label>
          <label className="switchLabel">
            <Switch checked={form.banned} onCheckedChange={(checked) => setForm({ ...form, banned: checked })} />
            封禁
          </label>
        </div>
      </div>

      <div className="buttonRow">
        <Button className="secondaryButton" variant="secondary" disabled={busy || !form.token.trim()} onClick={() => onImport(form)}>
          <Upload size={15} />
          导入
        </Button>
        <Button className="secondaryButton" variant="secondary" disabled={busy || selectedID == null} onClick={() => selectedID != null && onUpdate(selectedID, accountPatchPayload(form))}>
          <Save size={15} />
          保存
        </Button>
        <Button className="secondaryButton" variant="secondary" disabled={busy || selectedID == null} onClick={() => selectedID != null && onRefresh(selectedID)}>
          <RefreshCcw size={15} />
          刷新余额
        </Button>
        <Button
          className="dangerButton"
          variant="destructive"
          disabled={busy || selectedID == null}
          onClick={() => {
            if (selectedID == null) return;
            setDeleteTarget(selected);
          }}
        >
          <Trash2 size={15} />
          删除
        </Button>
      </div>
      <ConfirmDialog
        open={!!deleteTarget}
        title="删除账号"
        description={`确认删除账号 #${deleteTarget?.id ?? ""} ${deleteTarget?.email ?? ""}？该操作只删除本地记录，不会回收上游 token。`}
        confirmLabel="删除账号"
        cancelLabel="取消"
        busy={busy}
        onCancel={() => setDeleteTarget(null)}
        onConfirm={() => {
          if (!deleteTarget) return;
          onDelete(deleteTarget.id);
          setDeleteTarget(null);
        }}
      />

      <div className="blockedBox">
        <div className="blockedHeader">
          <strong>屏蔽模型</strong>
          <Button className="miniButton" variant="secondary" size="sm" disabled={busy || selectedID == null} onClick={() => selectedID != null && onBlockedModels(selectedID, parseLines(blockedDraft))}>
            应用
          </Button>
        </div>
        <Textarea value={blockedDraft} placeholder="每行一个模型 ID" onChange={(event) => setBlockedDraft(event.target.value)} />
        <div className="modelChips">
          {models.slice(0, 10).map((model) => (
            <Button
              key={model.id}
              className={`chip ${parseLines(blockedDraft).includes(model.id) ? "active" : ""}`}
              variant="ghost"
              size="sm"
              onClick={() => setBlockedDraft(toggleLine(blockedDraft, model.id))}
            >
              {model.id}
            </Button>
          ))}
        </div>
      </div>
    </div>
  );
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <label className="field">
      <span>{label}</span>
      {children}
    </label>
  );
}

function AccountBalanceDetails({ account }: { account: DebugAccount | null }) {
  if (!account) return <div className="empty compactEmpty">请选择一个账号查看余额详情。</div>;
  return (
    <div className="detailGrid">
      <StatusList
        rows={[
          ["套餐", account.plan_name || formatTier(account.tier)],
          ["计划开始", formatDateTime(account.plan_start)],
          ["计划结束", formatDateTime(account.plan_end)],
          ["模型配置", formatModelConfigCount(account.model_config_count)],
          ["能力状态", modelCapabilityText(account)],
          ["日额度", formatPercent(account.quota_daily_percent)],
          ["周额度", formatPercent(account.quota_weekly_percent)],
          ["日重置", formatDateTime(account.quota_daily_reset_at)],
          ["周重置", formatDateTime(account.quota_weekly_reset_at)],
          ["透支余额", formatMoney(account.overage_balance)],
          ["最近检查", formatDateTime(account.health_checked_at)]
        ]}
      />
      <StatusList
        rows={[
          ["Prompt 额度", formatCredit(account.prompt)],
          ["Flex 额度", formatCredit(account.flex)],
          ["全局限流", activeCooldown(account.rate_limited_until)],
          ["模型冷却", formatRecordCount(account.model_cooldowns)],
          ["Breaker", formatRecordCount(account.model_breakers)],
          ["最近错误", formatMap(account.recent_errors)]
        ]}
      />
    </div>
  );
}

function SortableTH<T>({ header }: { header: Header<T, unknown> }) {
  if (header.isPlaceholder) return <th />;
  const sorted = header.column.getIsSorted();
  const canSort = header.column.getCanSort();
  return (
    <th className={canSort ? "sortableTH" : ""} onClick={canSort ? header.column.getToggleSortingHandler() : undefined}>
      <span>
        {flexRender(header.column.columnDef.header, header.getContext())}
        {sorted === "asc" ? " ↑" : sorted === "desc" ? " ↓" : ""}
      </span>
    </th>
  );
}

function TablePager<T>({ table, total }: { table: Table<T>; total: number }) {
  const pageCount = table.getPageCount();
  const pageIndex = table.getState().pagination.pageIndex;
  return (
    <div className="tablePager">
      <span>
        第 {pageCount ? pageIndex + 1 : 0} / {pageCount || 0} 页 · 共 {total} 条
      </span>
      <div className="inlineActions">
        <Button className="miniButton" variant="secondary" size="sm" disabled={!table.getCanPreviousPage()} onClick={() => table.previousPage()}>
          上一页
        </Button>
        <Button className="miniButton" variant="secondary" size="sm" disabled={!table.getCanNextPage()} onClick={() => table.nextPage()}>
          下一页
        </Button>
        <Select className="compactInput" value={String(table.getState().pagination.pageSize)} onChange={(event) => table.setPageSize(Number(event.target.value))}>
          <option value="10">10 条</option>
          <option value="25">25 条</option>
          <option value="50">50 条</option>
        </Select>
      </div>
    </div>
  );
}

function ModelTable({
  rows,
  scope,
  showAll,
  onShowAllChange,
  selected,
  loading,
  busy,
  onPatch,
  onReset
}: {
  rows: DashboardModel[];
  scope?: string;
  showAll: boolean;
  onShowAllChange: (checked: boolean) => void;
  selected: DebugAccount | null;
  loading: boolean;
  busy: boolean;
  onPatch: (id: string, payload: Partial<DashboardModel>) => void;
  onReset: (id: string) => void;
}) {
  const blocked = new Set(selected?.blocked_models ?? []);
  const [sorting, setSorting] = useState<SortingState>([]);
  const columns = useMemo<ColumnDef<DashboardModel>[]>(
    () => [
      {
        id: "model",
        header: "模型",
        cell: ({ row }) => <span className="wideCell">{row.original.display_name || row.original.id}</span>
      },
      {
        id: "provider",
        header: "提供方",
        cell: ({ row }) => row.original.provider || row.original.family || "windsurf"
      },
      {
        id: "uid",
        header: "UID",
        cell: ({ row }) => row.original.model_uid || row.original.model_enum || "-"
      },
      {
        accessorKey: "credit",
        header: "消耗",
        cell: ({ row }) => row.original.credit ?? "-"
      },
      {
        id: "direct",
        header: "直连",
        cell: ({ row }) => (row.original.direct_supported ? "支持" : "不支持")
      },
      {
        id: "status",
        header: "状态",
        cell: ({ row }) => <span className={`pill ${row.original.supported ? "good" : "warn"}`}>{row.original.supported ? "支持" : "不支持"}</span>
      },
      {
        id: "visible",
        header: "可见",
        cell: ({ row }) => (
          <Switch
            checked={row.original.visible !== false}
            disabled={busy}
            onCheckedChange={(checked) => onPatch(row.original.id, { visible: checked })}
            aria-label={`${row.original.id} 可见`}
          />
        )
      },
      {
        id: "deprecated",
        header: "废弃",
        cell: ({ row }) => (
          <Switch
            checked={!!row.original.deprecated}
            disabled={busy}
            onCheckedChange={(checked) => onPatch(row.original.id, { deprecated: checked })}
            aria-label={`${row.original.id} 废弃`}
          />
        )
      },
      {
        id: "selected_account",
        header: "当前账号",
        cell: ({ row }) => (selected ? (blocked.has(row.original.id) ? "已屏蔽" : "允许") : "-")
      },
      {
        id: "reason",
        header: "原因",
        cell: ({ row }) => (
          <Input
            className="inlineInput"
            value={row.original.unsupported_reason || ""}
            placeholder={row.original.supported ? "无" : "原因"}
            disabled={busy}
            onChange={(event) => onPatch(row.original.id, { unsupported_reason: event.target.value })}
          />
        )
      },
      {
        id: "action",
        header: "操作",
        cell: ({ row }) => (
          <div className="inlineActions">
            <Button className="miniButton" variant="secondary" size="sm" disabled={busy} onClick={() => onPatch(row.original.id, { supported: !row.original.supported })}>
              {row.original.supported ? "禁用" : "启用"}
            </Button>
            <Button className="miniButton" variant="secondary" size="sm" disabled={busy} onClick={() => onReset(row.original.id)}>
              重置
            </Button>
          </div>
        )
      }
    ],
    [blocked, busy, onPatch, onReset, selected]
  );
  const table = useReactTable({
    data: rows,
    columns,
    state: { sorting },
    onSortingChange: setSorting,
    getCoreRowModel: getCoreRowModel(),
    getSortedRowModel: getSortedRowModel(),
    getPaginationRowModel: getPaginationRowModel(),
    getRowId: (row) => row.id
  });
  React.useEffect(() => {
    table.setPageSize(25);
  }, [table]);
  if (loading) return <div className="empty">加载模型目录...</div>;
  if (!rows.length) return <div className="empty">暂无模型目录。</div>;
  return (
    <div className="modelDirectory">
      <div className="modelDirectoryHeader">
        <div>
          <strong>{showAll ? "完整模型目录" : "公开生产模型"}</strong>
          <span>{showAll ? "包含 Node 兼容目录和未启用模型" : "仅显示 /v1/models 对客户端暴露的模型"}</span>
        </div>
        <label className="switchLabel">
          <Switch checked={showAll} onCheckedChange={onShowAllChange} />
          显示完整目录
        </label>
      </div>
      <div className="modelDirectoryMeta">
        <span className="pill neutral">范围：{scope === "all" ? "完整" : "公开"}</span>
        <span>{rows.length} 个模型</span>
      </div>
      <div className="tableWrap">
        <table>
          <thead>
            {table.getHeaderGroups().map((headerGroup) => (
              <tr key={headerGroup.id}>
                {headerGroup.headers.map((header) => (
                  <SortableTH key={header.id} header={header} />
                ))}
              </tr>
            ))}
          </thead>
          <tbody>
            {table.getRowModel().rows.map((row) => (
              <tr key={row.id}>
                {row.getVisibleCells().map((cell) => (
                  <td key={cell.id}>{flexRender(cell.column.columnDef.cell, cell.getContext())}</td>
                ))}
              </tr>
            ))}
          </tbody>
        </table>
        <TablePager table={table} total={rows.length} />
      </div>
    </div>
  );
}

function StatusList({ rows }: { rows: [string, string][] }) {
  return (
    <div className="statusList">
      {rows.map(([label, value]) => (
        <div className="statusRow" key={label}>
          <span>{label}</span>
          <strong>{value}</strong>
        </div>
      ))}
    </div>
  );
}

function SecretStatusList({ security }: { security: RuntimeConfigSnapshot["security"] | undefined }) {
  const rows: Array<[string, SecretStatus | undefined]> = [
    ["接口密钥", security?.api_keys],
    ["控制台密码", security?.dashboard_password],
    ["Redis 密码", security?.redis_password]
  ];
  return (
    <div className="secretList">
      {rows.map(([label, item]) => (
        <div className="secretRow" key={label}>
          <div>
            <strong>{label}</strong>
            <span>{localizeSecretMessage(item?.message || "未知")}</span>
            <code>{item?.environment || "-"}</code>
          </div>
          <span className={`pill ${item?.safe ? "good" : "bad"}`}>{item?.safe ? "安全" : "需处理"}</span>
        </div>
      ))}
    </div>
  );
}

function AvailabilityPanel({
  accounts,
  models,
  selectedAccountIDs,
  busy,
  onRefresh,
  onPrune,
  onProbeModel,
  onProbeAccountModel,
  onClearCooldown,
  onClearBreaker,
  onClearModelBreakers
}: {
  accounts: DebugAccount[];
  models: DashboardModel[];
  selectedAccountIDs: number[];
  busy: boolean;
  onRefresh: () => void;
  onPrune: () => void;
  onProbeModel: (model: string, accountIDs: number[]) => void;
  onProbeAccountModel: (accountID: number, model: string) => void;
  onClearCooldown: (accountID: number, model: string) => void;
  onClearBreaker: (accountID: number, model: string) => void;
  onClearModelBreakers: (model: string) => void;
}) {
  const rows = availabilityRows(accounts);
  const [probeModel, setProbeModel] = useState("claude-sonnet-4.6");
  const [probeAccountID, setProbeAccountID] = useState("");
  const directModels = models.filter((model) => model.direct_supported && !model.deprecated);
  React.useEffect(() => {
    if (!probeModel && directModels[0]?.id) setProbeModel(directModels[0].id);
  }, [probeModel, directModels]);
  const [sorting, setSorting] = useState<SortingState>([]);
  const columns = useMemo<ColumnDef<AvailabilityRow>[]>(
    () => [
      { accessorKey: "type", header: "类型", cell: ({ row }) => (row.original.type === "cooldown" ? "冷却" : row.original.type === "breaker" ? "Breaker" : "错误") },
      { accessorKey: "account_id", header: "账号", cell: ({ row }) => (row.original.account_id ? `#${row.original.account_id}` : "全局") },
      { accessorKey: "email", header: "邮箱", cell: ({ row }) => <span className="wideCell">{row.original.email || "-"}</span> },
      { accessorKey: "model", header: "模型", cell: ({ row }) => <span className="wideCell">{row.original.model}</span> },
      { accessorKey: "value", header: "状态", cell: ({ row }) => row.original.value },
      {
        id: "action",
        header: "操作",
        enableSorting: false,
        cell: ({ row }) => (
          <div className="inlineActions">
            <Button className="miniButton" variant="secondary" size="sm" disabled={busy || !row.original.account_id} onClick={() => onProbeAccountModel(row.original.account_id, row.original.model)}>
              探测
            </Button>
            {row.original.type === "cooldown" ? (
              <Button className="miniButton" variant="secondary" size="sm" disabled={busy || !row.original.account_id} onClick={() => onClearCooldown(row.original.account_id, row.original.model)}>
                清冷却
              </Button>
            ) : null}
            {row.original.type === "breaker" ? (
              <>
                <Button className="miniButton" variant="secondary" size="sm" disabled={busy || !row.original.account_id} onClick={() => onClearBreaker(row.original.account_id, row.original.model)}>
                  清账号 Breaker
                </Button>
                <Button className="miniButton" variant="secondary" size="sm" disabled={busy} onClick={() => onClearModelBreakers(row.original.model)}>
                  清全模型
                </Button>
              </>
            ) : null}
          </div>
        )
      }
    ],
    [busy, onClearBreaker, onClearCooldown, onClearModelBreakers]
  );
  const table = useReactTable({
    data: rows,
    columns,
    state: { sorting },
    onSortingChange: setSorting,
    getCoreRowModel: getCoreRowModel(),
    getSortedRowModel: getSortedRowModel(),
    getPaginationRowModel: getPaginationRowModel(),
    getRowId: (row) => `${row.type}-${row.account_id}-${row.model}`
  });
  React.useEffect(() => {
    table.setPageSize(10);
  }, [table]);
  return (
    <div className="manager">
      <StatusList
        rows={[
          ["账号总数", String(accounts.length)],
          ["启用账号", String(accounts.filter((a) => a.enabled).length)],
          ["封禁账号", String(accounts.filter((a) => a.banned).length)],
          ["低额度账号", String(accounts.filter((a) => a.quota_score > 0 && a.quota_score < 5).length)],
          ["限流账号", String(accounts.filter((a) => activeCooldown(a.rate_limited_until) !== "-").length)]
        ]}
      />
      <div className="probeBar">
        <Select className="compactInput wide" value={probeModel} onChange={(event) => setProbeModel(event.target.value)}>
          {(directModels.length ? directModels : models.slice(0, 20)).map((model) => (
            <option key={model.id} value={model.id}>
              {model.id}
            </option>
          ))}
        </Select>
        <Select className="compactInput wide" value={probeAccountID} onChange={(event) => setProbeAccountID(event.target.value)}>
          <option value="">自动选择账号</option>
          {accounts.map((account) => (
            <option key={account.id} value={String(account.id)}>
              #{account.id} {account.email}
            </option>
          ))}
        </Select>
        <Button
          className="secondaryButton"
          variant="secondary"
          disabled={busy || !probeModel}
          onClick={() => {
            const accountID = Number(probeAccountID);
            if (accountID > 0) onProbeAccountModel(accountID, probeModel);
            else onProbeModel(probeModel, selectedAccountIDs);
          }}
        >
          <Play size={15} />
          {selectedAccountIDs.length && !probeAccountID ? `探测已选 ${selectedAccountIDs.length} 个` : "手动探测模型"}
        </Button>
      </div>
      <div className="buttonRow">
        <Button className="secondaryButton" variant="secondary" disabled={busy || !accounts.length} onClick={onRefresh}>
          <RefreshCcw size={15} />
          手动刷新状态
        </Button>
        <Button className="secondaryButton" variant="secondary" disabled={busy} onClick={onPrune}>
          清理过期状态
        </Button>
      </div>
      {!rows.length ? (
        <div className="empty compactEmpty">暂无活动冷却、Breaker 或错误窗口。</div>
      ) : (
        <div className="tableWrap compactTable">
          <table>
            <thead>
              {table.getHeaderGroups().map((headerGroup) => (
                <tr key={headerGroup.id}>
                  {headerGroup.headers.map((header) => (
                    <SortableTH key={header.id} header={header} />
                  ))}
                </tr>
              ))}
            </thead>
            <tbody>
              {table.getRowModel().rows.map((row) => (
                <tr key={row.id}>
                  {row.getVisibleCells().map((cell) => (
                    <td key={cell.id}>{flexRender(cell.column.columnDef.cell, cell.getContext())}</td>
                  ))}
                </tr>
              ))}
            </tbody>
          </table>
          <TablePager table={table} total={rows.length} />
        </div>
      )}
    </div>
  );
}

function ProxyPanel({
  snapshot,
  value,
  accounts,
  busy,
  onChange,
  onAdd,
  onGenerate,
  onPatch,
  onDelete,
  selectedAccountIDs,
  accountSelection,
  onAccountSelectionChange,
  onBindAccounts,
  onGenerateForAccounts,
  onDynamicBind,
  onDynamicRotate,
  onDynamicVerify,
  onDynamicSuspend,
  onDynamicResume,
  onDynamicClear,
  onBindingAction,
  onRunMaintenance
}: {
  snapshot: ProxySnapshot | undefined;
  value: string;
  accounts: DebugAccount[];
  busy: boolean;
  onChange: (value: string) => void;
  onAdd: () => void;
  onGenerate: () => void;
  onPatch: (id: string, payload: { enabled?: boolean; cooldown_seconds?: number; test?: boolean }) => void;
  onDelete: (id: string) => void;
  selectedAccountIDs: number[];
  accountSelection: RowSelectionState;
  onAccountSelectionChange: React.Dispatch<React.SetStateAction<RowSelectionState>>;
  onBindAccounts: (proxyID: string, accountIDs: number[]) => void;
  onGenerateForAccounts: (accountIDs: number[]) => void;
  onDynamicBind: (accountIDs: number[]) => void;
  onDynamicRotate: (accountIDs: number[]) => void;
  onDynamicVerify: (accountIDs: number[]) => void;
  onDynamicSuspend: (accountIDs: number[]) => void;
  onDynamicResume: (accountIDs: number[]) => void;
  onDynamicClear: (accountIDs: number[]) => void;
  onBindingAction: (accountID: number, action: string) => void;
  onRunMaintenance: () => void;
}) {
  const rows = snapshot?.entries ?? [];
  const bindingRows = snapshot?.bindings ?? [];
  const accountMap = useMemo(() => new Map(accounts.map((account) => [account.id, account])), [accounts]);
  const bindingMap = useMemo(() => new Map(bindingRows.map((binding) => [binding.account_id, binding])), [bindingRows]);
  const [deleteTarget, setDeleteTarget] = useState<ProxyEntry | null>(null);
  const [sorting, setSorting] = useState<SortingState>([]);
  const [bindingSorting, setBindingSorting] = useState<SortingState>([]);
  const [accountSorting, setAccountSorting] = useState<SortingState>([]);
  const columns = useMemo<ColumnDef<ProxyEntry>[]>(
    () => [
      {
        id: "proxy",
        header: "代理",
        cell: ({ row }) => <span className="wideCell">{row.original.masked_url || row.original.id}</span>
      },
      {
        id: "status",
        header: "状态",
        cell: ({ row }) => <span className={`pill ${row.original.enabled ? "good" : "warn"}`}>{row.original.enabled ? "启用" : "停用"}</span>
      },
      {
        accessorKey: "inflight",
        header: "进行中"
      },
      {
        id: "ok_fail",
        header: "成功/失败",
        cell: ({ row }) => `${row.original.successes}/${row.original.failures}`
      },
      {
        id: "last_test",
        header: "最近测试",
        cell: ({ row }) => (row.original.last_test_status ? `${row.original.last_test_status} ${row.original.last_test_latency_ms ?? 0}ms` : "-")
      },
      {
        id: "cooldown",
        header: "冷却",
        cell: ({ row }) => activeCooldown(row.original.cooldown_until)
      },
      {
        id: "action",
        header: "操作",
        cell: ({ row }) => {
          const entry = row.original;
          return (
            <>
              <div className="inlineActions">
                <Button className="miniButton" variant="secondary" size="sm" disabled={busy} onClick={() => onPatch(entry.id, { enabled: !entry.enabled })}>
                  {entry.enabled ? "停用" : "启用"}
                </Button>
                <Button className="miniButton" variant="secondary" size="sm" disabled={busy} onClick={() => onPatch(entry.id, { test: true })}>
                  测试
                </Button>
                <Button className="miniButton" variant="secondary" size="sm" disabled={busy} onClick={() => onPatch(entry.id, { cooldown_seconds: entry.cooldown_until ? 0 : 120 })}>
                  {entry.cooldown_until ? "解冷" : "冷却"}
                </Button>
                <Button className="miniButton" variant="secondary" size="sm" disabled={busy || selectedAccountIDs.length === 0} onClick={() => onBindAccounts(entry.id, selectedAccountIDs)}>
                  绑到已选账号
                </Button>
                <Button
                  className="miniButton"
                  variant="secondary"
                  size="sm"
                  disabled={busy}
                  onClick={() => setDeleteTarget(entry)}
                >
                  删除
                </Button>
              </div>
              {entry.last_error ? <small className="errorText">{entry.last_error}</small> : null}
            </>
          );
        }
      }
    ],
    [busy, onBindAccounts, onPatch, selectedAccountIDs]
  );
  const table = useReactTable({
    data: rows,
    columns,
    state: { sorting },
    onSortingChange: setSorting,
    getCoreRowModel: getCoreRowModel(),
    getSortedRowModel: getSortedRowModel(),
    getPaginationRowModel: getPaginationRowModel(),
    getRowId: (row) => row.id
  });
  React.useEffect(() => {
    table.setPageSize(10);
  }, [table]);
  const bindingColumns = useMemo<ColumnDef<ProxyBinding>[]>(
    () => [
      {
        id: "account",
        header: "账号",
        cell: ({ row }) => {
          const account = accountMap.get(row.original.account_id);
          return (
            <span className="wideCell">
              #{row.original.account_id} {account?.email ?? ""}
            </span>
          );
        }
      },
      {
        id: "status",
        header: "状态",
        cell: ({ row }) => <span className={`pill ${bindingTone(row.original.status)}`}>{bindingStatusText(row.original.status)}</span>
      },
      {
        id: "egress",
        header: "出口",
        cell: ({ row }) => (
          <span className="wideCell">
            {row.original.egress_ip || "-"} {formatLocation(row.original)}
          </span>
        )
      },
      {
        id: "proxy",
        header: "绑定代理",
        cell: ({ row }) => <span className="wideCell">{row.original.masked_url || `${row.original.host || "-"}:${row.original.port || "-"}`}</span>
      },
      {
        id: "ttl",
        header: "剩余 TTL",
        cell: ({ row }) => formatDuration(row.original.remaining_ms)
      },
      {
        id: "verified",
        header: "最近验证",
        cell: ({ row }) => formatDateTime(row.original.last_verified_at)
      },
      {
        id: "fail",
        header: "失败",
        cell: ({ row }) => String(row.original.fail_count ?? 0)
      },
      {
        id: "action",
        header: "操作",
        cell: ({ row }) => {
          const binding = row.original;
          const suspended = binding.status === "suspended";
          return (
            <>
              <div className="inlineActions">
                <Button className="miniButton" variant="secondary" size="sm" disabled={busy} onClick={() => onBindingAction(binding.account_id, "verify")}>
                  验证
                </Button>
                <Button className="miniButton" variant="secondary" size="sm" disabled={busy} onClick={() => onBindingAction(binding.account_id, "rotate")}>
                  轮换
                </Button>
                <Button className="miniButton" variant="secondary" size="sm" disabled={busy} onClick={() => onBindingAction(binding.account_id, suspended ? "resume" : "suspend")}>
                  {suspended ? "恢复" : "暂停"}
                </Button>
                <Button className="miniButton" variant="secondary" size="sm" disabled={busy} onClick={() => onBindingAction(binding.account_id, "clear")}>
                  清除
                </Button>
              </div>
              {binding.verify_error ? <small className="errorText">{binding.verify_error}</small> : null}
            </>
          );
        }
      }
    ],
    [accountMap, busy, onBindingAction]
  );
  const bindingTable = useReactTable({
    data: bindingRows,
    columns: bindingColumns,
    state: { sorting: bindingSorting },
    onSortingChange: setBindingSorting,
    getCoreRowModel: getCoreRowModel(),
    getSortedRowModel: getSortedRowModel(),
    getPaginationRowModel: getPaginationRowModel(),
    getRowId: (row) => String(row.account_id)
  });
  React.useEffect(() => {
    bindingTable.setPageSize(10);
  }, [bindingTable]);
  const accountColumns = useMemo<ColumnDef<DebugAccount>[]>(
    () => [
      {
        id: "select",
        header: ({ table }) => (
          <input
            type="checkbox"
            aria-label="选择当前页账号"
            checked={table.getIsAllPageRowsSelected()}
            ref={(node) => {
              if (node) node.indeterminate = table.getIsSomePageRowsSelected();
            }}
            onChange={table.getToggleAllPageRowsSelectedHandler()}
          />
        ),
        cell: ({ row }) => (
          <input
            type="checkbox"
            aria-label={`选择账号 ${row.original.id}`}
            checked={row.getIsSelected()}
            ref={(node) => {
              if (node) node.indeterminate = row.getIsSomeSelected();
            }}
            onClick={(event) => event.stopPropagation()}
            onChange={row.getToggleSelectedHandler()}
          />
        ),
        enableSorting: false
      },
      { accessorKey: "id", header: "ID" },
      {
        accessorKey: "email",
        header: "账号",
        cell: ({ row }) => <span className="wideCell">{row.original.email || "未知"}</span>
      },
      {
        id: "account_status",
        header: "账号状态",
        cell: ({ row }) => (
          <span className={`pill ${row.original.banned ? "bad" : row.original.enabled ? "good" : "warn"}`}>
            {row.original.banned ? "封禁" : row.original.enabled ? "启用" : "停用"}
          </span>
        )
      },
      {
        id: "binding_status",
        header: "绑定状态",
        cell: ({ row }) => {
          const binding = bindingMap.get(row.original.id);
          return <span className={`pill ${bindingTone(binding?.status)}`}>{binding ? bindingStatusText(binding.status) : "未绑定"}</span>;
        }
      },
      {
        id: "egress",
        header: "出口 IP",
        cell: ({ row }) => {
          const binding = bindingMap.get(row.original.id);
          return (
            <span className="wideCell">
              {binding?.egress_ip || "-"} {binding ? formatLocation(binding) : ""}
            </span>
          );
        }
      },
      {
        id: "remaining",
        header: "剩余",
        cell: ({ row }) => formatDuration(bindingMap.get(row.original.id)?.remaining_ms)
      },
      {
        id: "static_proxy",
        header: "静态代理",
        cell: ({ row }) => (row.original.proxy_url_set || row.original.proxy_url ? "已设置" : "-")
      },
      {
        id: "error",
        header: "错误",
        cell: ({ row }) => <span className="wideCell">{bindingMap.get(row.original.id)?.verify_error || "-"}</span>
      }
    ],
    [bindingMap]
  );
  const accountTable = useReactTable({
    data: accounts,
    columns: accountColumns,
    state: { sorting: accountSorting, rowSelection: accountSelection },
    onSortingChange: setAccountSorting,
    onRowSelectionChange: onAccountSelectionChange,
    getCoreRowModel: getCoreRowModel(),
    getSortedRowModel: getSortedRowModel(),
    getPaginationRowModel: getPaginationRowModel(),
    getRowId: (row) => String(row.id)
  });
  React.useEffect(() => {
    accountTable.setPageSize(10);
  }, [accountTable]);
  return (
    <div className="manager">
      <StatusList
        rows={[
          ["账号绑定", snapshot?.account_binding ? "开启" : "关闭"],
          ["自动绑定新账号", snapshot?.auto_bind_new_accounts ? "开启" : "关闭"],
          ["续绑阈值", formatDuration(snapshot?.renew_before_ms)],
          ["代理 TTL", `${snapshot?.ttl_minutes ?? "-"} 分钟`],
          ["已绑定 / 未绑定", `${snapshot?.summary?.bound ?? 0} / ${snapshot?.summary?.unbound ?? 0}`],
          ["即将过期", String(snapshot?.summary?.expiring_soon ?? 0)],
          ["失败 / 暂停", `${snapshot?.summary?.failed ?? 0} / ${snapshot?.summary?.suspended ?? 0}`],
          ["维护周期 / 并发", `${formatDuration(snapshot?.worker_interval_ms)} / ${snapshot?.worker_concurrency ?? 0}`],
          ["代理密码", snapshot?.password_set ? "已设置" : "未设置"]
        ]}
      />
      <div className="proxyNotice">
        <strong>账号级动态代理</strong>
        <span>
          在这里直接勾选账号批量绑定、验证、换 IP、暂停或清除。维护计划会处理失败、过期、快过期绑定；开启“自动绑定新账号”后也会给未绑定的启用账号补绑定。
        </span>
      </div>
      <div className="buttonRow">
        <Button className="miniButton" variant="secondary" size="sm" disabled={busy || selectedAccountIDs.length === 0} onClick={() => onDynamicBind(selectedAccountIDs)}>
          绑定已选账号 IP
        </Button>
        <Button className="miniButton" variant="secondary" size="sm" disabled={busy || selectedAccountIDs.length === 0} onClick={() => onDynamicRotate(selectedAccountIDs)}>
          更新/换 IP
        </Button>
        <Button className="miniButton" variant="secondary" size="sm" disabled={busy || selectedAccountIDs.length === 0} onClick={() => onDynamicVerify(selectedAccountIDs)}>
          验证已选 IP
        </Button>
        <Button className="miniButton" variant="secondary" size="sm" disabled={busy || selectedAccountIDs.length === 0} onClick={() => onDynamicSuspend(selectedAccountIDs)}>
          暂停已选绑定
        </Button>
        <Button className="miniButton" variant="secondary" size="sm" disabled={busy || selectedAccountIDs.length === 0} onClick={() => onDynamicResume(selectedAccountIDs)}>
          恢复已选绑定
        </Button>
        <Button className="miniButton" variant="secondary" size="sm" disabled={busy || selectedAccountIDs.length === 0} onClick={() => onDynamicClear(selectedAccountIDs)}>
          解绑已选账号
        </Button>
        <Button className="miniButton" variant="secondary" size="sm" disabled={busy} onClick={onRunMaintenance}>
          自动检测并续绑
        </Button>
      </div>
      <div className="selectionHint">
        已选择 {selectedAccountIDs.length} 个账号；批量操作只作用于下面表格里勾选的账号。
      </div>
      {!accounts.length ? (
        <div className="empty compactEmpty">暂无账号，先导入账号后再绑定动态代理。</div>
      ) : (
        <div className="tableWrap compactTable">
          <table>
            <thead>
              {accountTable.getHeaderGroups().map((headerGroup) => (
                <tr key={headerGroup.id}>
                  {headerGroup.headers.map((header) => (
                    <SortableTH key={header.id} header={header} />
                  ))}
                </tr>
              ))}
            </thead>
            <tbody>
              {accountTable.getRowModel().rows.map((row) => (
                <tr key={row.id}>
                  {row.getVisibleCells().map((cell) => (
                    <td key={cell.id}>{flexRender(cell.column.columnDef.cell, cell.getContext())}</td>
                  ))}
                </tr>
              ))}
            </tbody>
          </table>
          <TablePager table={accountTable} total={accounts.length} />
        </div>
      )}
      {!bindingRows.length ? (
        <div className="empty compactEmpty">暂无账号级动态绑定。开启账号绑定后，可对选中的账号执行绑定或等待维护任务自动处理。</div>
      ) : (
        <div className="tableWrap compactTable">
          <table>
            <thead>
              {bindingTable.getHeaderGroups().map((headerGroup) => (
                <tr key={headerGroup.id}>
                  {headerGroup.headers.map((header) => (
                    <SortableTH key={header.id} header={header} />
                  ))}
                </tr>
              ))}
            </thead>
            <tbody>
              {bindingTable.getRowModel().rows.map((row) => (
                <tr key={row.id}>
                  {row.getVisibleCells().map((cell) => (
                    <td key={cell.id}>{flexRender(cell.column.columnDef.cell, cell.getContext())}</td>
                  ))}
                </tr>
              ))}
            </tbody>
          </table>
          <TablePager table={bindingTable} total={bindingRows.length} />
        </div>
      )}
      <div className="buttonRow">
        <Input className="inlineInput wide" value={value} placeholder="http://user:pass@host:port" onChange={(event) => onChange(event.target.value)} />
        <Button className="miniButton" variant="secondary" size="sm" disabled={busy || !value.trim()} onClick={onAdd}>
          添加
        </Button>
        <Button className="miniButton" variant="secondary" size="sm" disabled={busy} onClick={onGenerate}>
          生成代理
        </Button>
        <Button className="miniButton" variant="secondary" size="sm" disabled={busy || selectedAccountIDs.length === 0} onClick={() => onGenerateForAccounts(selectedAccountIDs)}>
          生成池代理并写入账号
        </Button>
      </div>
      <StatusList
        rows={[
          ["提供商", snapshot?.provider || "-"],
          ["协议 / 主机", `${snapshot?.protocol || "-"} / ${snapshot?.host || "-"}:${snapshot?.port || "-"}`],
          ["地区", `${snapshot?.region || "-"} ${snapshot?.state || ""}`.trim()],
          ["TTL", `${snapshot?.ttl_minutes ?? "-"} 分钟`],
          ["密码", snapshot?.password_set ? "已设置" : "未设置"]
        ]}
      />
      {!rows.length ? (
        <div className="empty">暂无动态代理，当前只使用账号代理或默认代理。</div>
      ) : (
        <div className="tableWrap compactTable">
          <table>
            <thead>
              {table.getHeaderGroups().map((headerGroup) => (
                <tr key={headerGroup.id}>
                  {headerGroup.headers.map((header) => (
                    <SortableTH key={header.id} header={header} />
                  ))}
                </tr>
              ))}
            </thead>
            <tbody>
              {table.getRowModel().rows.map((row) => (
                <tr key={row.id}>
                  {row.getVisibleCells().map((cell) => (
                    <td key={cell.id}>{flexRender(cell.column.columnDef.cell, cell.getContext())}</td>
                  ))}
                </tr>
              ))}
            </tbody>
          </table>
          <TablePager table={table} total={rows.length} />
        </div>
      )}
      <ConfirmDialog
        open={!!deleteTarget}
        title="删除代理"
        description={`确认删除动态代理 ${deleteTarget?.masked_url || deleteTarget?.id || ""}？已绑定到账号的代理配置不会被自动改写。`}
        confirmLabel="删除代理"
        cancelLabel="取消"
        busy={busy}
        onCancel={() => setDeleteTarget(null)}
        onConfirm={() => {
          if (!deleteTarget) return;
          onDelete(deleteTarget.id);
          setDeleteTarget(null);
        }}
      />
    </div>
  );
}

function SettingsPanel({
  config,
  busy,
  onSave
}: {
  config: RuntimeConfigSnapshot | undefined;
  busy: boolean;
  onSave: (payload: RuntimeConfigSnapshot) => void;
}) {
  const [draft, setDraft] = useState<RuntimeConfigSnapshot>({});
  React.useEffect(() => {
    if (config) setDraft(config);
  }, [config]);
  if (!config) return <div className="empty">加载运行配置...</div>;
  const direct = draft.direct ?? {};
  const server = draft.server ?? {};
  const health = draft.health ?? {};
  const scheduler = draft.scheduler ?? {};
  const usage = draft.usage ?? {};
  const virtualCache = usage.virtual_cache ?? {};
  const proxy = draft.proxy ?? {};
  const log = draft.log ?? {};
  const secrets = draft.secrets ?? {};
  return (
    <div className="manager">
      <div className="formGrid">
        <Field label="直连上游地址">
          <Input
            value={(direct.hosts ?? []).join(",")}
            onChange={(event) => setDraft({ ...draft, direct: { ...direct, hosts: parseLines(event.target.value) } })}
          />
        </Field>
        <Field label="直连超时">
          <Input
            type="number"
            value={direct.timeout_seconds ?? 30}
            onChange={(event) => setDraft({ ...draft, direct: { ...direct, timeout_seconds: Number(event.target.value) } })}
          />
        </Field>
        <Field label="最大请求体">
          <Input
            type="number"
            value={server.max_request_body_bytes ?? 26214400}
            onChange={(event) => setDraft({ ...draft, server: { ...server, max_request_body_bytes: Number(event.target.value) } })}
          />
        </Field>
        <Field label="健康检查模型">
          <Input value={health.model ?? ""} onChange={(event) => setDraft({ ...draft, health: { ...health, model: event.target.value } })} />
        </Field>
        <Field label="健康检查间隔">
          <Input
            type="number"
            value={health.interval_seconds ?? 300}
            onChange={(event) => setDraft({ ...draft, health: { ...health, interval_seconds: Number(event.target.value) } })}
          />
        </Field>
        <Field label="单账号最大并发">
          <Input
            type="number"
            value={scheduler.max_inflight_per_account ?? 4}
            onChange={(event) => setDraft({ ...draft, scheduler: { ...scheduler, max_inflight_per_account: Number(event.target.value) } })}
          />
        </Field>
        <Field label="预约 TTL">
          <Input
            type="number"
            value={scheduler.reservation_ttl_seconds ?? 180}
            onChange={(event) => setDraft({ ...draft, scheduler: { ...scheduler, reservation_ttl_seconds: Number(event.target.value) } })}
          />
        </Field>
        <Field label="虚拟 cache 模式">
          <Select
            value={virtualCache.mode ?? "conservative"}
            onChange={(event) => setDraft({ ...draft, usage: { ...usage, virtual_cache: { ...virtualCache, mode: event.target.value } } })}
          >
            <option value="conservative">conservative</option>
            <option value="dynamic">dynamic</option>
          </Select>
        </Field>
        <Field label="虚拟 cache TTL">
          <Select
            value={virtualCache.default_ttl ?? "5m"}
            onChange={(event) => setDraft({ ...draft, usage: { ...usage, virtual_cache: { ...virtualCache, default_ttl: event.target.value } } })}
          >
            <option value="5m">5m</option>
            <option value="1h">1h</option>
          </Select>
        </Field>
        <Field label="未缓存输入 tokens">
          <Input
            type="number"
            value={virtualCache.uncached_input_tokens ?? 64}
            onChange={(event) => setDraft({ ...draft, usage: { ...usage, virtual_cache: { ...virtualCache, uncached_input_tokens: Number(event.target.value) } } })}
          />
        </Field>
        <Field label="虚拟输入上下限">
          <Input
            value={`${virtualCache.min_input_tokens ?? 1},${virtualCache.max_input_tokens ?? 4096}`}
            onChange={(event) => {
              const [min, max] = parseNumberPair(event.target.value);
              setDraft({ ...draft, usage: { ...usage, virtual_cache: { ...virtualCache, min_input_tokens: min || 1, max_input_tokens: max || 4096 } } });
            }}
          />
        </Field>
        <Field label="创建 tokens 上限">
          <Input
            type="number"
            value={virtualCache.max_creation_tokens ?? 8192}
            onChange={(event) => setDraft({ ...draft, usage: { ...usage, virtual_cache: { ...virtualCache, max_creation_tokens: Number(event.target.value) } } })}
          />
        </Field>
        <Field label="创建 jitter">
          <Input
            type="number"
            step="0.05"
            value={virtualCache.creation_jitter_ratio ?? 0}
            onChange={(event) => setDraft({ ...draft, usage: { ...usage, virtual_cache: { ...virtualCache, creation_jitter_ratio: Number(event.target.value) } } })}
          />
        </Field>
        <Field label="默认代理">
          <Input value={proxy.default ?? ""} onChange={(event) => setDraft({ ...draft, proxy: { ...proxy, default: event.target.value } })} />
        </Field>
        <Field label="动态代理">
          <Input value={(proxy.dynamic ?? []).join(",")} onChange={(event) => setDraft({ ...draft, proxy: { ...proxy, dynamic: parseLines(event.target.value) } })} />
        </Field>
        <Field label="代理测试地址">
          <Input value={proxy.test_url ?? ""} onChange={(event) => setDraft({ ...draft, proxy: { ...proxy, test_url: event.target.value } })} />
        </Field>
        <Field label="代理冷却时间">
          <Input
            type="number"
            value={proxy.cooldown_seconds ?? 120}
            onChange={(event) => setDraft({ ...draft, proxy: { ...proxy, cooldown_seconds: Number(event.target.value) } })}
          />
        </Field>
        <Field label="绑定续期提前量">
          <Input
            type="number"
            value={proxy.renew_before_ms ?? 900000}
            onChange={(event) => setDraft({ ...draft, proxy: { ...proxy, renew_before_ms: Number(event.target.value) } })}
          />
        </Field>
        <Field label="绑定重试次数">
          <Input
            type="number"
            value={proxy.max_bind_retries ?? 3}
            onChange={(event) => setDraft({ ...draft, proxy: { ...proxy, max_bind_retries: Number(event.target.value) } })}
          />
        </Field>
        <Field label="维护间隔">
          <Input
            type="number"
            value={proxy.worker_interval_ms ?? 60000}
            onChange={(event) => setDraft({ ...draft, proxy: { ...proxy, worker_interval_ms: Number(event.target.value) } })}
          />
        </Field>
        <Field label="维护批量大小">
          <Input
            type="number"
            value={proxy.worker_batch_size ?? 20}
            onChange={(event) => setDraft({ ...draft, proxy: { ...proxy, worker_batch_size: Number(event.target.value) } })}
          />
        </Field>
        <Field label="维护并发">
          <Input
            type="number"
            value={proxy.worker_concurrency ?? 3}
            onChange={(event) => setDraft({ ...draft, proxy: { ...proxy, worker_concurrency: Number(event.target.value) } })}
          />
        </Field>
        <Field label="代理提供商">
          <Select value={proxy.provider ?? "novproxy"} onChange={(event) => setDraft({ ...draft, proxy: { ...proxy, provider: event.target.value } })}>
            <option value="novproxy">novproxy</option>
          </Select>
        </Field>
        <Field label="代理协议">
          <Select value={proxy.protocol ?? "http"} onChange={(event) => setDraft({ ...draft, proxy: { ...proxy, protocol: event.target.value } })}>
            <option value="http">http</option>
            <option value="socks5">socks5</option>
          </Select>
        </Field>
        <Field label="代理主机">
          <Input value={proxy.host ?? "us.novproxy.io"} onChange={(event) => setDraft({ ...draft, proxy: { ...proxy, host: event.target.value } })} />
        </Field>
        <Field label="代理端口">
          <Input type="number" value={proxy.port ?? 1000} onChange={(event) => setDraft({ ...draft, proxy: { ...proxy, port: Number(event.target.value) } })} />
        </Field>
        <Field label="用户名模板">
          <Input value={proxy.username_template ?? ""} onChange={(event) => setDraft({ ...draft, proxy: { ...proxy, username_template: event.target.value } })} />
        </Field>
        <Field label="代理密码">
          <Input
            type="password"
            value={proxy.password ?? ""}
            placeholder={proxy.password_set ? "已设置，留空不修改" : ""}
            onChange={(event) => setDraft({ ...draft, proxy: { ...proxy, password: event.target.value } })}
          />
        </Field>
        <Field label="代理区域">
          <Input value={proxy.region ?? "US"} onChange={(event) => setDraft({ ...draft, proxy: { ...proxy, region: event.target.value } })} />
        </Field>
        <Field label="代理州/省">
          <Input value={proxy.state ?? "New Jersey"} onChange={(event) => setDraft({ ...draft, proxy: { ...proxy, state: event.target.value } })} />
        </Field>
        <Field label="代理 TTL 分钟">
          <Input type="number" value={proxy.ttl_minutes ?? 120} onChange={(event) => setDraft({ ...draft, proxy: { ...proxy, ttl_minutes: Number(event.target.value) } })} />
        </Field>
        <Field label="日志级别">
          <Select value={log.level ?? "info"} onChange={(event) => setDraft({ ...draft, log: { ...log, level: event.target.value } })}>
            <option value="debug">debug</option>
            <option value="info">info</option>
            <option value="warn">warn</option>
            <option value="error">error</option>
          </Select>
        </Field>
        <Field label="接口密钥">
          <Textarea
            value={(secrets.api_keys ?? []).join("\n")}
            placeholder="每行一个 API key；留空则不修改"
            onChange={(event) => setDraft({ ...draft, secrets: { ...secrets, api_keys: parseLines(event.target.value) } })}
          />
        </Field>
        <Field label="控制台密码">
          <Input
            type="password"
            value={secrets.dashboard_password ?? ""}
            placeholder="留空则不修改"
            onChange={(event) => setDraft({ ...draft, secrets: { ...secrets, dashboard_password: event.target.value } })}
          />
        </Field>
        <Field label="Redis 密码">
          <Input
            type="password"
            value={secrets.redis_password ?? ""}
            placeholder="仅更新运行时配置；不重连 Redis"
            onChange={(event) => setDraft({ ...draft, secrets: { ...secrets, redis_password: event.target.value } })}
          />
        </Field>
      </div>
      <div className="toggleRow compact">
        <label className="switchLabel">
          <Switch
            checked={health.enabled ?? true}
            onCheckedChange={(checked) => setDraft({ ...draft, health: { ...health, enabled: checked } })}
          />
          启用健康检查
        </label>
        <label className="switchLabel">
          <Switch
            checked={scheduler.redis_enabled ?? false}
            onCheckedChange={(checked) => setDraft({ ...draft, scheduler: { ...scheduler, redis_enabled: checked } })}
          />
          Redis 调度
        </label>
        <label className="switchLabel">
          <Switch
            checked={scheduler.redis_fail_closed ?? false}
            onCheckedChange={(checked) => setDraft({ ...draft, scheduler: { ...scheduler, redis_fail_closed: checked } })}
          />
          Redis 故障时关闭
        </label>
        <label className="switchLabel">
          <Switch
            checked={virtualCache.enabled ?? false}
            onCheckedChange={(checked) => setDraft({ ...draft, usage: { ...usage, virtual_cache: { ...virtualCache, enabled: checked } } })}
          />
          虚拟 cache 账单
        </label>
        <label className="switchLabel">
          <Switch
            checked={proxy.rotate_on_error ?? true}
            onCheckedChange={(checked) => setDraft({ ...draft, proxy: { ...proxy, rotate_on_error: checked } })}
          />
          代理失败轮换
        </label>
        <label className="switchLabel">
          <Switch
            checked={proxy.account_binding ?? false}
            onCheckedChange={(checked) => setDraft({ ...draft, proxy: { ...proxy, account_binding: checked } })}
          />
          账号级动态绑定
        </label>
        <label className="switchLabel">
          <Switch
            checked={proxy.auto_bind_new_accounts ?? false}
            onCheckedChange={(checked) => setDraft({ ...draft, proxy: { ...proxy, auto_bind_new_accounts: checked } })}
          />
          自动绑定新账号
        </label>
        <label className="switchLabel">
          <Switch
            checked={proxy.allow_private ?? false}
            onCheckedChange={(checked) => setDraft({ ...draft, proxy: { ...proxy, allow_private: checked } })}
          />
          允许私有代理地址
        </label>
      </div>
      <div className="buttonRow">
        <Button className="secondaryButton" variant="secondary" disabled={busy} onClick={() => onSave(runtimePatch(draft))}>
          <Save size={15} />
          保存运行配置
        </Button>
      </div>
    </div>
  );
}

function StatsPanel({ stats }: { stats: RequestStats | undefined }) {
  return (
    <div className="statsPanel">
      <StatusList
        rows={[
          ["总请求", String(stats?.total ?? 0)],
          ["成功 / 失败", `${stats?.success ?? 0} / ${stats?.failed ?? 0}`],
          ["重试 / 流式", `${stats?.retried ?? 0} / ${stats?.stream_count ?? 0}`],
          ["错误率", `${(((stats?.error_rate ?? 0) as number) * 100).toFixed(1)}%`],
          ["p50 / p95 / p99", `${stats?.p50_ms ?? 0} / ${stats?.p95_ms ?? 0} / ${stats?.p99_ms ?? 0} ms`],
          ["输入 / 输出", `${stats?.usage?.input ?? 0} / ${stats?.usage?.output ?? 0}`],
          ["缓存读取", `${stats?.cache?.cache_read_tokens ?? 0} (${(((stats?.cache?.cache_read_ratio ?? 0) as number) * 100).toFixed(1)}%)`],
          ["复用命中", `${stats?.cache?.reuse_hits ?? 0} (${(((stats?.cache?.reuse_hit_rate ?? 0) as number) * 100).toFixed(1)}%)`],
          ["工具调用", String(stats?.tool_call_count ?? 0)]
        ]}
      />
      <div className="chartGrid">
        <MiniBarChart title="路由" data={stats?.by_route} />
        <MiniBarChart title="账号" data={stats?.by_account} labelPrefix="#" />
        <MiniBarChart title="延迟" data={stats?.latency_buckets} />
        <MiniBarChart title="错误" data={stats?.by_class} />
      </div>
    </div>
  );
}

function MiniBarChart({ title, data, labelPrefix = "" }: { title: string; data: Record<string, number> | undefined; labelPrefix?: string }) {
  const entries = Object.entries(data ?? {})
    .filter(([, value]) => value > 0)
    .sort((a, b) => b[1] - a[1])
    .slice(0, 6);
  if (!entries.length) {
    return (
      <div className="miniChart">
        <strong>{title}</strong>
        <span className="chartEmpty">暂无</span>
      </div>
    );
  }
  const max = Math.max(...entries.map(([, value]) => value), 1);
  return (
    <div className="miniChart">
      <strong>{title}</strong>
      {entries.map(([label, value]) => (
        <div className="barRow" key={label}>
          <span>{labelPrefix}{label}</span>
          <div className="barTrack" aria-label={`${title} ${label} ${value}`}>
            <div className="barFill" style={{ width: `${Math.max(6, (value / max) * 100)}%` }} />
          </div>
          <em>{value}</em>
        </div>
      ))}
    </div>
  );
}

function EventList({ rows }: { rows: SchedulerEvent[] }) {
  if (!rows.length) return <div className="empty">暂无调度事件。</div>;
  return (
    <div className="events">
      {rows.map((event, index) => (
        <div className="event" key={`${event.time}-${index}`}>
          <div className="eventIcon">{event.class ? <AlertTriangle size={15} /> : <CheckCircle2 size={15} />}</div>
          <div>
            <strong>{event.message || "事件"}</strong>
            <span>
              账号 #{event.account_id || "-"} · {event.model || "全局"} · {event.class || "正常"}
            </span>
          </div>
        </div>
      ))}
    </div>
  );
}

function RequestList({ rows }: { rows: RequestEvent[] }) {
  const [sorting, setSorting] = useState<SortingState>([]);
  const columns = useMemo<ColumnDef<RequestEvent>[]>(
    () => [
      {
        id: "status",
        header: "状态",
        cell: ({ row }) => <span className={`pill ${row.original.status === "ok" ? "good" : "bad"}`}>{row.original.status === "ok" ? "成功" : "失败"}</span>
      },
      {
        accessorKey: "route",
        header: "路由"
      },
      {
        accessorKey: "model",
        header: "模型",
        cell: ({ row }) => <span className="wideCell">{row.original.model || "-"}</span>
      },
      {
        accessorKey: "latency_ms",
        header: "延迟",
        cell: ({ row }) => `${row.original.latency_ms}ms`
      },
      {
        accessorKey: "account_id",
        header: "账号",
        cell: ({ row }) => (row.original.account_id ? `#${row.original.account_id}` : "-")
      },
      {
        accessorKey: "attempt",
        header: "尝试"
      },
      {
        id: "flags",
        header: "标记",
        cell: ({ row }) => {
          const flags = [
            row.original.stream ? "stream" : "",
            row.original.retry ? "retry" : "",
            row.original.reuse_hit ? "reuse" : "",
            row.original.tool_call_count ? `tools:${row.original.tool_call_count}` : ""
          ].filter(Boolean);
          return flags.length ? flags.join(" · ") : "-";
        }
      },
      {
        id: "usage",
        header: "用量",
        cell: ({ row }) => `${row.original.usage_input}/${row.original.usage_output}${row.original.usage_cache_read ? ` c${row.original.usage_cache_read}` : ""}`
      },
      {
        id: "error",
        header: "错误",
        cell: ({ row }) => <span className="wideCell">{row.original.error_class || row.original.error || row.original.reuse_miss_reason || "-"}</span>
      },
      {
        accessorKey: "req_id",
        header: "请求 ID",
        cell: ({ row }) => <code className="requestID">{row.original.req_id}</code>
      }
    ],
    []
  );
  const table = useReactTable({
    data: rows.slice(0, 50),
    columns,
    state: { sorting },
    onSortingChange: setSorting,
    getCoreRowModel: getCoreRowModel(),
    getSortedRowModel: getSortedRowModel(),
    getPaginationRowModel: getPaginationRowModel(),
    getRowId: (row) => `${row.time}-${row.req_id}-${row.attempt}`
  });
  React.useEffect(() => {
    table.setPageSize(25);
  }, [table]);
  if (!rows.length) return <div className="empty">暂无请求记录。</div>;
  return (
    <div className="tableWrap compactTable">
      <table>
        <thead>
          {table.getHeaderGroups().map((headerGroup) => (
            <tr key={headerGroup.id}>
              {headerGroup.headers.map((header) => (
                <SortableTH key={header.id} header={header} />
              ))}
            </tr>
          ))}
        </thead>
        <tbody>
          {table.getRowModel().rows.map((row) => (
            <tr key={row.id}>
              {row.getVisibleCells().map((cell) => (
                <td key={cell.id}>{flexRender(cell.column.columnDef.cell, cell.getContext())}</td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
      <TablePager table={table} total={Math.min(rows.length, 50)} />
    </div>
  );
}

function Roadmap() {
  return (
    <div className="roadmap">
      {[
        ["P0", "直连 API 对齐", "Chat / Messages / Responses 主链路锁定"],
        ["P1", "高可用调度", "Redis 协调、健康刷新、低额度降权"],
        ["P2", "模型目录对齐", "迁移 Node 模型目录和访问控制"],
        ["P3", "管理接口", "账号、代理、运行配置、统计"],
        ["P4", "运维上线", "部署、日志、验收矩阵、可选压测"]
      ].map(([stage, title, detail]) => (
        <div className="roadmapItem" key={stage}>
          <span>{stage}</span>
          <div>
            <strong>{title}</strong>
            <small>{detail}</small>
          </div>
        </div>
      ))}
    </div>
  );
}

function emptyForm(): AccountFormState {
  return { email: "", token: "", tier: "unknown", proxy_url: "", notes: "", enabled: true, banned: false };
}

function accountPatchPayload(form: AccountFormState): Partial<AccountFormState> {
  const payload: Partial<AccountFormState> = {
    email: form.email,
    tier: form.tier,
    notes: form.notes,
    enabled: form.enabled,
    banned: form.banned,
    ...(form.token.trim() ? { token: form.token.trim() } : {})
  };
  if (!form.proxy_url.includes("***")) {
    payload.proxy_url = form.proxy_url;
  }
  return payload;
}

function filterAccounts(rows: DebugAccount[], query: string): DebugAccount[] {
  const q = query.trim().toLowerCase();
  if (!q) return rows;
  return rows.filter((row) =>
    [row.email, row.tier, row.plan_name, row.notes, row.proxy_url, ...(row.blocked_models ?? [])].filter(Boolean).join(" ").toLowerCase().includes(q)
  );
}

function availabilityRows(accounts: DebugAccount[]): AvailabilityRow[] {
  const rows: AvailabilityRow[] = [];
  for (const account of accounts) {
    for (const [model, until] of Object.entries(account.model_cooldowns ?? {})) {
      const value = activeCooldown(until);
      if (value !== "-") rows.push({ type: "cooldown", account_id: account.id, email: account.email, model, value });
    }
    for (const [model, until] of Object.entries(account.model_breakers ?? {})) {
      const value = activeCooldown(until);
      if (value !== "-") rows.push({ type: "breaker", account_id: account.id, email: account.email, model, value });
    }
    for (const [model, count] of Object.entries(account.recent_errors ?? {})) {
      rows.push({ type: "error", account_id: account.id, email: account.email, model, value: `${count} 次` });
    }
  }
  return rows;
}

function parseLines(raw: string): string[] {
  return Array.from(new Set(raw.split(/\r?\n|,/).map((line) => line.trim()).filter(Boolean))).sort();
}

function parseNumberPair(raw: string): [number, number] {
  const nums = raw
    .split(/[\r\n,，/ ]/)
    .map((part) => Number(part.trim()))
    .filter((value) => Number.isFinite(value));
  return [nums[0] ?? 0, nums[1] ?? 0];
}

function emptyLogFilters(): LogFilters {
  return { q: "", route: "", status: "", errorClass: "", model: "", accountID: "", stream: "", retry: "" };
}

function dashboardViewPath(view: DashboardView): string {
  return view === "overview" ? "/" : `/${view}`;
}

function dashboardViewFromPath(pathname: string): DashboardView {
  const normalized = pathname.replace(/^\/dashboard\/?/, "").replace(/^\//, "");
  const raw = normalized.split("/")[0];
  switch (raw) {
    case "accounts":
    case "scheduler":
    case "availability":
    case "models":
    case "proxy":
    case "requests":
    case "settings":
    case "legacy":
      return raw;
    default:
      return "overview";
  }
}

function logFilterParams(filters: LogFilters, limit: string): URLSearchParams {
  const params = new URLSearchParams({ limit });
  const entries: Array<[string, string]> = [
    ["q", filters.q],
    ["route", filters.route],
    ["status", filters.status],
    ["error_class", filters.errorClass],
    ["model", filters.model],
    ["account_id", filters.accountID],
    ["stream", filters.stream],
    ["retry", filters.retry]
  ];
  for (const [key, value] of entries) {
    const trimmed = value.trim();
    if (trimmed) params.set(key, trimmed);
  }
  return params;
}

function dashboardExportURL(format: string, dashboardPassword: string, filters: LogFilters): string {
  const params = logFilterParams(filters, "500");
  params.set("format", format);
  if (dashboardPassword) params.set("dashboard_password", dashboardPassword);
  return `/dashboard/api/logs/export?${params.toString()}`;
}

function activeCooldown(raw: string | undefined): string {
  if (!raw || raw.startsWith("0001-")) return "-";
  const until = Date.parse(raw);
  if (!Number.isFinite(until) || until <= Date.now()) return "-";
  return `${Math.ceil((until - Date.now()) / 1000)}s`;
}

function toggleLine(raw: string, value: string): string {
  const lines = parseLines(raw);
  const next = lines.includes(value) ? lines.filter((line) => line !== value) : [...lines, value];
  return next.sort().join("\n");
}

function formatPercent(value: number | undefined): string {
  if (typeof value !== "number") return "未知";
  return `${value.toFixed(1)}%`;
}

function formatDateTime(value: string | undefined): string {
  if (!value) return "-";
  const ms = Date.parse(value);
  if (!Number.isFinite(ms)) return value;
  return new Date(ms).toLocaleString("zh-CN", { hour12: false });
}

function formatMoney(value: number | undefined): string {
  if (typeof value !== "number") return "-";
  return value.toFixed(2);
}

function formatCredit(bucket: CreditBucket | undefined): string {
  if (!bucket || (bucket.limit == null && bucket.used == null && bucket.remaining == null)) return "-";
  return `剩 ${formatNumber(bucket.remaining)} / 用 ${formatNumber(bucket.used)} / 总 ${formatNumber(bucket.limit)}`;
}

function formatNumber(value: number | undefined): string {
  if (typeof value !== "number") return "-";
  return Number.isInteger(value) ? String(value) : value.toFixed(2);
}

function formatRecordCount(value: Record<string, unknown> | undefined): string {
  const count = Object.keys(value ?? {}).length;
  return count ? `${count} 个` : "无";
}

function formatMap(map: Record<string, number> | undefined): string {
  if (!map || Object.keys(map).length === 0) return "无";
  return Object.entries(map)
    .map(([key, value]) => `${key}:${value}`)
    .join(", ");
}

function formatDuration(ms: number | undefined): string {
  if (typeof ms !== "number" || ms <= 0) return "-";
  const seconds = Math.ceil(ms / 1000);
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.ceil(seconds / 60);
  if (minutes < 60) return `${minutes}m`;
  const hours = Math.floor(minutes / 60);
  const rest = minutes % 60;
  return rest ? `${hours}h ${rest}m` : `${hours}h`;
}

function bindingStatusText(status: string | undefined): string {
  switch (status) {
    case "active":
      return "可用";
    case "verifying":
      return "验证中";
    case "rotating":
      return "轮换中";
    case "failed":
      return "失败";
    case "expired":
      return "过期";
    case "suspended":
      return "暂停";
    default:
      return status || "未知";
  }
}

function bindingTone(status: string | undefined): "good" | "warn" | "bad" {
  switch (status) {
    case "active":
      return "good";
    case "failed":
    case "expired":
      return "bad";
    default:
      return "warn";
  }
}

function formatLocation(binding: ProxyBinding): string {
  const parts = [binding.country, binding.region, binding.city, binding.isp_org].filter(Boolean);
  return parts.length ? `· ${parts.join(" / ")}` : "";
}

function formatBoolean(value: boolean): string {
  return value ? "开启" : "关闭";
}

function formatTier(value: string | undefined): string {
  switch (value) {
    case "free":
      return "免费";
    case "pro":
      return "专业";
    case "unknown":
    case "":
    case undefined:
      return "未知";
    default:
      return value;
  }
}

function AccountPlanBadge({ account }: { account: DebugAccount }) {
  const label = account.plan_name || formatTier(account.tier);
  if (isFreeAccount(account)) {
    return <span className="pill bad">免费账号 · {label}</span>;
  }
  if (isLowCapabilityAccount(account)) {
    return <span className="pill warn">低能力 · {label}</span>;
  }
  return <span>{label || "-"}</span>;
}

function ModelCapabilityBadge({ account }: { account: DebugAccount }) {
  if (isFreeAccount(account)) {
    return <span className="pill bad">{modelCapabilityText(account)}</span>;
  }
  if (isLowCapabilityAccount(account)) {
    return <span className="pill warn">{modelCapabilityText(account)}</span>;
  }
  if ((account.model_config_count ?? 0) >= 100) {
    return <span className="pill good">{modelCapabilityText(account)}</span>;
  }
  return <span className="pill">未探测</span>;
}

function isFreeAccount(account: DebugAccount): boolean {
  return account.tier === "free" || (account.plan_name || "").toLowerCase().includes("free");
}

function isLowCapabilityAccount(account: DebugAccount): boolean {
  const count = account.model_config_count ?? 0;
  return isFreeAccount(account) || (count > 0 && count < 100);
}

function modelCapabilityText(account: DebugAccount): string {
  const count = account.model_config_count ?? 0;
  if (isFreeAccount(account)) {
    return count > 0 ? `低能力账号 · configs=${count}` : "低能力账号";
  }
  if (count <= 0) {
    return "未探测";
  }
  if (count < 100) {
    return `低能力账号 · configs=${count}`;
  }
  return `完整能力 · configs=${count}`;
}

function formatModelConfigCount(value: number | undefined): string {
  return value && value > 0 ? `${value} 个` : "未探测";
}

function localizeSecretMessage(message: string): string {
  const normalized = message.toLowerCase();
  if (normalized.includes("missing dashboard password")) return "缺少控制台密码；远程访问将关闭";
  if (normalized.includes("default dashboard password")) return "正在使用默认控制台密码；远程访问将关闭";
  if (normalized.includes("missing api key")) return "缺少接口密钥";
  if (normalized.includes("default api key")) return "正在使用默认接口密钥";
  if (normalized.includes("not set")) return "未设置";
  if (normalized.includes("set")) return "已设置";
  if (normalized === "unknown") return "未知";
  return message;
}

function humanizeDashboardError(message: string): string {
  const normalized = message.toLowerCase();
  if (normalized.includes("authorization locked")) return "认证已临时锁定。请稍后再试，或重启服务清除本地锁定状态。";
  if (normalized.includes("invalid dashboard authorization")) return "密码或接口密钥不正确。";
  if (normalized.includes("password must be configured")) return "控制台密码未正确配置，远程访问已关闭。";
  if (normalized.includes("dashboard password or api key required")) return "请输入控制台密码或接口密钥。";
  return message;
}

function runtimePatch(config: RuntimeConfigSnapshot): RuntimeConfigSnapshot {
  const secrets: RuntimeConfigSnapshot["secrets"] = {};
  if ((config.secrets?.api_keys ?? []).length) secrets.api_keys = config.secrets?.api_keys;
  if (config.secrets?.dashboard_password?.trim()) secrets.dashboard_password = config.secrets.dashboard_password.trim();
  if (config.secrets?.redis_password) secrets.redis_password = config.secrets.redis_password;
  return {
    server: {
      max_request_body_bytes: config.server?.max_request_body_bytes ?? 26214400
    },
    direct: config.direct,
    health: {
      enabled: config.health?.enabled ?? true,
      interval_seconds: config.health?.interval_seconds ?? 300,
      timeout_seconds: config.health?.timeout_seconds ?? 20,
      mark_invalid_banned: config.health?.mark_invalid_banned ?? true,
      check_model_configs: config.health?.check_model_configs ?? true,
      ready_require_check: config.health?.ready_require_check ?? false,
      model: config.health?.model || "claude-sonnet-4.6"
    },
    scheduler: {
      redis_enabled: config.scheduler?.redis_enabled ?? false,
      redis_fail_closed: config.scheduler?.redis_fail_closed ?? false,
      max_inflight_per_account: config.scheduler?.max_inflight_per_account ?? 4,
      reservation_ttl_seconds: config.scheduler?.reservation_ttl_seconds ?? 180
    },
    usage: {
      virtual_cache: {
        enabled: config.usage?.virtual_cache?.enabled ?? false,
        mode: config.usage?.virtual_cache?.mode || "conservative",
        default_ttl: config.usage?.virtual_cache?.default_ttl || "5m",
        uncached_input_tokens: config.usage?.virtual_cache?.uncached_input_tokens ?? 64,
        min_input_tokens: config.usage?.virtual_cache?.min_input_tokens ?? 1,
        max_input_tokens: config.usage?.virtual_cache?.max_input_tokens ?? 4096,
        warmup_tokens: config.usage?.virtual_cache?.warmup_tokens ?? 0,
        min_creation_tokens: config.usage?.virtual_cache?.min_creation_tokens ?? 0,
        max_creation_tokens: config.usage?.virtual_cache?.max_creation_tokens ?? 8192,
        creation_jitter_ratio: config.usage?.virtual_cache?.creation_jitter_ratio ?? 0,
        burst_every_turns: config.usage?.virtual_cache?.burst_every_turns ?? 0,
        burst_min_tokens: config.usage?.virtual_cache?.burst_min_tokens ?? 0,
        burst_max_tokens: config.usage?.virtual_cache?.burst_max_tokens ?? 0
      }
    },
    proxy: {
      default: config.proxy?.default ?? "",
      dynamic: config.proxy?.dynamic ?? [],
      rotate_on_error: config.proxy?.rotate_on_error ?? true,
      allow_private: config.proxy?.allow_private ?? false,
      test_url: config.proxy?.test_url || "https://ipinfo.io/json",
      cooldown_seconds: config.proxy?.cooldown_seconds ?? 120,
      account_binding: config.proxy?.account_binding ?? false,
      auto_bind_new_accounts: config.proxy?.auto_bind_new_accounts ?? false,
      renew_before_ms: config.proxy?.renew_before_ms ?? 900000,
      max_bind_retries: config.proxy?.max_bind_retries ?? 3,
      worker_interval_ms: config.proxy?.worker_interval_ms ?? 60000,
      worker_batch_size: config.proxy?.worker_batch_size ?? 20,
      worker_concurrency: config.proxy?.worker_concurrency ?? 3,
      provider: config.proxy?.provider || "novproxy",
      protocol: config.proxy?.protocol || "http",
      host: config.proxy?.host || "us.novproxy.io",
      port: config.proxy?.port ?? 1000,
      username_template: config.proxy?.username_template || "nfgr68136-region-{region}-st-{state}-sid-{sid}-t-{ttl}",
      ...(config.proxy?.password?.trim() ? { password: config.proxy.password } : {}),
      region: config.proxy?.region || "US",
      state: config.proxy?.state || "New Jersey",
      ttl_minutes: config.proxy?.ttl_minutes ?? 120
    },
    log: config.log,
    ...(Object.keys(secrets).length ? { secrets } : {})
  };
}

createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  </React.StrictMode>
);
