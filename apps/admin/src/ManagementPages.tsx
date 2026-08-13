import { useI18n } from "@merchant/i18n";
import { Badge, Button, Input, PageHeader, SectionCard, StatusBadge } from "@merchant/ui";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { FileClock, KeyRound, Link2, RefreshCw, Webhook } from "lucide-react";
import { type FormEvent, type ReactNode, useEffect, useMemo, useState } from "react";
import { isStepUpError, useAdmin, useAdminQuery } from "./AdminProvider";
import type { APIClientInput, APIClientRecord, APIClientSecret, ManagementActionCategory, ManagementActionRequest, ManagementAuditEvent, ManagementPage, PaymentLink, PaymentLinkInput, Permission, WebhookDelivery, WebhookEndpoint, WebhookEndpointInput, WebhookEndpointSecret } from "./api/types";

type Notice = "success" | "failure" | "stepup" | null;
type AsyncAction =
  () =>
  Promise<unknown>;
type ClientSecretAction =
  () =>
  Promise<APIClientSecret | unknown>;

const resourceStatusKey = {
  active: "management.resourceActive",
  disabled: "management.resourceDisabled",
  expired: "management.resourceExpired",
  revoked: "management.resourceRevoked",
  unverified: "management.resourceUnverified"
} as const;

const deliveryStatusKey = {
  pending: "management.deliveryPending",
  leased: "management.deliveryLeased",
  retry: "management.deliveryRetry",
  acknowledged: "management.deliveryAcknowledged",
  dead_letter: "management.deliveryDeadLetter"
} as const;

function idempotencyKey() {
  return `admin-${crypto.randomUUID()}`;
}

function date(value: string | undefined, locale: string) {
  if (!value) return "—";
  const instant = Date.parse(value);
  return Number.isFinite(instant) ? new Intl.DateTimeFormat(locale, { dateStyle: "medium", timeStyle: "short" }).format(instant) : value;
}

function exactMinor(value: string, scale: number) {
  if (scale === 0) return value;
  const padded = value.padStart(scale + 1, "0");
  return `${padded.slice(0, -scale)}.${padded.slice(-scale)}`;
}

function copyText(value: string) {
  return navigator.clipboard?.writeText(value) ?? Promise.reject(new Error("clipboard_unavailable"));
}

function State({ busy, error, empty, permission }: { busy:boolean;error:unknown;empty:boolean;permission:boolean }) {
  const { t } = useI18n();
  if (!permission) return <div className="admin-live-state" role="status"><strong>{t("admin.permissionTitle")}</strong><p>{t("admin.permissionBody")}</p></div>;
  if (busy) return <div aria-busy="true" className="admin-live-state" role="status"><strong>{t("admin.dataLoading")}</strong></div>;
  if (error) return <div className="admin-live-state" role="alert"><strong>{t("admin.dataError")}</strong><p>{t("admin.dataErrorBody")}</p></div>;
  if (empty) return <div className="admin-live-state" role="status"><strong>{t("admin.emptyTitle")}</strong><p>{t("admin.emptyBody")}</p></div>;
  return null;
}

function MutationNotice({ notice }: { notice:Notice }) {
  const { t } = useI18n();
  const admin = useAdmin();
  if (!notice) return null;
  return <div aria-live="polite" className={`admin-live-notice is-${notice}`} role={notice === "failure" ? "alert" : "status"}>
    {t(notice === "success" ? "admin.mutationSucceeded" : notice === "stepup" ? "admin.stepUpBody" : "admin.mutationFailed")}
    {notice === "stepup" && <a href={admin.client?.stepUpURL(`${window.location.pathname}${window.location.hash}`)}>{t("admin.stepUp")}</a>}
  </div>;
}

function SecretPanel({ kind, secret, keyId, publicURL, onClear }: { kind:"credential"|"link";secret?:string;keyId?:string;publicURL?:string;onClear:()=>void }) {
  const { t } = useI18n();
  const value = publicURL ?? secret ?? "";
  const [copied, setCopied] = useState(false);
  return <section aria-labelledby="one-time-value-title" className="admin-secret-panel" data-testid="one-time-value">
    <div><Badge tone="warning">{t("management.oneTime")}</Badge><h2 id="one-time-value-title">{t(kind === "link" ? "management.savePublicURL" : "management.saveSecret")}</h2></div>
    <p>{t("management.oneTimeBody")}</p>
    {keyId && <dl><div><dt>{t("management.keyId")}</dt><dd><code>{keyId}</code></dd></div></dl>}
    <output aria-label={t(kind === "link" ? "management.publicURL" : "management.secret")} tabIndex={0}><code>{value}</code></output>
    <div className="admin-live-actions"><Button onClick={() => void copyText(value).then(() => setCopied(true)).catch(() => setCopied(false))} variant="secondary">{t(copied ? "management.copied" : "common.copy")}</Button><Button onClick={onClear}>{t("management.saved")}</Button></div>
  </section>;
}

function Cards({ children }: { children:ReactNode }) {
  return <div className="admin-management-grid">{children}</div>;
}

function ReasonField({ value, onChange }: { value:string;onChange:(value:string)=>void }) {
  const { t } = useI18n();
  return <label className="admin-live-label"><span>{t("admin.reason")}</span><textarea onChange={(event) => onChange(event.target.value)} rows={3} value={value} /></label>;
}

export function PaymentLinksManagementPage() {
  const { locale, t } = useI18n();
  const admin = useAdmin();
  const queryClient = useQueryClient();
  const query = useAdminQuery<ManagementPage<PaymentLink>>("management-payment-links", "payment_links:read", (client, scope) => client.paymentLinks(scope));
  const [created, setCreated] = useState<PaymentLink | null>(null);
  const [notice, setNotice] = useState<Notice>(null);
  const [busy, setBusy] = useState(false);
  const [routeProvider, setRouteProvider] = useState<"on_chain"|"hosted_gateway">("on_chain");
  useEffect(() => { setCreated(null); return () => setCreated(null); }, [admin.scope?.merchantId, admin.scope?.tenantId]);
  const rows = query.data?.data ?? [];
  const mutate = async (action: AsyncAction) => {
    setBusy(true); setNotice(null);
    try { await action(); setNotice("success"); await queryClient.invalidateQueries({ queryKey: ["admin", "management-payment-links"] }); }
    catch (error) { setNotice(isStepUpError(error) ? "stepup" : "failure"); }
    finally { setBusy(false); }
  };
  const create = async (event:FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (!admin.scope || !admin.can("payment_links:write")) return;
    const form = event.currentTarget;
    const fields = new FormData(form);
    const expires = String(fields.get("expires_at") ?? "");
    const assetID = String(fields.get("asset_id") ?? "").trim();
    const route = routeProvider === "hosted_gateway"
      ? {provider:"hosted_gateway" as const,provider_id:String(fields.get("provider_id") ?? "").trim(),asset_id:assetID}
      : {provider:"on_chain" as const,chain_id:String(fields.get("chain_id") ?? "").trim(),asset_id:assetID};
    const input:PaymentLinkInput = {
      name:String(fields.get("name") ?? "").trim(), amount_minor:String(fields.get("amount_minor") ?? "").trim(), currency:String(fields.get("currency") ?? "").trim().toUpperCase(), currency_scale:Number(fields.get("currency_scale")), description:String(fields.get("description") ?? "").trim(),
      allowed_routes:[route], metadata:{}, success_url:String(fields.get("success_url") ?? "").trim(), cancel_url:String(fields.get("cancel_url") ?? "").trim(), max_uses:Number(fields.get("max_uses")),
      ...(String(fields.get("allowed_origin") ?? "").trim() ? {allowed_origin:String(fields.get("allowed_origin")).trim()} : {}), ...(expires ? {expires_at:new Date(expires).toISOString()} : {})
    };
    await mutate(async () => { const value = await admin.client!.createPaymentLink(admin.scope!, input, idempotencyKey()); if (!value.public_url) throw new Error("one_time_url_missing"); setCreated(value); form.reset(); setRouteProvider("on_chain"); });
  };
  return <div className="admin-page">
    <PageHeader actions={<Button onClick={() => void query.refetch()} variant="secondary"><RefreshCw size={15}/>{t("common.refresh")}</Button>} description={t("management.paymentLinksBody")} eyebrow={<><Link2 size={13}/>{t("management.managementAPI")}</>} title={t("page.paymentLinks.title")}/>
    {created?.public_url && <SecretPanel kind="link" onClear={() => setCreated(null)} publicURL={created.public_url}/>}<MutationNotice notice={notice}/>
    {admin.can("payment_links:write") && <SectionCard title={t("management.createPaymentLink")}><form className="admin-management-form" onSubmit={(event) => void create(event)}>
      <label><span>{t("common.name")}</span><Input name="name" required/></label><label><span>{t("management.amountMinor")}</span><Input inputMode="numeric" name="amount_minor" pattern="[1-9][0-9]*" required/></label><label><span>{t("common.currency")}</span><Input maxLength={3} name="currency" required/></label><label><span>{t("management.currencyScale")}</span><Input max={9} min={0} name="currency_scale" required type="number"/></label>
      <label><span>{t("management.routeProvider")}</span><select className="mp-select" name="provider" onChange={(event)=>setRouteProvider(event.target.value as typeof routeProvider)} value={routeProvider}><option value="on_chain">{t("management.onChain")}</option><option value="hosted_gateway">{t("management.hostedGateway")}</option></select></label>{routeProvider === "on_chain" ? <label><span>{t("management.chainId")}</span><Input name="chain_id" required/></label> : <label><span>{t("management.providerId")}</span><Input name="provider_id" required/></label>}<label><span>{t("management.assetId")}</span><Input name="asset_id" required/></label><label><span>{t("management.maxUses")}</span><Input defaultValue="1" max={1000000} min={1} name="max_uses" required type="number"/></label><label><span>{t("management.expiresAtOptional")}</span><Input name="expires_at" type="datetime-local"/></label>
      <label className="is-wide"><span>{t("common.description")}</span><textarea maxLength={1000} name="description" required rows={3}/></label><label className="is-wide"><span>{t("management.successURL")}</span><Input name="success_url" required type="url"/></label><label className="is-wide"><span>{t("management.cancelURL")}</span><Input name="cancel_url" required type="url"/></label><label className="is-wide"><span>{t("management.allowedOriginOptional")}</span><Input name="allowed_origin" type="url"/></label>
      <div className="is-wide"><Button disabled={busy} type="submit">{t("common.create")}</Button></div>
    </form></SectionCard>}
    <State busy={query.isLoading} empty={rows.length===0} error={query.error} permission={admin.can("payment_links:read")}/>
    {rows.length>0 && <Cards>{rows.map((item) => { const route=item.allowed_routes[0]; return <article className="admin-management-card" key={item.id}><header><div><strong>{item.name}</strong><small>{exactMinor(item.amount_minor,item.currency_scale)} {item.currency}</small></div><StatusBadge status={item.status}>{t(resourceStatusKey[item.status])}</StatusBadge></header><dl><div><dt>{t("management.route")}</dt><dd>{route?.provider === "hosted_gateway" ? route.provider_id : route?.chain_id} · {route?.asset_id}</dd></div><div><dt>{t("management.uses")}</dt><dd>{item.use_count} / {item.max_uses}</dd></div><div><dt>{t("management.settled")}</dt><dd>{item.settled_count}</dd></div><div><dt>{t("admin.objectVersion")}</dt><dd>{item.version}</dd></div><div><dt>{t("admin.expiresAt")}</dt><dd>{date(item.expires_at,locale)}</dd></div></dl>{admin.can("payment_links:write")&&item.status==="active"&&<Button disabled={busy} onClick={() => void mutate(() => admin.client!.disablePaymentLink(admin.scope!,item.id,item.version,idempotencyKey()))} variant="secondary">{t("management.disable")}</Button>}</article>; })}</Cards>}
  </div>;
}

export function APIClientsManagementPage() {
  const { locale, t } = useI18n(); const admin=useAdmin(); const queryClient=useQueryClient();
  const query=useAdminQuery<ManagementPage<APIClientRecord>>("management-api-clients","api_clients:read",(client,scope)=>client.apiClients(scope));
  const [secret,setSecret]=useState<APIClientSecret|null>(null);
  const [notice,setNotice]=useState<Notice>(null);
  const [busy,setBusy]=useState(false);
  const [requestReason,setRequestReason]=useState("");
  const [lastAction,setLastAction]=useState<ManagementActionRequest|null>(null);
  useEffect(()=>{setSecret(null);return()=>setSecret(null)},[admin.scope?.merchantId,admin.scope?.tenantId]); const rows=query.data?.data??[];
  const hasManagedClients=rows.some((item)=>item.managed);
  const mutate=async(action:ClientSecretAction)=>{setBusy(true);setNotice(null);try{const value=await action();if(value&&typeof value==="object"&&"secret" in value)setSecret(value as APIClientSecret);if(value&&typeof value==="object"&&"operation" in value)setLastAction(value as ManagementActionRequest);setNotice("success");await queryClient.invalidateQueries({queryKey:["admin","management-api-clients"]});}catch(error){setNotice(isStepUpError(error)?"stepup":"failure");}finally{setBusy(false)}};
  const create=(event:FormEvent<HTMLFormElement>)=>{event.preventDefault();if(!admin.scope||!admin.can("api_clients:write"))return;const data=new FormData(event.currentTarget);const until=String(data.get("valid_until")??"");const input:APIClientInput={name:String(data.get("name")??"").trim(),scopes:String(data.get("scopes")??"").split(",").map(value=>value.trim()).filter(Boolean),...(until?{valid_until:new Date(until).toISOString()}:{})};void mutate(()=>admin.client!.createAPIClient(admin.scope!,input,idempotencyKey()));};
  return <div className="admin-page"><PageHeader actions={<Button onClick={()=>void query.refetch()} variant="secondary"><RefreshCw size={15}/>{t("common.refresh")}</Button>} description={t("management.apiClientsBody")} eyebrow={<><KeyRound size={13}/>{t("management.managementAPI")}</>} title={t("page.apiClients.title")}/>{secret&&<SecretPanel keyId={secret.key_id} kind="credential" onClear={()=>setSecret(null)} secret={secret.secret}/>}<MutationNotice notice={notice}/>{lastAction&&<div className="admin-management-approval-note" role="status">{t("management.actionRequested",{id:lastAction.id})}</div>}
    {admin.can("api_clients:write")&&<SectionCard title={t("management.createAPIClient")}><form className="admin-management-form" onSubmit={create}><label><span>{t("common.name")}</span><Input name="name" required/></label><label><span>{t("management.validUntilOptional")}</span><Input name="valid_until" type="datetime-local"/></label><label className="is-wide"><span>{t("management.scopesComma")}</span><Input name="scopes" required/></label><div className="is-wide"><Button disabled={busy} type="submit">{t("common.create")}</Button></div></form></SectionCard>}
    {admin.can("api_clients:revoke")&&hasManagedClients&&<ReasonField onChange={setRequestReason} value={requestReason}/>}<State busy={query.isLoading} empty={rows.length===0} error={query.error} permission={admin.can("api_clients:read")}/>{rows.length>0&&<Cards>{rows.map(item=><article className="admin-management-card" key={item.id}><header><div><strong>{item.managed?item.name:t("management.directMerchantAPI")}</strong><small><code>{item.versions.map(version=>version.key_id).join(", ")}</code><br/>{item.scopes.join(", ")}</small></div><StatusBadge status={item.status}>{t(resourceStatusKey[item.status])}</StatusBadge></header>{!item.managed&&<div className="admin-management-approval-note" role="note">{t("management.directMerchantAPIReadOnly")}</div>}<dl><div><dt>{t("management.keyVersions")}</dt><dd>{item.versions.length}</dd></div><div><dt>{t("admin.objectVersion")}</dt><dd>{item.version}</dd></div><div><dt>{t("admin.createdAt")}</dt><dd>{date(item.created_at,locale)}</dd></div></dl><div className="admin-live-actions">{item.managed&&admin.can("api_clients:rotate")&&item.status==="active"&&<Button disabled={busy} onClick={()=>void mutate(()=>admin.client!.rotateAPIClient(admin.scope!,item.id,item.version,3600,idempotencyKey()))} variant="secondary">{t("management.rotateSecret")}</Button>}{item.managed&&admin.can("api_clients:revoke")&&item.status==="active"&&<Button disabled={busy||requestReason.trim().length<8} onClick={()=>void mutate(()=>admin.client!.requestAPIClientRevoke(admin.scope!,item.id,item.version,requestReason.trim(),idempotencyKey()))} variant="danger">{t("management.requestRevoke")}</Button>}</div></article>)}</Cards>}
  </div>;
}

export function WebhookManagementPage() {
  const { locale,t }=useI18n();const admin=useAdmin();const queryClient=useQueryClient();
  const endpoints=useAdminQuery<ManagementPage<WebhookEndpoint>>("management-webhooks","webhook_settings:read",(client,scope)=>client.webhookEndpoints(scope));
  const [selectedId,setSelectedId]=useState("");const selected=useMemo(()=>{const rows=Array.isArray(endpoints.data?.data)?endpoints.data.data:[];return selectedId==="__new__"?undefined:rows.find(item=>item.id===selectedId)??rows[0]},[endpoints.data,selectedId]);
  const deliveries=useQuery<ManagementPage<WebhookDelivery>,Error>({queryKey:["admin","management-webhook-deliveries",admin.scope?.tenantId,admin.scope?.merchantId,selected?.id],enabled:!admin.preview&&Boolean(admin.scope&&selected&&admin.can("webhook_settings:read")),queryFn:({signal})=>admin.clientFor(signal).webhookDeliveries(admin.scope!,selected!.id,"",50,signal)});
  const [secret,setSecret]=useState<WebhookEndpointSecret|null>(null);
  const [notice,setNotice]=useState<Notice>(null);
  const [busy,setBusy]=useState(false);
  const [reason,setReason]=useState("");
  const [disableReason,setDisableReason]=useState("");
  const [lastAction,setLastAction]=useState<ManagementActionRequest|null>(null);useEffect(()=>{setSecret(null);return()=>setSecret(null)},[admin.scope?.merchantId,admin.scope?.tenantId]);
  const refresh=async()=>{await Promise.all([queryClient.invalidateQueries({queryKey:["admin","management-webhooks"]}),queryClient.invalidateQueries({queryKey:["admin","management-webhook-deliveries"]})])};
  const mutate=async(action:AsyncAction)=>{setBusy(true);setNotice(null);try{const value=await action();if(value&&typeof value==="object"&&"secret" in value)setSecret(value as WebhookEndpointSecret);if(value&&typeof value==="object"&&"operation" in value)setLastAction(value as ManagementActionRequest);setNotice("success");await refresh();}catch(error){setNotice(isStepUpError(error)?"stepup":"failure");}finally{setBusy(false)}};
  const endpointInput=(form:HTMLFormElement):WebhookEndpointInput=>{const data=new FormData(form);return{url:String(data.get("url")??"").trim(),event_types:String(data.get("event_types")??"").split(",").map(value=>value.trim()).filter(Boolean),timeout_ms:Number(data.get("timeout_ms")),max_concurrency:Number(data.get("max_concurrency"))}};
  const submit=(event:FormEvent<HTMLFormElement>)=>{event.preventDefault();if(!admin.scope)return;const input=endpointInput(event.currentTarget);void mutate(()=>selected&&admin.can("webhook_settings:write")?admin.client!.updateWebhookEndpoint(admin.scope!,selected.id,{...input,version:selected.version},idempotencyKey()):admin.client!.createWebhookEndpoint(admin.scope!,input,idempotencyKey()));};
  const rows=Array.isArray(endpoints.data?.data)?endpoints.data.data:[];
  const deliveryRows=Array.isArray(deliveries.data?.data)?deliveries.data.data:[];
  return <div className="admin-page"><PageHeader actions={<Button onClick={()=>void endpoints.refetch()} variant="secondary"><RefreshCw size={15}/>{t("common.refresh")}</Button>} description={t("management.webhooksBody")} eyebrow={<><Webhook size={13}/>{t("management.managementAPI")}</>} title={t("page.webhooks.title")}/>{secret&&<SecretPanel keyId={secret.key_id} kind="credential" onClear={()=>setSecret(null)} secret={secret.secret}/>}<MutationNotice notice={notice}/>{lastAction&&<div className="admin-management-approval-note" role="status">{t("management.actionRequested",{id:lastAction.id})}</div>}
    {admin.can("webhook_settings:write")&&<SectionCard title={t(selected?"management.editWebhook":"management.createWebhook")}><form className="admin-management-form" key={selected?.id??"new"} onSubmit={submit}><label className="is-wide"><span>{t("webhooks.endpoint")}</span><Input defaultValue={selected?.url} name="url" required type="url"/></label><label className="is-wide"><span>{t("management.eventTypesComma")}</span><Input defaultValue={selected?.event_types.join(", ")} name="event_types" required/></label><label><span>{t("management.timeoutMs")}</span><Input defaultValue={selected?.timeout_ms??5000} max={30000} min={100} name="timeout_ms" required type="number"/></label><label><span>{t("management.maxConcurrency")}</span><Input defaultValue={selected?.max_concurrency??5} max={100} min={1} name="max_concurrency" required type="number"/></label><div className="is-wide admin-live-actions"><Button disabled={busy} type="submit">{t(selected?"common.save":"common.create")}</Button>{selected&&<Button onClick={()=>setSelectedId("__new__")} type="button" variant="secondary">{t("management.newEndpoint")}</Button>}</div></form></SectionCard>}
    {admin.can("webhook_settings:disable")&&rows.length>0&&<ReasonField onChange={setDisableReason} value={disableReason}/>}<State busy={endpoints.isLoading} empty={false} error={endpoints.error} permission={admin.can("webhook_settings:read")}/>{!endpoints.isLoading&&!endpoints.error&&rows.length===0&&<div className="admin-management-approval-note" role="status">{t("management.noWebhooksPollingNote")}</div>}{rows.length>0&&<Cards>{rows.map(item=><article className={`admin-management-card${selected?.id===item.id?" is-selected":""}`} key={item.id}><button aria-pressed={selected?.id===item.id} className="admin-management-card__select" onClick={()=>setSelectedId(item.id)} type="button"><span><strong>{item.url}</strong><small>{item.event_types.join(", ")}</small></span><StatusBadge status={item.status}>{t(resourceStatusKey[item.status])}</StatusBadge></button><dl><div><dt>{t("management.signingKey")}</dt><dd><code>{item.signing_key_id}</code></dd></div><div><dt>{t("admin.objectVersion")}</dt><dd>{item.version}</dd></div><div><dt>{t("admin.createdAt")}</dt><dd>{date(item.created_at,locale)}</dd></div></dl><div className="admin-live-actions">{admin.can("webhook_settings:write")&&item.status==="unverified"&&<Button disabled={busy} onClick={()=>void mutate(()=>admin.client!.verifyWebhookEndpoint(admin.scope!,item.id,item.version,idempotencyKey()))} variant="secondary">{t("management.verify")}</Button>}{admin.can("webhook_settings:rotate")&&item.status!=="disabled"&&<Button disabled={busy} onClick={()=>void mutate(()=>admin.client!.rotateWebhookEndpoint(admin.scope!,item.id,item.version,3600,idempotencyKey()))} variant="secondary">{t("management.rotateSecret")}</Button>}{admin.can("webhook_settings:disable")&&item.status!=="disabled"&&<Button disabled={busy||disableReason.trim().length<8} onClick={()=>void mutate(()=>admin.client!.requestWebhookDisable(admin.scope!,item.id,item.version,disableReason.trim(),idempotencyKey()))} variant="danger">{t("management.requestDisable")}</Button>}</div></article>)}</Cards>}
    {selected&&<SectionCard title={t("management.deliveries")}><ReasonField onChange={setReason} value={reason}/><State busy={deliveries.isLoading} empty={deliveryRows.length===0} error={deliveries.error} permission={admin.can("webhook_settings:read")}/><div className="admin-management-list">{deliveryRows.map(item=><article key={item.id}><div><strong>{item.event_type}</strong><small>{date(item.created_at,locale)}</small></div><StatusBadge status={item.status}>{t(deliveryStatusKey[item.status])}</StatusBadge><span>{t("management.attempts",{count:item.attempt_count})}</span>{(item.status==="dead_letter"||item.status==="retry")&&admin.can("webhook_settings:write")&&<Button disabled={busy||reason.trim().length<3} onClick={()=>void mutate(()=>admin.client!.retryWebhookDelivery(admin.scope!,item.id,item.version,reason.trim(),idempotencyKey()))} size="sm" variant="secondary">{t("management.retryDelivery")}</Button>}</article>)}</div></SectionCard>}
  </div>;
}

export function ManagementAuditPage() {
  const {locale,t}=useI18n();const admin=useAdmin();const query=useAdminQuery<ManagementPage<ManagementAuditEvent>>("management-audit","management_audit:read",(client,scope)=>client.managementAudit(scope));const rows=query.data?.data??[];
  return <div className="admin-page"><PageHeader actions={<Button onClick={()=>void query.refetch()} variant="secondary"><RefreshCw size={15}/>{t("common.refresh")}</Button>} description={t("management.auditBody")} eyebrow={<><FileClock size={13}/>{t("management.hashChained")}</>} title={t("management.auditTitle")}/><State busy={query.isLoading} empty={rows.length===0} error={query.error} permission={admin.can("management_audit:read")}/><div className="admin-management-list">{rows.map(item=><article key={item.id}><div><strong>{item.action}</strong><small>{item.resource_type} · {item.resource_id}</small></div><span>{item.reason??"—"}</span><code>{item.entry_hash.slice(0,16)}…</code><time>{date(item.occurred_at,locale)}</time></article>)}</div></div>;
}

function categoryPermission(category:ManagementActionCategory):Permission{return category==="webhook-disable"?"webhook_settings:disable":"api_clients:revoke"}

export function ManagementActionsPage(){
  const {locale,t}=useI18n();const admin=useAdmin();const cache=useQueryClient();
  const [category,setCategory]=useState<ManagementActionCategory>("webhook-disable");
  const [selectedId,setSelectedId]=useState("");const [reason,setReason]=useState("");
  const [notice,setNotice]=useState<Notice>(null);const [busy,setBusy]=useState(false);
  const permission=categoryPermission(category);
  const page=useQuery<ManagementPage<ManagementActionRequest>,Error>({queryKey:["admin","management-actions",admin.scope?.tenantId,admin.scope?.merchantId,category],enabled:!admin.preview&&Boolean(admin.scope)&&admin.can(permission),queryFn:({signal})=>admin.clientFor(signal).managementActions(admin.scope!,category,"",50,signal)});
  const rows=Array.isArray(page.data?.data)?page.data.data:[];
  const listSelected=rows.find(item=>item.id===selectedId)??rows[0];
  const detail=useQuery<ManagementActionRequest,Error>({queryKey:["admin","management-action",admin.scope?.tenantId,admin.scope?.merchantId,category,listSelected?.id],enabled:!admin.preview&&Boolean(admin.scope&&listSelected)&&admin.can(permission),queryFn:({signal})=>admin.clientFor(signal).managementAction(admin.scope!,category,listSelected!.id,signal)});
  const action=detail.data??listSelected;const expired=Boolean(action?.status==="pending_approval"&&Date.parse(action.expires_at)<=Date.now());const own=action?.requested_by===admin.principal?.user_id;
  const statusLabel=(status:ManagementActionRequest["status"])=>t(({pending_approval:"management.statusPending",executing:"management.statusExecuting",completed:"management.statusCompleted",rejected:"management.statusRejected",failed:"management.statusFailed"} as const)[status]);
  const operationLabel=(operation:ManagementActionRequest["operation"])=>t(operation==="webhook.disable"?"management.webhookDisable":"management.apiClientRevoke");
  const decide=async(decision:"approve"|"reject")=>{if(!admin.scope||!action||reason.trim().length<1||expired||own)return;setBusy(true);setNotice(null);try{await admin.client!.decideManagementAction(admin.scope,category,action.id,decision,reason.trim(),idempotencyKey());setNotice("success");await Promise.all([cache.invalidateQueries({queryKey:["admin","management-actions"]}),cache.invalidateQueries({queryKey:["admin","management-action"]})])}catch(error){setNotice(isStepUpError(error)?"stepup":"failure")}finally{setBusy(false)}};
  return <div className="admin-page"><PageHeader actions={<Button onClick={()=>void page.refetch()} variant="secondary"><RefreshCw size={15}/>{t("common.refresh")}</Button>} description={t("management.actionsBody")} eyebrow={<><KeyRound size={13}/>{t("management.fourEyes")}</>} title={t("management.actionsTitle")}/><div className="admin-platform-toolbar"><label><span>{t("management.actionCategory")}</span><select className="mp-select" onChange={event=>{setCategory(event.target.value as ManagementActionCategory);setSelectedId("")}} value={category}><option value="webhook-disable">{t("management.webhookDisable")}</option><option value="api-client-revoke">{t("management.apiClientRevoke")}</option></select></label></div><MutationNotice notice={notice}/>
    <State busy={page.isLoading} empty={rows.length===0} error={page.error} permission={admin.can(permission)}/>{rows.length>0&&<div className="admin-platform-columns"><SectionCard title={t("management.actionRequests")}><div className="admin-platform-items">{rows.map(item=><button aria-pressed={action?.id===item.id} key={item.id} onClick={()=>setSelectedId(item.id)} type="button"><span><strong>{operationLabel(item.operation)}</strong><small>{item.resource_id}</small></span><StatusBadge status={item.status}>{statusLabel(item.status)}</StatusBadge></button>)}</div></SectionCard>{action&&<SectionCard title={t("management.actionDetail")}><dl className="admin-live-facts"><div><dt>{t("common.status")}</dt><dd><StatusBadge status={expired?"expired":action.status}>{expired?t("management.expired"):statusLabel(action.status)}</StatusBadge></dd></div><div><dt>{t("management.requester")}</dt><dd><code>{action.requested_by}</code></dd></div><div><dt>{t("management.approver")}</dt><dd><code>{action.approved_by??"—"}</code></dd></div><div><dt>{t("admin.objectVersion")}</dt><dd>{action.resource_version}</dd></div><div><dt>{t("admin.expiresAt")}</dt><dd>{date(action.expires_at,locale)}</dd></div><div><dt>{t("admin.reason")}</dt><dd>{action.request_reason}</dd></div></dl>{action.failure_code&&<p className="admin-management-approval-note" role="alert">{t("management.actionFailure",{code:action.failure_code})}</p>}{own&&<p className="admin-management-approval-note">{t("management.actionSecondOperator")}</p>}{expired&&<p className="admin-management-approval-note">{t("management.actionExpired")}</p>}<p className="admin-management-replay-note">{t("management.actionReplayBody")}</p>{action.status==="pending_approval"&&!expired&&<><ReasonField onChange={setReason} value={reason}/><div className="admin-live-actions"><Button disabled={busy||own||reason.trim().length<1} onClick={()=>void decide("approve")}>{t("admin.approve")}</Button><Button disabled={busy||own||reason.trim().length<1} onClick={()=>void decide("reject")} variant="danger">{t("admin.reject")}</Button></div></>}</SectionCard>}</div>}
  </div>;
}
