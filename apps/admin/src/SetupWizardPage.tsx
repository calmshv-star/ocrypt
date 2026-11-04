import { useI18n, type MessageKey } from "@merchant/i18n";
import { Badge, Button, Input, PageHeader, SectionCard, StatusBadge } from "@merchant/ui";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Check, Compass } from "lucide-react";
import { useState, type FormEvent, type KeyboardEvent } from "react";
import { useAdmin } from "./AdminProvider";
import { SecretPanel } from "./ManagementPages";
import type { APIClientSecret, IntentRow, ManagementPage, MerchantProjectSettings, Page, PaymentLink, WebhookDelivery, WebhookEndpointSecret } from "./api/types";
import { useGoLiveState } from "./goLive";
import {
  ACCEPTANCE_METHODS, CHALLENGE_EXAMPLE, METHODS_COMING, METHODS_WITH_WEBHOOK, SIGNATURE_SNIPPETS, SNIPPET_LANGUAGES,
  URL_PLACEHOLDER, WIZARD_KEY_SCOPES, WIZARD_PROFILE_REASON, WIZARD_WEBHOOK_EVENTS, httpStatusLabel, isMissing, managementOutcome,
  nameFits, newProfile, nextLanguage, saveMethod, savedMethod, timeZones,
  type AcceptanceMethod, type Outcome, type SnippetLanguage, type WizardAction
} from "./setupWizard";

const PAID_STATUSES = new Set(["settled", "overpaid"]);
const LOCALES: MerchantProjectSettings["locale"][] = ["en", "ru", "zh-CN", "es", "fr", "de"];
// The test-payment chain refreshes itself until every link of it is done.
const CHAIN_REFRESH_MS = 5000;
const key = () => `admin-${crypto.randomUUID()}`;

// The first-run wizard (TZ: profile, acceptance method, webhook, test
// payment, then live mode). Each step uses the same APIs and cache entries as
// its full page.
export function SetupWizardPage() {
  const { t } = useI18n();
  const admin = useAdmin();
  const merchantId = admin.scope?.merchantId ?? "";
  const [method, setMethod] = useState<AcceptanceMethod | null>(() => merchantId ? savedMethod(merchantId) : null);
  const choose = (value: AcceptanceMethod) => { setMethod(value); if (merchantId) saveMethod(merchantId, value); };
  const webhookStep = method !== null && METHODS_WITH_WEBHOOK.has(method);
  const after = webhookStep ? 1 : 0;
  return <div className="admin-page" data-testid="setup-wizard-page">
    <PageHeader description={t("wizard.body")} eyebrow={<><Compass size={13} />{t("onboarding.nav")}</>} title={t("wizard.title")} />
    <ol className="wizard__steps">
      <li><ProfileStep index={1} /></li>
      <li><MethodStep index={2} method={method} onChoose={choose} /></li>
      {webhookStep && <li><WebhookStep index={3} /></li>}
      <li><TestPaymentStep index={3 + after} webhookStep={webhookStep} /></li>
      <li><GoLiveStep index={4 + after} /></li>
    </ol>
  </div>;
}

function StepTitle({ index, done, title }: { index: number; done: boolean; title: MessageKey }) {
  const { t } = useI18n();
  return <span className="wizard__title">
    <span aria-hidden="true" className="wizard__number" data-done={done || undefined}>{done ? <Check size={14} /> : index}</span>
    {t(title)}
    {done && <span className="mp-sr-only">{t("wizard.done")}</span>}
  </span>;
}

function OutcomeLine({ outcome }: { outcome: Outcome }) {
  const { t } = useI18n();
  const admin = useAdmin();
  if (!outcome) return null;
  if (outcome === "stepUp") return <p role="alert">{t("wizard.stepUp")} <a href={admin.client?.stepUpURL("/#/setup")}>{t("signupReviews.stepUp")}</a></p>;
  if (outcome === "verifyFailed") return <p role="alert">{t("wizard.webhook.verifyFailed")}</p>;
  return <p className="security__outcome" role={outcome === "saved" ? "status" : "alert"}>{t(outcome === "saved" ? "notifications.saved" : "wizard.failed")}</p>;
}

function ProfileStep({ index }: { index: number }) {
  const { locale, t } = useI18n();
  const admin = useAdmin();
  const cache = useQueryClient();
  const scope = admin.scope;
  const canWrite = admin.can("settings:write");
  // The Settings page's cache entry, so neither page keeps a stale version.
  const settings = useQuery({
    queryKey: ["merchant-settings", scope?.tenantId, scope?.merchantId],
    enabled: !admin.preview && Boolean(scope?.merchantId) && admin.can("settings:read"),
    queryFn: ({ signal }) => admin.clientFor(signal).projectSettings(scope!, signal)
  });
  const [busy, setBusy] = useState(false);
  const [tooLong, setTooLong] = useState(false);
  const [outcome, setOutcome] = useState<Outcome>(null);
  const missing = isMissing(settings.error);
  const current = settings.data ?? (missing ? newProfile(locale) : undefined);
  const done = Boolean(settings.data?.display_name && settings.data.support_email);
  const save = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (!admin.client || !scope || !current || busy) return;
    const data = new FormData(event.currentTarget);
    const name = String(data.get("display_name") ?? "").trim();
    setTooLong(!nameFits(name));
    if (!nameFits(name)) return;
    setBusy(true);
    setOutcome(null);
    try {
      await admin.client.updateProjectSettings(scope, {
        version: current.version,
        display_name: name,
        locale: String(data.get("locale")) as MerchantProjectSettings["locale"],
        timezone: String(data.get("timezone") ?? "").trim(),
        support_email: String(data.get("support_email") ?? "").trim() || undefined,
        notifications: current.notifications,
        allowed_embed_origins: current.allowed_embed_origins,
        reason: WIZARD_PROFILE_REASON
      }, key());
      setOutcome("saved");
      await cache.invalidateQueries({ queryKey: ["merchant-settings"] });
    } catch (error) {
      setOutcome(managementOutcome(error));
    } finally {
      setBusy(false);
    }
  };
  return <SectionCard description={t("wizard.profile.body")} title={<StepTitle done={done} index={index} title="wizard.profile.title" />}>
    {settings.isLoading && <div aria-busy="true" className="admin-route-loading"><span /><span /><span /></div>}
    {settings.error && !missing && <p role="alert">{t("wizard.profile.loadFailed")}</p>}
    {current && <form className="admin-management-form" key={current.version} onSubmit={(event) => void save(event)}>
      <label><span>{t("wizard.profile.name")}</span><Input defaultValue={current.display_name} disabled={!canWrite} maxLength={120} name="display_name" required /></label>
      <label><span>{t("wizard.profile.email")}</span><Input defaultValue={current.support_email ?? ""} disabled={!canWrite} name="support_email" type="email" /></label>
      <label><span>{t("wizard.profile.language")}</span><select className="mp-input" defaultValue={current.locale} disabled={!canWrite} name="locale">{LOCALES.map((value) => <option key={value} value={value}>{value}</option>)}</select></label>
      <label><span>{t("wizard.profile.timezone")}</span><select className="mp-input" defaultValue={current.timezone} disabled={!canWrite} name="timezone">{timeZones(current.timezone).map((zone) => <option key={zone} value={zone}>{zone}</option>)}</select></label>
      {canWrite && <div className="is-wide"><Button disabled={busy} type="submit">{t("notifications.save")}</Button></div>}
    </form>}
    {tooLong && <p role="alert">{t("wizard.profile.nameTooLong")}</p>}
    {!canWrite && <p className="go-live__note">{t("wizard.noRights")}</p>}
    <p className="go-live__note">{t("wizard.profile.later")}</p>
    <OutcomeLine outcome={outcome} />
  </SectionCard>;
}

function MethodStep({ index, method, onChoose }: { index: number; method: AcceptanceMethod | null; onChoose: (method: AcceptanceMethod) => void }) {
  const { t } = useI18n();
  const admin = useAdmin();
  const cache = useQueryClient();
  const [secret, setSecret] = useState<APIClientSecret | null>(null);
  const [busy, setBusy] = useState(false);
  const [outcome, setOutcome] = useState<Outcome>(null);
  const needsKey = method === "api" || method === "plugin";
  const createKey = async () => {
    if (!admin.client || !admin.scope || busy) return;
    setBusy(true);
    setOutcome(null);
    try {
      setSecret(await admin.client.createAPIClient(admin.scope, { name: t("wizard.method.keyName"), scopes: WIZARD_KEY_SCOPES }, key()));
      await Promise.all([cache.invalidateQueries({ queryKey: ["admin", "management-api-clients"] }), cache.invalidateQueries({ queryKey: ["admin", "onboarding-api-clients"] })]);
    } catch (error) {
      setOutcome(managementOutcome(error));
    } finally {
      setBusy(false);
    }
  };
  return <SectionCard description={t("wizard.method.body")} title={<StepTitle done={method !== null} index={index} title="wizard.method.title" />}>
    <div className="go-live__presets" role="radiogroup" aria-label={t("wizard.method.title")}>{ACCEPTANCE_METHODS.map((item) => {
      const title = t(`wizard.method.${item}.title` as MessageKey);
      const coming = METHODS_COMING.has(item);
      return <label className="go-live__preset" data-selected={method === item || undefined} key={item}>
        <input aria-describedby={`wizard-method-${item}`} aria-label={coming ? `${title} (${t("wizard.method.soon")})` : title} checked={method === item} name="method" onChange={() => onChoose(item)} type="radio" value={item} />
        <strong>{title} {coming && <Badge tone="neutral">{t("wizard.method.soon")}</Badge>}</strong>
        <small id={`wizard-method-${item}`}>{t(`wizard.method.${item}.body` as MessageKey)}</small>
      </label>;
    })}</div>
    {method && <div className="wizard__instructions">
      <p>{t(`wizard.method.${method}.steps` as MessageKey)}</p>
      {method === "link" && admin.can("payment_links:read") && <a className="mp-button mp-button--secondary mp-button--md" href="#/payment-links">{t("wizard.method.openLinks")}</a>}
      {needsKey && admin.can("api_clients:write") && !secret && <Button disabled={busy} onClick={() => void createKey()} variant="secondary">{t("wizard.method.createKey")}</Button>}
      {needsKey && !admin.can("api_clients:write") && <p className="go-live__note">{t("wizard.method.noKeyRights")}</p>}
      {secret && <><p>{t("wizard.method.keyOnce")}</p><SecretPanel keyId={secret.key_id} kind="credential" onClear={() => setSecret(null)} secret={secret.secret} titleId="wizard-key-secret" /></>}
      {METHODS_COMING.has(method) && <p className="go-live__note">{t("wizard.method.comingNote")}</p>}
    </div>}
    <OutcomeLine outcome={outcome} />
  </SectionCard>;
}

function useEndpoints() {
  const admin = useAdmin();
  return useQuery({
    queryKey: ["admin", "management-webhooks", admin.scope?.tenantId, admin.scope?.merchantId],
    enabled: !admin.preview && admin.sessionState === "ready" && Boolean(admin.scope) && admin.can("webhook_settings:read"),
    queryFn: ({ signal }) => admin.clientFor(signal).webhookEndpoints(admin.scope!)
  });
}

function WebhookStep({ index }: { index: number }) {
  const { t } = useI18n();
  const admin = useAdmin();
  const cache = useQueryClient();
  const endpoints = useEndpoints();
  const [secret, setSecret] = useState<WebhookEndpointSecret | null>(null);
  const [busy, setBusy] = useState(false);
  const [outcome, setOutcome] = useState<Outcome>(null);
  const [language, setLanguage] = useState<SnippetLanguage>("node");
  // A disabled endpoint no longer receives events; the wizard sets up a new one.
  const endpoint = endpoints.data?.data.find((item) => item.status !== "disabled");
  const canWrite = admin.can("webhook_settings:write");
  const run = async (action: WizardAction, verifying: boolean) => {
    if (busy) return;
    setBusy(true);
    setOutcome(null);
    try {
      await action();
      await Promise.all([cache.invalidateQueries({ queryKey: ["admin", "management-webhooks"] }), cache.invalidateQueries({ queryKey: ["admin", "onboarding-webhooks"] })]);
    } catch (error) {
      setOutcome(managementOutcome(error, verifying));
    } finally {
      setBusy(false);
    }
  };
  const create = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (!admin.client || !admin.scope) return;
    const url = String(new FormData(event.currentTarget).get("url") ?? "").trim();
    void run(async () => setSecret(await admin.client!.createWebhookEndpoint(admin.scope!, { url, event_types: WIZARD_WEBHOOK_EVENTS, timeout_ms: 5000, max_concurrency: 4 }, key())), false);
  };
  const verify = () => endpoint && void run(() => admin.client!.verifyWebhookEndpoint(admin.scope!, endpoint.id, endpoint.version, key()), true);
  const moveTab = (event: KeyboardEvent<HTMLDivElement>) => {
    const next = nextLanguage(language, event.key);
    if (!next) return;
    event.preventDefault();
    setLanguage(next);
    document.getElementById(`wizard-tab-${next}`)?.focus();
  };
  return <SectionCard description={t("wizard.webhook.body")} title={<StepTitle done={endpoint?.status === "active"} index={index} title="wizard.webhook.title" />}>
    {!endpoint && canWrite && <form className="admin-management-form" onSubmit={create}>
      <label className="is-wide"><span>{t("webhooks.endpoint")}</span><Input name="url" placeholder={URL_PLACEHOLDER} required type="url" /></label>
      <div className="is-wide"><Button disabled={busy} type="submit">{t("wizard.webhook.create")}</Button></div>
    </form>}
    {!endpoint && !canWrite && <p className="go-live__note">{t("wizard.noRights")}</p>}
    {secret && <><p>{t("wizard.webhook.secretOnce")}</p><SecretPanel keyId={secret.key_id} kind="credential" onClear={() => setSecret(null)} secret={secret.secret} titleId="wizard-webhook-secret" /></>}
    {endpoint && <div className="wizard__endpoint">
      <span><code>{endpoint.url}</code> <StatusBadge status={endpoint.status === "active" ? "settled" : "pending"}>{t(endpoint.status === "active" ? "wizard.webhook.verified" : "wizard.webhook.unverified")}</StatusBadge></span>
      {endpoint.status === "unverified" && canWrite && <Button disabled={busy} onClick={verify} variant="secondary">{t("wizard.webhook.verify")}</Button>}
    </div>}
    <OutcomeLine outcome={outcome} />
    <p>{t("wizard.webhook.challenge")}</p>
    <pre className="wizard__code">{CHALLENGE_EXAMPLE}</pre>
    <p>{t("wizard.webhook.signature")}</p>
    <div aria-label={t("wizard.webhook.signature")} className="wizard__tabs" onKeyDown={moveTab} role="tablist">{SNIPPET_LANGUAGES.map((item) => <button aria-controls="wizard-snippet" aria-selected={language === item} id={`wizard-tab-${item}`} key={item} onClick={() => setLanguage(item)} role="tab" tabIndex={language === item ? 0 : -1} type="button">{t(`wizard.language.${item}` as MessageKey)}</button>)}</div>
    <pre aria-labelledby={`wizard-tab-${language}`} className="wizard__code" id="wizard-snippet" role="tabpanel" tabIndex={0}>{SIGNATURE_SNIPPETS[language]}</pre>
  </SectionCard>;
}

function TestPaymentStep({ index, webhookStep }: { index: number; webhookStep: boolean }) {
  const { t } = useI18n();
  const admin = useAdmin();
  const scope = admin.scope;
  const ready = !admin.preview && admin.sessionState === "ready" && Boolean(scope);
  // Same cache entries as the Getting started page; each query keeps
  // refreshing until its link of the chain is done.
  const intents = useQuery<Page<IntentRow>, Error>({
    queryKey: ["admin", "onboarding-intents", scope?.tenantId, scope?.merchantId],
    enabled: ready && admin.can("payments:read"),
    refetchInterval: (query) => query.state.data?.items.some((intent) => PAID_STATUSES.has(intent.status)) ? false : CHAIN_REFRESH_MS,
    queryFn: ({ signal }) => admin.clientFor(signal).intents(scope!, "", 50)
  });
  const links = useQuery<ManagementPage<PaymentLink>, Error>({
    queryKey: ["admin", "onboarding-payment-links", scope?.tenantId, scope?.merchantId],
    enabled: ready && admin.can("payment_links:read"),
    refetchInterval: (query) => (query.state.data?.data.length ?? 0) > 0 || (intents.data?.items.length ?? 0) > 0 ? false : CHAIN_REFRESH_MS,
    queryFn: ({ signal }) => admin.clientFor(signal).paymentLinks(scope!, "", 1)
  });
  const endpoints = useEndpoints();
  const endpoint = endpoints.data?.data.find((item) => item.status !== "disabled");
  const deliveries = useQuery<ManagementPage<WebhookDelivery>, Error>({
    queryKey: ["admin", "management-webhook-deliveries", scope?.tenantId, scope?.merchantId, endpoint?.id, "latest"],
    enabled: ready && webhookStep && Boolean(endpoint) && admin.can("webhook_settings:read"),
    refetchInterval: (query) => query.state.data?.data[0]?.status === "acknowledged" ? false : CHAIN_REFRESH_MS,
    queryFn: ({ signal }) => admin.clientFor(signal).webhookDeliveries(scope!, endpoint!.id, "", 5, signal)
  });
  const linked = (links.data?.data.length ?? 0) > 0 || (intents.data?.items.length ?? 0) > 0;
  const paid = Boolean(intents.data?.items.some((intent) => PAID_STATUSES.has(intent.status)));
  const latest = deliveries.data?.data[0];
  const delivered = latest?.status === "acknowledged";
  const chain: Array<{ key: MessageKey; done: boolean }> = [
    { key: "wizard.test.link", done: linked },
    { key: "wizard.test.paid", done: paid },
    ...(webhookStep ? [{ key: "wizard.test.webhook" as MessageKey, done: delivered }] : [])
  ];
  return <SectionCard description={t("wizard.test.body")} title={<StepTitle done={chain.every((item) => item.done)} index={index} title="wizard.test.title" />}>
    <ol className="wizard__chain">{chain.map((item) => <li data-done={item.done || undefined} key={item.key}>
      {item.done ? <Check aria-hidden="true" size={14} /> : <span aria-hidden="true">·</span>}
      {t(item.key)}
      <span className="mp-sr-only">{t(item.done ? "wizard.done" : "wizard.notDone")}</span>
    </li>)}</ol>
    {!linked && admin.can("payment_links:read") && <a className="mp-button mp-button--secondary mp-button--md" href="#/payment-links">{t("wizard.method.openLinks")}</a>}
    {latest && <div className="wizard__delivery" role="status">
      <strong>{t("wizard.test.response")}</strong>
      <span>{latest.event_type}</span>
      <span>{httpStatusLabel(latest.last_http_status) ?? t("wizard.test.noResponse")}</span>
      {latest.response_snippet && <pre className="wizard__code">{latest.response_snippet}</pre>}
    </div>}
    <p className="go-live__note">{t("wizard.test.sandbox")}</p>
  </SectionCard>;
}

function GoLiveStep({ index }: { index: number }) {
  const { t } = useI18n();
  const admin = useAdmin();
  const state = useGoLiveState();
  const live = state.data?.environment === "live";
  return <SectionCard description={t("wizard.live.body")} title={<StepTitle done={live} index={index} title="wizard.live.title" />}>
    {!live && admin.can("go_live:request") && <a className="mp-button mp-button--primary mp-button--md" href="#/go-live">{t("wizard.live.open")}</a>}
  </SectionCard>;
}
