/* ═══════════════════════════════════════════════════════════════
   Any2API Desktop Admin — Main Application
   ═══════════════════════════════════════════════════════════════ */

const DEFAULT_BACKEND_URL = "http://127.0.0.1:8099";
// Preserve legacy localStorage keys so existing desktop settings continue to work.
const BACKEND_STORAGE_KEY = "newplatform2api.desktop.backendUrl";
const TOKEN_STORAGE_PREFIX = "newplatform2api.desktop.adminToken";
const THEME_STORAGE_KEY = "newplatform2api.desktop.theme";
const SIDEBAR_STORAGE_KEY = "newplatform2api.desktop.sidebarCollapsed";

// ── Types ──

type AdminFeatures = {
  providers: boolean;
  credentials: boolean;
  providerState: boolean;
  stats: boolean;
  logs: boolean;
  users: boolean;
  configImportExport: boolean;
};

type AdminMeta = {
  backend: { language: string; version: string };
  auth: { mode: string };
  features: Partial<AdminFeatures>;
};

type AdminSession = {
  authenticated: boolean;
  expiresAt?: string;
  user: { id: string; name: string; role: string };
};

type AdminLoginResponse = { ok: boolean; token?: string };
type AdminSettings = { apiKey: string; defaultProvider: string; adminPasswordConfigured: boolean };
type ProviderStatus = { count: number; configured: boolean; active: string };
type AdminStatus = {
  providers: {
    cursor: ProviderStatus;
    kiro: ProviderStatus;
    grok: ProviderStatus;
    orchids: ProviderStatus;
  };
};

type CursorConfig = {
  apiUrl?: string; scriptUrl?: string; cookie?: string; xIsHuman?: string;
  userAgent?: string; referer?: string; webglVendor?: string; webglRenderer?: string;
};

type KiroAccount = {
  id?: string; name?: string; accessToken?: string; machineId?: string;
  preferredEndpoint?: string; active?: boolean;
};

type ImportedKiroAccount = {
  id?: string; name?: string; email?: string; accessToken?: string;
  machineId?: string; machineID?: string; preferredEndpoint?: string;
  credentials?: { accessToken?: string; machineId?: string };
};

type GrokToken = { id?: string; name?: string; cookieToken?: string; active?: boolean };

type ImportedGrokToken = {
  id?: string; name?: string; cookieToken?: string; token?: string; value?: string; active?: boolean;
};

type OrchidsConfig = {
  apiUrl?: string; clerkUrl?: string; clientCookie?: string; clientUat?: string;
  sessionId?: string; projectId?: string; userId?: string; email?: string; agentMode?: string;
};

type APIOptions = { method?: string; body?: string; headers?: Record<string, string> };

const defaultFeatures: AdminFeatures = {
  providers: false, credentials: false, providerState: false,
  stats: false, logs: false, users: false, configImportExport: false,
};

const ADMIN_TABS = ["overview", "providers", "logs", "users", "cursor", "kiro", "grok", "orchids", "settings"] as const;
type AdminTab = typeof ADMIN_TABS[number];

const NAV_SECTIONS: Record<"management" | "config", AdminTab[]> = {
  management: ["overview", "providers", "logs", "users"],
  config: ["cursor", "kiro", "grok", "orchids", "settings"],
};

// ── Utilities ──

function normalizeBaseUrl(input: string): string {
  const trimmed = input.trim().replace(/\/+$/, "").replace(/\/admin$/, "");
  return trimmed || DEFAULT_BACKEND_URL;
}

function tokenStorageKey(baseUrl: string): string {
  return `${TOKEN_STORAGE_PREFIX}:${normalizeBaseUrl(baseUrl)}`;
}

function escapeHtml(value: unknown): string {
  return String(value ?? "").replace(/[&<>"']/g, (c) =>
    ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c] ?? c));
}

function must<T extends HTMLElement>(selector: string): T {
  const el = document.querySelector<T>(selector);
  if (!el) throw new Error(`missing element: ${selector}`);
  return el;
}

// ── Theme System ──

function initTheme(): string {
  const saved = localStorage.getItem(THEME_STORAGE_KEY);
  const theme = saved === "light" ? "light" : "dark";
  document.documentElement.setAttribute("data-theme", theme);
  updateThemeIcons(theme);
  return theme;
}

function toggleTheme(): string {
  const current = document.documentElement.getAttribute("data-theme") || "dark";
  const next = current === "dark" ? "light" : "dark";
  document.documentElement.setAttribute("data-theme", next);
  localStorage.setItem(THEME_STORAGE_KEY, next);
  updateThemeIcons(next);
  return next;
}

function updateThemeIcons(theme: string) {
  const darkIcon = document.getElementById("theme-icon-dark");
  const lightIcon = document.getElementById("theme-icon-light");
  if (darkIcon) darkIcon.classList.toggle("hidden", theme === "light");
  if (lightIcon) lightIcon.classList.toggle("hidden", theme === "dark");
}

// ── Sidebar ──

function initSidebar(): boolean {
  const collapsed = localStorage.getItem(SIDEBAR_STORAGE_KEY) === "true";
  const sidebar = document.getElementById("sidebar");
  if (sidebar && collapsed) sidebar.classList.add("collapsed");
  return collapsed;
}

function toggleSidebar() {
  const sidebar = document.getElementById("sidebar");
  if (!sidebar) return;
  const collapsed = sidebar.classList.toggle("collapsed");
  localStorage.setItem(SIDEBAR_STORAGE_KEY, String(collapsed));
}

function toggleMobileSidebar(open?: boolean) {
  const sidebar = document.getElementById("sidebar");
  const overlay = document.getElementById("sidebar-overlay");
  if (!sidebar || !overlay) return;
  const isOpen = open ?? !sidebar.classList.contains("mobile-open");
  sidebar.classList.toggle("mobile-open", isOpen);
  overlay.classList.toggle("visible", isOpen);
}

// ── Main App ──

window.addEventListener("DOMContentLoaded", () => {
  initTheme();
  initSidebar();

  // DOM refs
  const backendInput = must<HTMLInputElement>("#backend-url");
  const adminUrl = must<HTMLElement>("#admin-url");
  const statusText = must<HTMLElement>("#status-text");
  const loginCard = must<HTMLElement>("#login-card");
  const appShell = must<HTMLElement>("#app-shell");
  const sessionChip = must<HTMLElement>("#session-chip");
  const sessionDot = must<HTMLElement>("#session-dot");
  const authChip = must<HTMLElement>("#auth-chip");
  const loginSubtitle = must<HTMLElement>("#login-subtitle");
  const sidebarSubtitle = must<HTMLElement>("#sidebar-subtitle");
  const statusGrid = must<HTMLElement>("#status-grid");
  const loginPasswordInput = must<HTMLInputElement>("#login-password");
  const apiKeyInput = must<HTMLInputElement>("#api-key");
  const defaultProviderSelect = must<HTMLSelectElement>("#default-provider");
  const adminPasswordInput = must<HTMLInputElement>("#admin-password");
  const cursorAPIURLInput = must<HTMLInputElement>("#cursor-api-url");
  const cursorScriptURLInput = must<HTMLInputElement>("#cursor-script-url");
  const cursorXIsHumanInput = must<HTMLInputElement>("#cursor-x-is-human");
  const cursorUserAgentInput = must<HTMLInputElement>("#cursor-user-agent");
  const cursorRefererInput = must<HTMLInputElement>("#cursor-referer");
  const cursorWebGLVendorInput = must<HTMLInputElement>("#cursor-webgl-vendor");
  const cursorWebGLRendererInput = must<HTMLInputElement>("#cursor-webgl-renderer");
  const cursorCookieInput = must<HTMLTextAreaElement>("#cursor-cookie");
  const kiroList = must<HTMLElement>("#kiro-list");
  const grokList = must<HTMLElement>("#grok-list");
  const kiroImport = must<HTMLTextAreaElement>("#kiro-import");
  const grokImport = must<HTMLTextAreaElement>("#grok-import");
  const orchidsAPIURLInput = must<HTMLInputElement>("#orchids-api-url");
  const orchidsClerkURLInput = must<HTMLInputElement>("#orchids-clerk-url");
  const orchidsAgentModeInput = must<HTMLInputElement>("#orchids-agent-mode");
  const orchidsClientUATInput = must<HTMLInputElement>("#orchids-client-uat");
  const orchidsSessionIDInput = must<HTMLInputElement>("#orchids-session-id");
  const orchidsProjectIDInput = must<HTMLInputElement>("#orchids-project-id");
  const orchidsUserIDInput = must<HTMLInputElement>("#orchids-user-id");
  const orchidsEmailInput = must<HTMLInputElement>("#orchids-email");
  const orchidsClientCookieInput = must<HTMLTextAreaElement>("#orchids-client-cookie");
  const flash = must<HTMLElement>("#flash");
  const providerGrid = must<HTMLElement>("#provider-grid");

  // State
  const state = {
    baseUrl: normalizeBaseUrl(localStorage.getItem(BACKEND_STORAGE_KEY) || DEFAULT_BACKEND_URL),
    currentTab: "overview" as AdminTab,
    meta: null as AdminMeta | null,
    session: null as AdminSession | null,
    sessionToken: "",
    status: null as AdminStatus | null,
    settings: null as AdminSettings | null,
    cursorConfig: {} as CursorConfig,
    kiroAccounts: [] as KiroAccount[],
    grokTokens: [] as GrokToken[],
    orchidsConfig: {} as OrchidsConfig,
  };

  // ── Toast ──
  let flashTimer: ReturnType<typeof setTimeout> | null = null;
  const toast = (text: string, type: "info" | "error" | "success" = "info") => {
    if (flashTimer) clearTimeout(flashTimer);
    if (!text) { flash.className = "flash hidden"; flash.textContent = ""; return; }
    flash.className = `flash ${type === "info" ? "" : type}`.trim();
    flash.textContent = text;
    flashTimer = setTimeout(() => { flash.className = "flash hidden"; flash.textContent = ""; }, 3000);
  };

  const setStatus = (text: string) => {
    adminUrl.textContent = state.baseUrl;
    statusText.textContent = text;
  };

  const getFeatures = (): AdminFeatures => ({ ...defaultFeatures, ...(state.meta?.features || {}) });
  const authModeLabel = (): string => state.meta?.auth?.mode || "unknown";
  const backendLabel = (): string => {
    const lang = state.meta?.backend?.language || "unknown";
    const ver = state.meta?.backend?.version || "dev";
    return `${String(lang).toUpperCase()} / ${ver}`;
  };
  const supportsProviderOverview = (): boolean => {
    const f = getFeatures();
    return !!(f.providers || f.providerState);
  };
  const supportsProviderCredentials = (): boolean => {
    const f = getFeatures();
    return !!(f.providers && f.credentials);
  };
  const supportsSettings = (): boolean => {
    const f = getFeatures();
    return !!(f.providers || f.credentials || f.providerState);
  };
  const enabledFeatureCount = (): number => Object.values(getFeatures()).filter(Boolean).length;
  const availableTabs = (): AdminTab[] => ADMIN_TABS.filter((tab) => isTabEnabled(tab));

  const persistToken = (token: string) => {
    state.sessionToken = token;
    if (token) localStorage.setItem(tokenStorageKey(state.baseUrl), token);
    else localStorage.removeItem(tokenStorageKey(state.baseUrl));
  };

  const setLoggedOutState = () => {
    state.session = null;
    loginCard.classList.remove("hidden");
    appShell.classList.add("hidden");
    sessionChip.textContent = "未登录";
    sessionDot.classList.remove("online");
    authChip.textContent = authModeLabel();
    setStatus(`已连接 ${state.baseUrl}，等待登录`);
  };

  const setAuthenticatedState = (session: AdminSession | null) => {
    state.session = session;
    loginCard.classList.add("hidden");
    appShell.classList.remove("hidden");
    sessionChip.textContent = session?.user?.name || "已登录";
    sessionDot.classList.add("online");
    authChip.textContent = session?.user?.role || authModeLabel();
    setStatus(`${backendLabel()} · 已连接`);
  };

  // ── Tab / Feature Gating ──

  const isTabEnabled = (tab: string): boolean => {
    const f = getFeatures();
    if (tab === "overview") return true;
    if (tab === "providers") return supportsProviderOverview();
    if (tab === "logs") return !!f.logs;
    if (tab === "users") return !!f.users;
    if (tab === "cursor" || tab === "kiro" || tab === "grok" || tab === "orchids") return supportsProviderCredentials();
    if (tab === "settings") return supportsSettings();
    return false;
  };

  const updateNavSections = () => {
    (Object.entries(NAV_SECTIONS) as Array<[keyof typeof NAV_SECTIONS, AdminTab[]]>).forEach(([section, tabs]) => {
      const el = document.querySelector<HTMLElement>(`[data-nav-section="${section}"]`);
      if (!el) return;
      el.classList.toggle("hidden", !tabs.some((tab) => isTabEnabled(tab)));
    });
  };

  const switchTab = (tab: string) => {
    const enabledTabs = availableTabs();
    if (!ADMIN_TABS.includes(tab as AdminTab) || !enabledTabs.includes(tab as AdminTab)) {
      tab = enabledTabs[0] || "overview";
    }
    state.currentTab = tab as AdminTab;
    updateNavSections();

    document.querySelectorAll<HTMLElement>("[data-tab]").forEach((el) => {
      const targetTab = (el.dataset.tab || "") as AdminTab;
      const enabled = isTabEnabled(targetTab);
      el.classList.toggle("hidden", !enabled);
      el.classList.toggle("disabled", !enabled);
      el.classList.toggle("active", enabled && targetTab === tab);
    });

    document.querySelectorAll<HTMLElement>("[data-panel]").forEach((el) => {
      const panel = el.dataset.panel || "";
      el.classList.toggle("hidden", panel !== tab || !isTabEnabled(panel));
    });

    // Close mobile sidebar on tab switch
    toggleMobileSidebar(false);
  };

  const applyMeta = (meta: AdminMeta) => {
    state.meta = meta;
    loginSubtitle.textContent = `当前连接 ${backendLabel()}，认证模式 ${authModeLabel()}`;
    sidebarSubtitle.textContent = `${String(meta.backend.language || "unknown").toUpperCase()} · ${meta.backend.version || "dev"}`;
    authChip.textContent = authModeLabel();

    // Update feature-gated content
    const f = getFeatures();
    const chartCard = document.getElementById("overview-chart-card");
    if (chartCard) chartCard.classList.toggle("hidden", !f.stats);

    switchTab(state.currentTab || "overview");
  };

  // ── API Client ──

  async function api<T>(path: string, opts: APIOptions = {}): Promise<T> {
    const headers: Record<string, string> = { "Content-Type": "application/json", ...(opts.headers || {}) };
    if (state.sessionToken) headers.Authorization = `Bearer ${state.sessionToken}`;
    let res: Response;
    try {
      res = await fetch(`${state.baseUrl}${path}`, {
        method: opts.method || "GET", headers, body: opts.body, mode: "cors",
      });
    } catch (e) {
      throw new Error(`无法连接后台：${e instanceof Error ? e.message : "network error"}`);
    }
    const body = await res.json().catch(() => ({}));
    if (res.status === 401) throw new Error("UNAUTHORIZED");
    if (!res.ok) throw new Error(typeof body?.error === "string" ? body.error : "请求失败");
    return body as T;
  }

  async function optionalApi<T>(enabled: boolean, path: string, opts: APIOptions = {}): Promise<T | null> {
    if (!enabled) return null;
    try {
      return await api<T>(path, opts);
    } catch (e) {
      const message = e instanceof Error ? e.message : "请求失败";
      if (message === "not found" || message === "method not allowed") return null;
      throw e;
    }
  }

  // ── Helpers ──

  const downloadJSON = (filename: string, payload: unknown) => {
    const blob = new Blob([JSON.stringify(payload, null, 2)], { type: "application/json;charset=utf-8" });
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url; a.download = filename;
    document.body.appendChild(a); a.click(); a.remove();
    setTimeout(() => URL.revokeObjectURL(url), 0);
  };

  const ensureSingleActive = <T extends { active?: boolean }>(items: T[]) => {
    let found = false;
    items.forEach((item) => { if (item.active && !found) found = true; else item.active = false; });
    if (!found && items.length > 0) items[0].active = true;
  };

  const resetAdminData = () => {
    state.status = null;
    state.settings = null;
    state.cursorConfig = {};
    state.kiroAccounts = [];
    state.grokTokens = [];
    state.orchidsConfig = {};
    apiKeyInput.value = "";
    defaultProviderSelect.value = "cursor";
    adminPasswordInput.value = "";
    renderCursorConfig();
    renderKiroList();
    renderGrokList();
    renderOrchidsConfig();
    renderStatus(null, null);
  };

  // ── Render Functions ──

  const renderCursorConfig = () => {
    cursorAPIURLInput.value = state.cursorConfig.apiUrl || "";
    cursorScriptURLInput.value = state.cursorConfig.scriptUrl || "";
    cursorXIsHumanInput.value = state.cursorConfig.xIsHuman || "";
    cursorUserAgentInput.value = state.cursorConfig.userAgent || "";
    cursorRefererInput.value = state.cursorConfig.referer || "";
    cursorWebGLVendorInput.value = state.cursorConfig.webglVendor || "";
    cursorWebGLRendererInput.value = state.cursorConfig.webglRenderer || "";
    cursorCookieInput.value = state.cursorConfig.cookie || "";
  };

  const renderOrchidsConfig = () => {
    orchidsAPIURLInput.value = state.orchidsConfig.apiUrl || "";
    orchidsClerkURLInput.value = state.orchidsConfig.clerkUrl || "";
    orchidsAgentModeInput.value = state.orchidsConfig.agentMode || "";
    orchidsClientUATInput.value = state.orchidsConfig.clientUat || "";
    orchidsSessionIDInput.value = state.orchidsConfig.sessionId || "";
    orchidsProjectIDInput.value = state.orchidsConfig.projectId || "";
    orchidsUserIDInput.value = state.orchidsConfig.userId || "";
    orchidsEmailInput.value = state.orchidsConfig.email || "";
    orchidsClientCookieInput.value = state.orchidsConfig.clientCookie || "";
  };

  const readCursorConfig = (): CursorConfig => ({
    apiUrl: cursorAPIURLInput.value, scriptUrl: cursorScriptURLInput.value,
    xIsHuman: cursorXIsHumanInput.value, userAgent: cursorUserAgentInput.value,
    referer: cursorRefererInput.value, webglVendor: cursorWebGLVendorInput.value,
    webglRenderer: cursorWebGLRendererInput.value, cookie: cursorCookieInput.value,
  });

  const readOrchidsConfig = (): OrchidsConfig => ({
    apiUrl: orchidsAPIURLInput.value, clerkUrl: orchidsClerkURLInput.value,
    agentMode: orchidsAgentModeInput.value, clientUat: orchidsClientUATInput.value,
    sessionId: orchidsSessionIDInput.value, projectId: orchidsProjectIDInput.value,
    userId: orchidsUserIDInput.value, email: orchidsEmailInput.value,
    clientCookie: orchidsClientCookieInput.value,
  });

  const renderStatus = (status: AdminStatus | null, settings: AdminSettings | null) => {
    const providers: Array<[string, string, ProviderStatus]> = status ? [
      ["Cursor", "C", status.providers.cursor],
      ["Kiro", "K", status.providers.kiro],
      ["Grok", "G", status.providers.grok],
      ["Orchids", "O", status.providers.orchids],
    ] : [];
    const configuredCount = providers.filter(([, , p]) => p.configured).length;
    const currentUser = state.session?.user?.name || "Admin";
    const providerSummary = supportsProviderOverview()
      ? `${configuredCount}/${providers.length || 4}`
      : "--";
    const providerMeta = settings?.defaultProvider
      ? `默认 ${escapeHtml(settings.defaultProvider)}`
      : supportsProviderOverview()
        ? "等待 Provider 数据"
        : "当前后端未启用 Provider 管理";

    statusGrid.innerHTML = `
      <div class="stat-card">
        <div class="stat-label">后端</div>
        <div class="stat-value" style="font-size:18px">${escapeHtml(backendLabel())}</div>
        <div class="stat-meta">认证 ${escapeHtml(authModeLabel())}</div>
      </div>
      <div class="stat-card">
        <div class="stat-label">会话</div>
        <div class="stat-value" style="font-size:18px">${escapeHtml(currentUser)}</div>
        <div class="stat-meta">${escapeHtml(state.session?.user?.role || authModeLabel())}</div>
      </div>
      <div class="stat-card">
        <div class="stat-label">已启用能力</div>
        <div class="stat-value">${enabledFeatureCount()}</div>
        <div class="stat-meta">shared admin contract</div>
      </div>
      <div class="stat-card">
        <div class="stat-label">Provider 管理</div>
        <div class="stat-value">${providerSummary}</div>
        <div class="stat-meta">${providerMeta}</div>
      </div>
      ${settings ? `
      <div class="stat-card">
        <div class="stat-label">API Key</div>
        <div class="stat-value" style="font-size:18px">${settings.apiKey ? "已启用" : "关闭"}</div>
        <div class="stat-meta">${settings.apiKey ? "Bearer 校验中" : "允许无鉴权"}</div>
      </div>` : `
      <div class="stat-card">
        <div class="stat-label">系统设置</div>
        <div class="stat-value" style="font-size:18px">未暴露</div>
        <div class="stat-meta">当前后端仅支持基础后台协议</div>
      </div>`}
      ${providers.map(([name, , p]) => `
        <div class="stat-card">
          <div class="stat-label">${escapeHtml(name)}</div>
          <div class="stat-value">${p.count}</div>
          <div class="stat-meta">
            <span class="status-dot ${p.configured ? "healthy" : "unknown"}"></span>
            ${p.configured ? `已配置 · ${escapeHtml(p.active || "激活")}` : "未配置"}
          </div>
        </div>`).join("")}`;

    providerGrid.innerHTML = supportsProviderOverview() && providers.length
      ? providers.map(([name, initial, p]) => `
      <div class="provider-card">
        <div class="provider-card-header">
          <div class="provider-card-info">
            <div class="provider-icon">${escapeHtml(initial)}</div>
            <div>
              <div class="provider-name">${escapeHtml(name)}</div>
              <div class="provider-meta">${p.count} 项配置</div>
            </div>
          </div>
          <span class="status-dot ${p.configured ? "healthy" : "unknown"}"></span>
        </div>
        <div style="display:flex;gap:6px;flex-wrap:wrap;margin-top:8px">
          <span class="tag ${p.configured ? "success" : ""}">${p.configured ? "已配置" : "未配置"}</span>
          ${p.active ? `<span class="tag active">${escapeHtml(p.active)}</span>` : ""}
        </div>
      </div>`).join("")
      : '<div class="empty-state">当前后端未启用 Provider 管理能力，桌面端已自动跳过 Go 专属管理接口。</div>';
  };

  // ── Kiro / Grok List Rendering ──

  const maskToken = (token: string | undefined): string => {
    if (!token) return "未设置";
    if (token.length <= 16) return token.slice(0, 4) + "****";
    return token.slice(0, 8) + "…" + token.slice(-8);
  };

  const renderKiroList = () => {
    if (!state.kiroAccounts.length) {
      kiroList.innerHTML = '<div class="empty-state">还没有 Kiro 账号，先新增一条或从 JSON 导入。</div>';
      return;
    }
    kiroList.innerHTML = state.kiroAccounts.map((item, i) => `
      <div class="item-card" id="kiro-item-${i}">
        <div class="item-card-header">
          <div>
            <div class="item-card-title">${escapeHtml(item.name || `Kiro 账号 ${i + 1}`)}</div>
            <div class="item-card-subtitle">Machine ID: ${escapeHtml(item.machineId || "未设置")}</div>
            <div class="item-card-tags">
              <span class="tag ${item.active ? "active" : ""}">${item.active ? "当前激活" : "未启用"}</span>
              <span class="tag">${escapeHtml(item.preferredEndpoint || "auto")}</span>
            </div>
          </div>
          <div class="item-card-actions">
            <button class="btn btn-sm btn-ghost" data-kind="kiro-toggle" data-index="${i}" type="button">详情</button>
            <button class="btn btn-sm" data-kind="kiro-active" data-index="${i}" type="button">${item.active ? "已激活" : "设为激活"}</button>
            <button class="btn btn-sm btn-danger" data-kind="kiro-remove" data-index="${i}" type="button">删除</button>
          </div>
        </div>
        <div class="item-card-body collapsed">
          <div class="form-grid">
            <div><label>名称</label><input data-kind="kiro" data-index="${i}" data-field="name" value="${escapeHtml(item.name || "")}" /></div>
            <div><label>Machine ID</label><input data-kind="kiro" data-index="${i}" data-field="machineId" value="${escapeHtml(item.machineId || "")}" /></div>
            <div><label>Endpoint</label><select data-kind="kiro" data-index="${i}" data-field="preferredEndpoint"><option value="">auto</option><option value="amazonq" ${item.preferredEndpoint === "amazonq" ? "selected" : ""}>amazonq</option><option value="codewhisperer" ${item.preferredEndpoint === "codewhisperer" ? "selected" : ""}>codewhisperer</option></select></div>
            <div class="full-width"><label>Access Token</label><textarea data-kind="kiro" data-index="${i}" data-field="accessToken">${escapeHtml(item.accessToken || "")}</textarea></div>
          </div>
        </div>
      </div>`).join("");
  };

  const renderGrokList = () => {
    if (!state.grokTokens.length) {
      grokList.innerHTML = '<div class="empty-state">还没有 Grok Token，先新增一条。</div>';
      return;
    }
    grokList.innerHTML = state.grokTokens.map((item, i) => `
      <div class="item-card" id="grok-item-${i}">
        <div class="item-card-header">
          <div>
            <div class="item-card-title">${escapeHtml(item.name || `Grok Token ${i + 1}`)}</div>
            <div class="item-card-subtitle">Token: ${escapeHtml(maskToken(item.cookieToken))}</div>
            <div class="item-card-tags"><span class="tag ${item.active ? "active" : ""}">${item.active ? "当前激活" : "未启用"}</span></div>
          </div>
          <div class="item-card-actions">
            <button class="btn btn-sm btn-ghost" data-kind="grok-toggle" data-index="${i}" type="button">详情</button>
            <button class="btn btn-sm" data-kind="grok-active" data-index="${i}" type="button">${item.active ? "已激活" : "设为激活"}</button>
            <button class="btn btn-sm btn-danger" data-kind="grok-remove" data-index="${i}" type="button">删除</button>
          </div>
        </div>
        <div class="item-card-body collapsed">
          <div class="form-grid">
            <div><label>名称</label><input data-kind="grok" data-index="${i}" data-field="name" value="${escapeHtml(item.name || "")}" /></div>
            <div class="full-width"><label>Cookie Token</label><textarea data-kind="grok" data-index="${i}" data-field="cookieToken">${escapeHtml(item.cookieToken || "")}</textarea></div>
          </div>
        </div>
      </div>`).join("");
  };

  const wireLists = () => {
    document.querySelectorAll<HTMLInputElement | HTMLTextAreaElement | HTMLSelectElement>("[data-kind='kiro'], [data-kind='grok']").forEach((el) => {
      el.oninput = (e) => {
        const t = e.target as HTMLInputElement;
        const { kind, index, field } = t.dataset;
        const list = kind === "kiro" ? state.kiroAccounts : state.grokTokens;
        if (index && field) (list[Number(index)] as Record<string, unknown>)[field] = t.value;
      };
    });

    document.querySelectorAll<HTMLElement>("[data-kind='kiro-toggle']").forEach((el) => {
      el.onclick = () => {
        const card = document.getElementById(`kiro-item-${el.dataset.index}`);
        if (!card) return;
        const body = card.querySelector(".item-card-body");
        if (!body) return;
        body.classList.toggle("collapsed");
        el.textContent = body.classList.contains("collapsed") ? "详情" : "收起";
      };
    });

    document.querySelectorAll<HTMLElement>("[data-kind='grok-toggle']").forEach((el) => {
      el.onclick = () => {
        const card = document.getElementById(`grok-item-${el.dataset.index}`);
        if (!card) return;
        const body = card.querySelector(".item-card-body");
        if (!body) return;
        body.classList.toggle("collapsed");
        el.textContent = body.classList.contains("collapsed") ? "详情" : "收起";
      };
    });

    document.querySelectorAll<HTMLElement>("[data-kind='kiro-active']").forEach((el) => {
      el.onclick = () => {
        state.kiroAccounts.forEach((item, j) => { item.active = j === Number(el.dataset.index); });
        ensureSingleActive(state.kiroAccounts); renderKiroList(); wireLists();
      };
    });
    document.querySelectorAll<HTMLElement>("[data-kind='grok-active']").forEach((el) => {
      el.onclick = () => {
        state.grokTokens.forEach((item, j) => { item.active = j === Number(el.dataset.index); });
        ensureSingleActive(state.grokTokens); renderGrokList(); wireLists();
      };
    });
    document.querySelectorAll<HTMLElement>("[data-kind='kiro-remove']").forEach((el) => {
      el.onclick = () => {
        state.kiroAccounts.splice(Number(el.dataset.index), 1);
        ensureSingleActive(state.kiroAccounts); renderKiroList(); wireLists();
      };
    });
    document.querySelectorAll<HTMLElement>("[data-kind='grok-remove']").forEach((el) => {
      el.onclick = () => {
        state.grokTokens.splice(Number(el.dataset.index), 1);
        ensureSingleActive(state.grokTokens); renderGrokList(); wireLists();
      };
    });
  };

  const addKiroAccount = (item: KiroAccount = {}) => {
    state.kiroAccounts.push({
      id: item.id || "", name: item.name || "", accessToken: item.accessToken || "",
      machineId: item.machineId || "", preferredEndpoint: item.preferredEndpoint || "",
      active: state.kiroAccounts.length === 0 || !!item.active,
    });
    ensureSingleActive(state.kiroAccounts); renderKiroList(); wireLists();
  };

  const addGrokToken = (item: GrokToken = {}) => {
    state.grokTokens.push({
      id: item.id || "", name: item.name || "", cookieToken: item.cookieToken || "",
      active: state.grokTokens.length === 0 || !!item.active,
    });
    ensureSingleActive(state.grokTokens); renderGrokList(); wireLists();
  };

  // ── Data Loading ──

  const loadAdmin = async () => {
    const [status, settings, cursor, kiro, grok, orchids] = await Promise.all([
      optionalApi<AdminStatus>(supportsProviderOverview(), "/admin/api/status"),
      optionalApi<AdminSettings>(supportsSettings(), "/admin/api/settings"),
      optionalApi<{ config: CursorConfig }>(supportsProviderCredentials(), "/admin/api/providers/cursor/config"),
      optionalApi<{ accounts: KiroAccount[] }>(supportsProviderCredentials(), "/admin/api/providers/kiro/accounts"),
      optionalApi<{ tokens: GrokToken[] }>(supportsProviderCredentials(), "/admin/api/providers/grok/tokens"),
      optionalApi<{ config: OrchidsConfig }>(supportsProviderCredentials(), "/admin/api/providers/orchids/config"),
    ]);

    state.status = status;
    state.settings = settings;
    state.cursorConfig = cursor?.config || {};
    state.kiroAccounts = kiro?.accounts || [];
    state.grokTokens = grok?.tokens || [];
    state.orchidsConfig = orchids?.config || {};
    renderStatus(status, settings);
    apiKeyInput.value = settings?.apiKey || "";
    defaultProviderSelect.value = settings?.defaultProvider || "cursor";
    ensureSingleActive(state.kiroAccounts);
    ensureSingleActive(state.grokTokens);
    renderCursorConfig(); renderKiroList(); renderGrokList(); renderOrchidsConfig(); wireLists();
    switchTab(state.currentTab || "overview");
    setAuthenticatedState(state.session);
    toast(supportsSettings() || supportsProviderOverview() ? "管理台已就绪" : "已连接基础后台协议", "success");
  };

  const bootstrapAdmin = async () => {
    toast("正在连接后台...");
    backendInput.value = state.baseUrl;
    setStatus(`正在连接 ${state.baseUrl} ...`);
    resetAdminData();
    try {
      const meta = await api<AdminMeta>("/api/admin/meta");
      applyMeta(meta);
      if (!state.sessionToken) persistToken(localStorage.getItem(tokenStorageKey(state.baseUrl)) || "");
      if (state.sessionToken) {
        try {
          state.session = await api<AdminSession>("/api/admin/auth/session");
          await loadAdmin();
          return;
        } catch (e) {
          if ((e as Error).message !== "UNAUTHORIZED") throw e;
          persistToken("");
        }
      }
      setLoggedOutState();
      toast("");
    } catch (e) {
      persistToken("");
      resetAdminData();
      setLoggedOutState();
      toast((e as Error).message, "error");
      setStatus(`连接失败：${(e as Error).message}`);
    }
  };

  const connectToBackend = async (baseUrl: string) => {
    state.baseUrl = normalizeBaseUrl(baseUrl);
    localStorage.setItem(BACKEND_STORAGE_KEY, state.baseUrl);
    persistToken(localStorage.getItem(tokenStorageKey(state.baseUrl)) || "");
    await bootstrapAdmin();
  };

  // ── Event Bindings ──

  // Theme toggle
  must<HTMLButtonElement>("#theme-toggle").onclick = () => toggleTheme();

  // Hamburger menu
  must<HTMLButtonElement>("#hamburger-btn").onclick = () => toggleMobileSidebar();
  must<HTMLElement>("#sidebar-overlay").onclick = () => toggleMobileSidebar(false);

  // Sidebar collapse
  must<HTMLButtonElement>("#sidebar-collapse").onclick = () => toggleSidebar();

  // Tab navigation
  document.querySelectorAll<HTMLElement>("[data-tab]").forEach((el) => {
    el.onclick = () => switchTab(el.dataset.tab || "overview");
  });

  // Backend connection
  must<HTMLButtonElement>("#connect-btn").onclick = async () => { await connectToBackend(backendInput.value); };
  must<HTMLButtonElement>("#reset-btn").onclick = async () => { await connectToBackend(DEFAULT_BACKEND_URL); toast("已恢复默认后台地址", "success"); };
  backendInput.addEventListener("keydown", async (e) => { if (e.key === "Enter") await connectToBackend(backendInput.value); });

  // Login
  must<HTMLButtonElement>("#login-btn").onclick = async () => {
    try {
      const res = await api<AdminLoginResponse>("/api/admin/auth/login", {
        method: "POST", body: JSON.stringify({ password: loginPasswordInput.value }),
      });
      if (!res.token) throw new Error("登录成功但未收到会话令牌");
      persistToken(res.token);
      state.session = await api<AdminSession>("/api/admin/auth/session");
      await loadAdmin();
    } catch (e) {
      toast((e as Error).message === "UNAUTHORIZED" ? "登录失败，请检查管理密码" : (e as Error).message, "error");
    }
  };
  loginPasswordInput.addEventListener("keydown", async (e) => {
    if (e.key === "Enter") must<HTMLButtonElement>("#login-btn").click();
  });

  // Logout
  must<HTMLButtonElement>("#logout-btn").onclick = async () => {
    try { await api<{ ok: boolean }>("/api/admin/auth/logout", { method: "POST" }); }
    catch (e) { if ((e as Error).message !== "UNAUTHORIZED") { toast((e as Error).message, "error"); return; } }
    persistToken(""); setLoggedOutState(); toast("已退出登录", "success");
  };

  // Settings save
  must<HTMLButtonElement>("#save-settings-btn").onclick = async () => {
    try {
      await api<AdminSettings>("/admin/api/settings", {
        method: "PUT",
        body: JSON.stringify({ apiKey: apiKeyInput.value, defaultProvider: defaultProviderSelect.value, adminPassword: adminPasswordInput.value }),
      });
      adminPasswordInput.value = "";
      await loadAdmin();
      toast("系统设置已保存", "success");
    } catch (e) { toast((e as Error).message, "error"); }
  };

  // Cursor save
  must<HTMLButtonElement>("#cursor-save-btn").onclick = async () => {
    try {
      await api<{ config: CursorConfig }>("/admin/api/providers/cursor/config", {
        method: "PUT", body: JSON.stringify({ config: readCursorConfig() }),
      });
      await loadAdmin();
      toast("Cursor 配置已保存", "success");
    } catch (e) { toast((e as Error).message, "error"); }
  };

  // Kiro
  must<HTMLButtonElement>("#kiro-add-btn").onclick = () => addKiroAccount();
  must<HTMLButtonElement>("#kiro-export-btn").onclick = () => { downloadJSON("kiro-accounts.json", { accounts: state.kiroAccounts }); toast("Kiro 账号已导出", "success"); };
  must<HTMLButtonElement>("#kiro-import-btn").onclick = () => {
    try {
      const parsed = JSON.parse(kiroImport.value || "{}") as ImportedKiroAccount & { accounts?: ImportedKiroAccount[] };
      const accounts: ImportedKiroAccount[] = Array.isArray(parsed.accounts) ? parsed.accounts : [parsed];
      accounts.forEach((a) => addKiroAccount({
        name: a.email || a.name || a.id || "Imported Kiro",
        accessToken: a.accessToken || a.credentials?.accessToken || "",
        machineId: a.machineId || a.credentials?.machineId || a.machineID || "",
        preferredEndpoint: a.preferredEndpoint || "",
      }));
      kiroImport.value = "";
      toast("Kiro JSON 已导入", "success");
    } catch (e) { toast(`Kiro JSON 解析失败: ${(e as Error).message}`, "error"); }
  };
  must<HTMLButtonElement>("#kiro-save-btn").onclick = async () => {
    try {
      ensureSingleActive(state.kiroAccounts);
      await api<{ accounts: KiroAccount[] }>("/admin/api/providers/kiro/accounts", {
        method: "PUT", body: JSON.stringify({ accounts: state.kiroAccounts }),
      });
      await loadAdmin();
      toast("Kiro 账号已保存", "success");
    } catch (e) { toast((e as Error).message, "error"); }
  };

  // Grok
  must<HTMLButtonElement>("#grok-add-btn").onclick = () => addGrokToken();
  must<HTMLButtonElement>("#grok-export-btn").onclick = () => { downloadJSON("grok-tokens.json", { tokens: state.grokTokens }); toast("Grok Token 已导出", "success"); };
  must<HTMLButtonElement>("#grok-import-btn").onclick = () => {
    try {
      const parsed = JSON.parse(grokImport.value || "{}") as ImportedGrokToken & { tokens?: ImportedGrokToken[] };
      const tokens: ImportedGrokToken[] = Array.isArray(parsed.tokens) ? parsed.tokens : [parsed];
      tokens.forEach((t) => addGrokToken({
        name: t.name || t.id || "Imported Grok",
        cookieToken: t.cookieToken || t.token || t.value || "",
        active: !!t.active,
      }));
      grokImport.value = "";
      toast("Grok JSON 已导入", "success");
    } catch (e) { toast(`Grok JSON 解析失败: ${(e as Error).message}`, "error"); }
  };
  must<HTMLButtonElement>("#grok-save-btn").onclick = async () => {
    try {
      ensureSingleActive(state.grokTokens);
      await api<{ tokens: GrokToken[] }>("/admin/api/providers/grok/tokens", {
        method: "PUT", body: JSON.stringify({ tokens: state.grokTokens }),
      });
      await loadAdmin();
      toast("Grok Token 已保存", "success");
    } catch (e) { toast((e as Error).message, "error"); }
  };

  // Orchids save
  must<HTMLButtonElement>("#orchids-save-btn").onclick = async () => {
    try {
      await api<{ config: OrchidsConfig }>("/admin/api/providers/orchids/config", {
        method: "PUT", body: JSON.stringify({ config: readOrchidsConfig() }),
      });
      await loadAdmin();
      toast("Orchids 配置已保存", "success");
    } catch (e) { toast((e as Error).message, "error"); }
  };

  // ── Bootstrap ──
  backendInput.value = state.baseUrl;
  setStatus(`准备连接 ${state.baseUrl}`);
  void bootstrapAdmin();
});
