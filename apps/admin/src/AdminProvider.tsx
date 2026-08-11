import { useQuery, type UseQueryResult } from "@tanstack/react-query";
import { createContext, type PropsWithChildren, useCallback, useContext, useEffect, useMemo, useState } from "react";
import { AdminAPIError, AdminClient, isAdminPreviewMode } from "./api/client";
import type { AdminPrincipal, AdminScope, Permission } from "./api/types";

type SessionState = "loading" | "ready" | "unauthenticated" | "error";
type SignOutAction = () =>
  Promise<void>;
type ReloadIdentityAction = () =>
  Promise<AdminPrincipal>;
type AdminContextValue = {
  client: AdminClient | null;
  clientFor: (signal: AbortSignal) => AdminClient;
  preview: boolean;
  principal: AdminPrincipal | null;
  scope: AdminScope | null;
  scopes: AdminScope[];
  selectScope: (scope: AdminScope) => void;
  can: (permission: Permission) => boolean;
  signOut: SignOutAction;
  reloadIdentity: ReloadIdentityAction;
  sessionState: SessionState;
  sessionError: unknown;
};

const AdminContext = createContext<AdminContextValue | null>(null);
const SCOPE_KEY = "merchant-admin-authorized-scope";

const previewPrincipal: AdminPrincipal = {
  user_id: "10000000-0000-4000-8000-000000000001",
  session_id: "10000000-0000-4000-8000-000000000002",
  display_name: "preview_operator",
  roles: ["preview"],
  permissions: ["dashboard:read", "payments:read", "unmatched:read", "unmatched:claim", "resolution:request", "resolution:approve", "webhooks:read", "webhooks:replay", "infrastructure:read", "infrastructure:edit", "reconciliation:read", "audit:read", "team:admin", "payment_links:read", "payment_links:write", "webhook_settings:read", "webhook_settings:write", "webhook_settings:rotate", "webhook_settings:disable", "api_clients:read", "api_clients:write", "api_clients:rotate", "api_clients:revoke", "management_audit:read", "matching_policy:read", "matching_policy:write", "matching_policy:approve", "matching_policy:activate", "platform_config:read", "platform_config:write", "platform_config:request", "platform_config:approve", "platform_config:schedule", "platform_config:activate", "platform_config:rollback", "platform_config:emergency", "financial:read", "financial:sweep_create", "financial:sweep_cancel", "financial:sweep_approve", "financial:refund_create", "financial:refund_cancel", "financial:refund_approve", "financial:reconciliation_request", "financial:reconciliation_execute"],
  scopes: [{ tenant_id: "10000000-0000-4000-8000-000000000003" }],
  amr: ["preview"]
};

function scopesOf(principal: AdminPrincipal | null): AdminScope[] {
  if (!principal) return [];
  const unique = new Map<string, AdminScope>();
  for (const entry of principal.scopes) {
    if (!entry.tenant_id) continue;
    const scope = { tenantId: entry.tenant_id, ...(entry.merchant_id ? { merchantId: entry.merchant_id } : {}) };
    unique.set(`${scope.tenantId}:${scope.merchantId ?? ""}`, scope);
  }
  return [...unique.values()];
}

function sameScope(left: AdminScope, right: AdminScope) {
  return left.tenantId === right.tenantId && left.merchantId === right.merchantId;
}

export function AdminProvider({ children, client: providedClient, preview: previewOverride }: PropsWithChildren<{ client?: AdminClient; preview?: boolean }>) {
  const preview = previewOverride ?? isAdminPreviewMode();
  const client = useMemo(() => providedClient ?? (preview ? null : new AdminClient()), [preview, providedClient]);
  const [principal, setPrincipal] = useState<AdminPrincipal | null>(preview ? previewPrincipal : null);
  const [sessionState, setSessionState] = useState<SessionState>(preview ? "ready" : "loading");
  const [sessionError, setSessionError] = useState<unknown>(null);
  const scopes = useMemo(() => scopesOf(principal), [principal]);
  const [scope, setScope] = useState<AdminScope | null>(() => scopesOf(preview ? previewPrincipal : null)[0] ?? null);

  const reloadIdentity=useCallback(async()=>{
    if(!client||preview)throw new Error("Admin identity is unavailable");
    setSessionState("loading");setSessionError(null);
    try{const value=await client.me();setPrincipal(value);setSessionState("ready");return value}
    catch(error){setPrincipal(null);setSessionError(error);setSessionState(error instanceof AdminAPIError&&error.status===401?"unauthenticated":"error");throw error}
  },[client,preview]);

  useEffect(() => {
    if (preview) {
      setPrincipal(previewPrincipal);
      setSessionState("ready");
      setSessionError(null);
      return;
    }
    if (!client) return;
    const controller = new AbortController();
    setSessionState("loading");
    setSessionError(null);
    void client.me(controller.signal).then((value) => {
      if (controller.signal.aborted) return;
      setPrincipal(value);
      setSessionState("ready");
    }).catch((error: unknown) => {
      if (controller.signal.aborted) return;
      setPrincipal(null);
      setSessionError(error);
      setSessionState(error instanceof AdminAPIError && error.status === 401 ? "unauthenticated" : "error");
    });
    return () => controller.abort();
  }, [client, preview]);

  useEffect(() => {
    if (scopes.length === 0) {
      setScope(null);
      return;
    }
    let stored: AdminScope | null = null;
    try {
      const raw = window.localStorage.getItem(SCOPE_KEY);
      if (raw) stored = JSON.parse(raw) as AdminScope;
    } catch {
      window.localStorage.removeItem(SCOPE_KEY);
    }
    setScope((current) => {
      if (current && scopes.some((candidate) => sameScope(current, candidate))) return current;
      if (stored && scopes.some((candidate) => sameScope(stored as AdminScope, candidate))) return stored;
      return scopes[0] ?? null;
    });
  }, [scopes]);

  const selectScope = (next: AdminScope) => {
    if (!scopes.some((candidate) => sameScope(candidate, next))) return;
    setScope(next);
    window.localStorage.setItem(SCOPE_KEY, JSON.stringify(next));
  };
  const can = (permission: Permission) => principal?.permissions.includes(permission) ?? false;
  const clientFor = (signal: AbortSignal) => {
    if (!client) throw new Error("Admin client is unavailable");
    if (providedClient) return providedClient;
    return new AdminClient((input, init) => globalThis.fetch(input, { ...init, signal }), window.location.origin);
  };
  const signOut = async () => {
    if (!client || preview) return;
    await client.logout();
    setPrincipal(null);
    setScope(null);
    setSessionError(null);
    setSessionState("unauthenticated");
    window.localStorage.removeItem(SCOPE_KEY);
  };

  return <AdminContext.Provider value={{ client, clientFor, preview, principal, scope, scopes, selectScope, can, signOut, reloadIdentity, sessionState, sessionError }}>{children}</AdminContext.Provider>;
}

export function useAdmin() {
  const value = useContext(AdminContext);
  if (!value) throw new Error("useAdmin must be used inside AdminProvider");
  return value;
}

type AdminLoader<T> =
  (client: AdminClient, scope: AdminScope) =>
  Promise<T>;

export function useAdminQuery<T>(
  key: string,
  permission: Permission,
  load: AdminLoader<T>
): UseQueryResult<T, Error> {
  const admin = useAdmin();
  return useQuery<T, Error>({
    queryKey: ["admin", key, admin.scope?.tenantId, admin.scope?.merchantId],
    enabled: !admin.preview && admin.sessionState === "ready" && Boolean(admin.scope) && admin.can(permission),
    queryFn: ({ signal }) => load(admin.clientFor(signal), admin.scope as AdminScope)
  });
}

export function isStepUpError(error: unknown) {
  return error instanceof AdminAPIError && error.status === 403 && (error.code === "step_up_required" || error.code === "mfa_required");
}
