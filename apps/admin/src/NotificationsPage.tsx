import { useI18n, type MessageKey } from "@merchant/i18n";
import { Badge, Button, PageHeader, SectionCard } from "@merchant/ui";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { BellRing, Send } from "lucide-react";
import { useEffect, useState } from "react";
import { useAdmin } from "./AdminProvider";
import type { NotificationEvent, NotificationSettings } from "./api/types";

const EVENTS: NotificationEvent[] = ["paid", "underpaid", "review", "webhook_failed"];
const SETTINGS_KEY = ["admin", "notifications"];

type Outcome = "saved" | "failed" | "linkFailed" | null;

// Which events reach the user by email or Telegram. Account alerts apply to
// every user; merchant events need payment access in the current scope.
export function NotificationsPage() {
  const { t, locale } = useI18n();
  const admin = useAdmin();
  const cache = useQueryClient();
  const scope = admin.scope?.merchantId ? admin.scope : undefined;
  const settings = useQuery<NotificationSettings, Error>({
    queryKey: [...SETTINGS_KEY, scope?.tenantId ?? "", scope?.merchantId ?? ""],
    enabled: !admin.preview && admin.sessionState === "ready",
    queryFn: ({ signal }) => admin.clientFor(signal).notificationSettings(scope, signal)
  });
  const [email, setEmail] = useState<Set<NotificationEvent>>(new Set());
  const [telegram, setTelegram] = useState<Set<NotificationEvent>>(new Set());
  const [signIn, setSignIn] = useState({ email: true, telegram: true });
  const [busy, setBusy] = useState(false);
  const [outcome, setOutcome] = useState<{ section: "events" | "account" | "telegram"; kind: Outcome }>({ section: "events", kind: null });
  const [link, setLink] = useState<{ url: string; expires_at: string } | null>(null);
  const current = settings.data;
  // A one-time link lasts ten minutes; afterwards offer a new one.
  useEffect(() => {
    if (!link) return;
    const timer = window.setTimeout(() => setLink(null), Math.max(Date.parse(link.expires_at) - Date.now(), 0));
    return () => window.clearTimeout(timer);
  }, [link]);
  useEffect(() => {
    if (!current) return;
    setEmail(new Set(current.merchant?.email_events ?? []));
    setTelegram(new Set(current.merchant?.telegram_events ?? []));
    setSignIn({ email: current.account.sign_in_email, telegram: current.account.sign_in_telegram });
  }, [current]);

  const run = async (section: "events" | "account" | "telegram", action: () => Promise<unknown>, failure: Outcome = "failed") => {
    if (busy) return;
    setBusy(true);
    setOutcome({ section, kind: null });
    try {
      await action();
      setOutcome({ section, kind: section === "telegram" ? null : "saved" });
    } catch {
      setOutcome({ section, kind: failure });
    } finally {
      setBusy(false);
      await cache.invalidateQueries({ queryKey: SETTINGS_KEY });
    }
  };
  // Events the user can no longer receive (access or the bot gone) are
  // dropped on save, so the form never becomes unsavable.
  const allowed = (event: NotificationEvent) => event !== "webhook_failed" || Boolean(current?.merchant?.webhooks_allowed);
  const saveEvents = () => run("events", () => admin.client!.saveMerchantNotifications(scope!, EVENTS.filter((event) => email.has(event) && allowed(event)), current?.telegram.available ? EVENTS.filter((event) => telegram.has(event) && allowed(event)) : [], locale));
  const saveAccount = () => run("account", () => admin.client!.saveAccountNotifications(scope, signIn.email, signIn.telegram, locale));
  const connect = () => run("telegram", async () => setLink(await admin.client!.createTelegramLink()), "linkFailed");
  const disconnect = () => run("telegram", async () => { await admin.client!.unlinkTelegram(); setLink(null); });
  const toggle = (set: Set<NotificationEvent>, update: (next: Set<NotificationEvent>) => void, event: NotificationEvent) => {
    const next = new Set(set);
    if (next.has(event)) next.delete(event); else next.add(event);
    update(next);
  };
  const message = (section: "events" | "account" | "telegram") => outcome.section === section && outcome.kind && <p className="security__outcome" role={outcome.kind === "saved" ? "status" : "alert"}>{t(`notifications.${outcome.kind}` as MessageKey)}</p>;
  const telegramReady = Boolean(current?.telegram.available && current.telegram.linked);

  return <div className="admin-page" data-testid="notifications-page">
    <PageHeader description={t("notifications.body")} eyebrow={<><BellRing size={13} />{t("common.account")}</>} title={t("notifications.nav")} />
    {settings.isLoading && <div aria-busy="true" className="admin-route-loading"><span /><span /><span /></div>}
    {settings.error && <p className="admin-live-state" role="alert">{t("admin.dataError")}</p>}
    {current && <>
      <SectionCard title={t("notifications.channelsTitle")}>
        <ul className="security__sessions">
          <li><div><strong>{t("notifications.email")}</strong><small>{t("notifications.emailTo", { email: current.email || "—" })}</small></div></li>
          <li>
            <div>
              <strong>{t("notifications.telegram")}{current.telegram.available && <Badge tone={current.telegram.linked ? "positive" : "neutral"}>{t(current.telegram.linked ? "notifications.telegramLinked" : "notifications.telegramNotLinked")}</Badge>}</strong>
              {!current.telegram.available && <small>{t("notifications.telegramUnavailable")}</small>}
              {link && !current.telegram.linked && <small>{t("notifications.telegramOpen")} <a href={link.url} rel="noopener noreferrer" target="_blank">{t("notifications.telegramOpenLink")}</a></small>}
            </div>
            {current.telegram.available && !current.telegram.linked && (link
              ? <Button disabled={busy} onClick={() => void settings.refetch()} variant="secondary">{t("notifications.telegramCheck")}</Button>
              : <Button disabled={busy} onClick={() => void connect()} variant="secondary"><Send size={15} />{t("notifications.telegramConnect")}</Button>)}
            {current.telegram.linked && <Button disabled={busy} onClick={() => void disconnect()} variant="secondary">{t("notifications.telegramDisconnect")}</Button>}
          </li>
        </ul>
        {message("telegram")}
      </SectionCard>
      {current.merchant && <SectionCard description={t("notifications.eventsBody")} title={t("notifications.eventsTitle")}>
        {!current.merchant.configured && <p className="go-live__note">{t("notifications.defaults")}</p>}
        <table className="notifications__table">
          <thead><tr><th scope="col">{t("notifications.event")}</th><th scope="col">{t("notifications.email")}</th><th scope="col">{t("notifications.telegram")}</th></tr></thead>
          <tbody>{EVENTS.map((event) => {
            const name = t(`notifications.event.${event}` as MessageKey);
            const locked = event === "webhook_failed" && !current.merchant!.webhooks_allowed;
            return <tr key={event}>
              <th scope="row">{name}{locked && <small>{t("notifications.webhookNoAccess")}</small>}</th>
              <td><input aria-label={`${name}: ${t("notifications.email")}`} checked={email.has(event)} disabled={locked || busy} onChange={() => toggle(email, setEmail, event)} type="checkbox" /></td>
              <td><input aria-label={`${name}: ${t("notifications.telegram")}`} checked={telegram.has(event)} disabled={locked || busy || !current.telegram.available} onChange={() => toggle(telegram, setTelegram, event)} type="checkbox" /></td>
            </tr>;
          })}</tbody>
        </table>
        {current.telegram.available && !telegramReady && telegram.size > 0 && <p className="go-live__note">{t("notifications.telegramNeedsLink")}</p>}
        <div className="go-live__submit"><Button disabled={busy} onClick={() => void saveEvents()}>{t("notifications.save")}</Button></div>
        {message("events")}
      </SectionCard>}
      <SectionCard description={t("notifications.accountBody")} title={t("notifications.accountTitle")}>
        <label className="notifications__switch"><input checked={signIn.email} disabled={busy} onChange={() => setSignIn({ ...signIn, email: !signIn.email })} type="checkbox" />{t("notifications.signInEmail")}</label>
        <label className="notifications__switch"><input checked={signIn.telegram} disabled={busy || !current.telegram.available} onChange={() => setSignIn({ ...signIn, telegram: !signIn.telegram })} type="checkbox" />{t("notifications.signInTelegram")}</label>
        <div className="go-live__submit"><Button disabled={busy} onClick={() => void saveAccount()}>{t("notifications.save")}</Button></div>
        {message("account")}
      </SectionCard>
    </>}
  </div>;
}
