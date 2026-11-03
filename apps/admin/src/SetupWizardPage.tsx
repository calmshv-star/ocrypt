import { useI18n, type MessageKey } from "@merchant/i18n";
import { Badge, Button, Input, PageHeader, SectionCard, StatusBadge } from "@merchant/ui";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Check, Compass } from "lucide-react";
import { useState, type FormEvent } from "react";
import { isStepUpError, useAdmin, useAdminQuery } from "./AdminProvider";
import type { APIClientSecret, ManagementPage, MerchantProjectSettings, WebhookDelivery, WebhookEndpoint, WebhookEndpointSecret } from "./api/types";
import { useGoLiveState } from "./goLive";
import {
  ACCEPTANCE_METHODS, CHALLENGE_EXAMPLE, METHODS_COMING, METHODS_WITH_WEBHOOK, SIGNATURE_SNIPPETS, SNIPPET_LANGUAGES,
  URL_PLACEHOLDER, WIZARD_KEY_SCOPES, WIZARD_PROFILE_REASON, WIZARD_WEBHOOK_EVENTS, httpStatusLabel, saveMethod, savedMethod,
  type AcceptanceMethod, type SnippetLanguage, type WizardAction
} from "./setupWizard";

const PAID_STATUSES = new Set(["settled", "overpaid"]);
const LOCALES: MerchantProjectSettings["locale"][] = ["en", "ru", "zh-CN", "es", "fr", "de"];
type Outcome = "saved" | "failed" | "stepUp" | null;
const key = () => `admin-${crypto.randomUUID()}`;

// The first-run wizard (TZ: profile, acceptance method, webhook, test
// payment, then live mode). Each step uses the same APIs as its full page.
export function SetupWizardPage() {
  const { t } = useI18n();
  const admin = useAdmin();
  const merchantId = admin.scope?.merchantId ?? "";
  const [method, setMethod] = useState<AcceptanceMethod | null>(() => merchantId ? savedMethod(merchantId) : null);
  const choose = (value: AcceptanceMethod) => { setMethod(value); if (merchantId) saveMethod(merchantId, value); };
  const webhookStep = method !== null && METHODS_WITH_WEBHOOK.has(method);
  return <div className="admin-page" data-testid="setup-wizard-page">
    <PageHeader description={t("wizard.body")} eyebrow={<><Compass size={13} />{t("onboarding.nav")}</>} title={t("wizard.title")} />
    <ol className="wizard__steps">
      <li><ProfileStep /></li>
      <li><MethodStep method={method} onChoose={choose} /></li>
      {webhookStep && <li><WebhookStep /></li>}
      <li><TestPaymentStep webhookStep={webhookStep} /></li>
      <li><GoLiveStep /></li>
    </ol>
  </div>;
}

function StepTitle({ index, done, title }: { index: number; done: boolean; title: MessageKey }) {
  const { t } = useI18n();
  return <span className="wizard__title"><span aria-hidden="true" className="wizard__number" data-done={done || undefined}>{done ? <Check size={14} /> : index}</span>{t(title)}</span>;
}

function OutcomeLine({ outcome }: { outcome: Outcome }) {
  const { t } = useI18n();
  const admin = useAdmin();
  if (!outcome) return null;
  if (outcome === "stepUp") return <p role="alert">{t("signupReviews.stepUpBody")} <a href={admin.client?.stepUpURL("/#/setup")}>{t("signupReviews.stepUp")}</a></p>;
  return <p className="security__outcome" role={outcome === "saved" ? "status" : "alert"}>{t(outcome === "saved" ? "notifications.saved" : "wizard.failed")}</p>;
}

function ProfileStep() {
  const { t } = useI18n();
  const admin = useAdmin();
  const cache = useQueryClient();
  const settings = useAdminQuery<MerchantProjectSettings>("merchant-settings-project", "settings:read", (client, scope) => client.projectSettings(scope));
  const [busy, setBusy] = useState(false);
  const [outcome, setOutcome] = useState<Outcome>(null);
  const current = settings.data;
  const done = Boolean(current?.display_name && current.support_email);
  const save = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (!admin.client || !admin.scope || !current || busy) return;
    const data = new FormData(event.currentTarget);
    setBusy(true);
    setOutcome(null);
    try {
      await admin.client.updateProjectSettings(admin.scope, {
        version: current.version,
        display_name: String(data.get("display_name") ?? "").trim(),
        locale: String(data.get("locale")) as MerchantProjectSettings["locale"],
        timezone: String(data.get("timezone") ?? "").trim(),
        support_email: String(data.get("support_email") ?? "").trim() || undefined,
        notifications: current.notifications,
        allowed_embed_origins: current.allowed_embed_origins,
        reason: WIZARD_PROFILE_REASON
      }, key());
      setOutcome("saved");
      await cache.invalidateQueries({ queryKey: ["admin", "merchant-settings-project"] });
    } catch (error) {
      setOutcome(isStepUpError(error) ? "stepUp" : "failed");
    } finally {
      setBusy(false);
    }
  };
  return <SectionCard description={t("wizard.profile.body")} title={<StepTitle done={done} index={1} title="wizard.profile.title" />}>
    {settings.isLoading && <div aria-busy="true" className="admin-route-loading"><span /><span /><span /></div>}
    {current && <form className="admin-management-form" key={current.version} onSubmit={(event) => void save(event)}>
      <label><span>{t("wizard.profile.name")}</span><Input defaultValue={current.display_name} maxLength={120} name="display_name" required /></label>
      <label><span>{t("wizard.profile.email")}</span><Input defaultValue={current.support_email ?? ""} name="support_email" type="email" /></label>
      <label><span>{t("wizard.profile.language")}</span><select className="mp-input" defaultValue={current.locale} name="locale">{LOCALES.map((locale) => <option key={locale} value={locale}>{locale}</option>)}</select></label>
      <label><span>{t("wizard.profile.timezone")}</span><Input defaultValue={current.timezone} name="timezone" required /></label>
      {admin.can("settings:write") && <div className="is-wide"><Button disabled={busy} type="submit">{t("notifications.save")}</Button></div>}
    </form>}
    <p className="go-live__note">{t("wizard.profile.later")}</p>
    <OutcomeLine outcome={outcome} />
  </SectionCard>;
}

function MethodStep({ method, onChoose }: { method: AcceptanceMethod | null; onChoose: (method: AcceptanceMethod) => void }) {
  const { t } = useI18n();
  const admin = useAdmin();
  const [secret, setSecret] = useState<APIClientSecret | null>(null);
  const [busy, setBusy] = useState(false);
  const [outcome, setOutcome] = useState<Outcome>(null);
  const createKey = async () => {
    if (!admin.client || !admin.scope || busy) return;
    setBusy(true);
    setOutcome(null);
    try {
      setSecret(await admin.client.createAPIClient(admin.scope, { name: t("wizard.method.keyName"), scopes: WIZARD_KEY_SCOPES }, key()));
    } catch (error) {
      setOutcome(isStepUpError(error) ? "stepUp" : "failed");
    } finally {
      setBusy(false);
    }
  };
  return <SectionCard description={t("wizard.method.body")} title={<StepTitle done={method !== null} index={2} title="wizard.method.title" />}>
    <div className="go-live__presets" role="radiogroup" aria-label={t("wizard.method.title")}>{ACCEPTANCE_METHODS.map((item) => <label className="go-live__preset" data-selected={method === item || undefined} key={item}>
      <input checked={method === item} name="method" onChange={() => onChoose(item)} type="radio" value={item} />
      <strong>{t(`wizard.method.${item}.title` as MessageKey)}{METHODS_COMING.has(item) && <Badge tone="neutral">{t("wizard.method.soon")}</Badge>}</strong>
      <small>{t(`wizard.method.${item}.body` as MessageKey)}</small>
    </label>)}</div>
    {method && <div className="wizard__instructions">
      <p>{t(`wizard.method.${method}.steps` as MessageKey)}</p>
      {method === "link" && <a className="mp-button mp-button--secondary mp-button--md" href="#/payment-links">{t("wizard.method.openLinks")}</a>}
      {(method === "api" || method === "plugin") && admin.can("api_clients:write") && !secret && <Button disabled={busy} onClick={() => void createKey()} variant="secondary">{t("wizard.method.createKey")}</Button>}
      {secret && <div className="wizard__secret" role="status">
        <p>{t("wizard.method.keyOnce")}</p>
        <dl><div><dt>{t("wizard.keyId")}</dt><dd><code>{secret.key_id}</code></dd></div><div><dt>{t("wizard.secret")}</dt><dd><code>{secret.secret}</code></dd></div></dl>
      </div>}
      {METHODS_COMING.has(method) && <p className="go-live__note">{t("wizard.method.comingNote")}</p>}
    </div>}
    <OutcomeLine outcome={outcome} />
  </SectionCard>;
}

function useEndpoints() {
  return useAdminQuery<ManagementPage<WebhookEndpoint>>("management-webhooks", "webhook_settings:read", (client, scope) => client.webhookEndpoints(scope));
}

function WebhookStep() {
  const { t } = useI18n();
  const admin = useAdmin();
  const cache = useQueryClient();
  const endpoints = useEndpoints();
  const [secret, setSecret] = useState<WebhookEndpointSecret | null>(null);
  const [busy, setBusy] = useState(false);
  const [outcome, setOutcome] = useState<Outcome>(null);
  const [verifyFailed, setVerifyFailed] = useState(false);
  const [language, setLanguage] = useState<SnippetLanguage>("node");
  const endpoint = endpoints.data?.data[0];
  const run = async (action: WizardAction, onError?: () => void) => {
    if (busy) return;
    setBusy(true);
    setOutcome(null);
    setVerifyFailed(false);
    try {
      await action();
      await cache.invalidateQueries({ queryKey: ["admin", "management-webhooks"] });
    } catch (error) {
      if (isStepUpError(error)) setOutcome("stepUp"); else if (onError) onError(); else setOutcome("failed");
    } finally {
      setBusy(false);
    }
  };
  const create = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (!admin.client || !admin.scope) return;
    const url = String(new FormData(event.currentTarget).get("url") ?? "").trim();
    void run(async () => setSecret(await admin.client!.createWebhookEndpoint(admin.scope!, { url, event_types: WIZARD_WEBHOOK_EVENTS, timeout_ms: 5000, max_concurrency: 4 }, key())));
  };
  const verify = () => endpoint && void run(() => admin.client!.verifyWebhookEndpoint(admin.scope!, endpoint.id, endpoint.version, key()), () => setVerifyFailed(true));
  return <SectionCard description={t("wizard.webhook.body")} title={<StepTitle done={endpoint?.status === "active"} index={3} title="wizard.webhook.title" />}>
    {!endpoint && admin.can("webhook_settings:write") && <form className="admin-management-form" onSubmit={create}>
      <label className="is-wide"><span>{t("webhooks.endpoint")}</span><Input name="url" placeholder={URL_PLACEHOLDER} required type="url" /></label>
      <div className="is-wide"><Button disabled={busy} type="submit">{t("wizard.webhook.create")}</Button></div>
    </form>}
    {secret && <div className="wizard__secret" role="status">
      <p>{t("wizard.webhook.secretOnce")}</p>
      <dl><div><dt>{t("wizard.keyId")}</dt><dd><code>{secret.key_id}</code></dd></div><div><dt>{t("wizard.secret")}</dt><dd><code>{secret.secret}</code></dd></div></dl>
    </div>}
    {endpoint && <div className="wizard__endpoint">
      <span><code>{endpoint.url}</code> <StatusBadge status={endpoint.status === "active" ? "settled" : "pending"}>{t(endpoint.status === "active" ? "wizard.webhook.verified" : "wizard.webhook.unverified")}</StatusBadge></span>
      {endpoint.status === "unverified" && admin.can("webhook_settings:write") && <Button disabled={busy} onClick={verify} variant="secondary">{t("wizard.webhook.verify")}</Button>}
    </div>}
    <p>{t("wizard.webhook.challenge")}</p>
    <pre className="wizard__code">{CHALLENGE_EXAMPLE}</pre>
    {verifyFailed && <p role="alert">{t("wizard.webhook.verifyFailed")}</p>}
    <p>{t("wizard.webhook.signature")}</p>
    <div className="wizard__tabs" role="tablist" aria-label={t("wizard.webhook.signature")}>{SNIPPET_LANGUAGES.map((item) => <button aria-selected={language === item} key={item} onClick={() => setLanguage(item)} role="tab" type="button">{t(`wizard.language.${item}` as MessageKey)}</button>)}</div>
    <pre className="wizard__code" role="tabpanel">{SIGNATURE_SNIPPETS[language]}</pre>
    <OutcomeLine outcome={outcome} />
  </SectionCard>;
}

function TestPaymentStep({ webhookStep }: { webhookStep: boolean }) {
  const { t } = useI18n();
  const admin = useAdmin();
  const links = useAdminQuery("onboarding-payment-links", "payment_links:read", (client, scope) => client.paymentLinks(scope, "", 1));
  const intents = useAdminQuery("onboarding-intents", "payments:read", (client, scope) => client.intents(scope, "", 50));
  const endpoints = useEndpoints();
  const endpoint = endpoints.data?.data[0];
  const deliveries = useQuery<ManagementPage<WebhookDelivery>, Error>({
    queryKey: ["admin", "management-webhook-deliveries", admin.scope?.tenantId, admin.scope?.merchantId, endpoint?.id],
    enabled: !admin.preview && webhookStep && Boolean(admin.scope && endpoint && admin.can("webhook_settings:read")),
    queryFn: ({ signal }) => admin.clientFor(signal).webhookDeliveries(admin.scope!, endpoint!.id, "", 5, signal)
  });
  const linked = (links.data?.data.length ?? 0) > 0;
  const paid = Boolean(intents.data?.items.some((intent) => PAID_STATUSES.has(intent.status)));
  const latest = deliveries.data?.data[0];
  const delivered = latest?.status === "acknowledged";
  const chain: Array<{ key: MessageKey; done: boolean }> = [
    { key: "wizard.test.link", done: linked },
    { key: "wizard.test.paid", done: paid },
    ...(webhookStep ? [{ key: "wizard.test.webhook" as MessageKey, done: delivered }] : [])
  ];
  return <SectionCard description={t("wizard.test.body")} title={<StepTitle done={chain.every((item) => item.done)} index={webhookStep ? 4 : 3} title="wizard.test.title" />}>
    <ol className="wizard__chain">{chain.map((item) => <li data-done={item.done || undefined} key={item.key}>{item.done ? <Check aria-hidden="true" size={14} /> : <span aria-hidden="true">·</span>}{t(item.key)}</li>)}</ol>
    {!linked && <a className="mp-button mp-button--secondary mp-button--md" href="#/payment-links">{t("wizard.method.openLinks")}</a>}
    {latest && <div className="wizard__delivery" role="status">
      <strong>{t("wizard.test.response")}</strong>
      <span>{latest.event_type}</span>
      <span>{httpStatusLabel(latest.last_http_status) ?? t("wizard.test.noResponse")}</span>
      {latest.response_snippet && <pre className="wizard__code">{latest.response_snippet}</pre>}
    </div>}
    <p className="go-live__note">{t("wizard.test.sandbox")}</p>
  </SectionCard>;
}

function GoLiveStep() {
  const { t } = useI18n();
  const state = useGoLiveState();
  const live = state.data?.environment === "live";
  return <SectionCard description={t("wizard.live.body")} title={<StepTitle done={live} index={5} title="wizard.live.title" />}>
    {!live && <a className="mp-button mp-button--primary mp-button--md" href="#/go-live">{t("wizard.live.open")}</a>}
  </SectionCard>;
}
