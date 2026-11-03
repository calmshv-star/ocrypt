import { useI18n, type MessageKey } from "@merchant/i18n";
import { Badge, Button, PageHeader, SectionCard } from "@merchant/ui";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { LockKeyhole, LogOut, RefreshCw } from "lucide-react";
import { useState } from "react";
import { useAdmin } from "./AdminProvider";
import type { SessionSummary } from "./api/types";
import { deviceName, signInMethods } from "./security";

const SESSIONS_KEY = ["admin", "my-sessions"];

// The signed-in user's own sessions and how this one was confirmed. Passkeys
// and second factors live at the identity provider, not here.
export function SecurityPage() {
  const { t, locale } = useI18n();
  const admin = useAdmin();
  const cache = useQueryClient();
  const sessions = useQuery<{ data: SessionSummary[] }, Error>({
    queryKey: SESSIONS_KEY,
    enabled: !admin.preview && admin.sessionState === "ready",
    queryFn: ({ signal }) => admin.clientFor(signal).mySessions(signal)
  });
  const [busy, setBusy] = useState<string | null>(null);
  const [outcome, setOutcome] = useState<{ kind: "ended" | "endedOthers" | "failed"; count?: number } | null>(null);
  const rows = sessions.data?.data ?? [];
  const others = rows.filter((session) => !session.current);
  const methods = signInMethods(admin.principal?.amr ?? []);
  const when = (value: string) => { const time = Date.parse(value); return Number.isFinite(time) ? new Intl.DateTimeFormat(locale, { dateStyle: "medium", timeStyle: "short" }).format(time) : value; };

  const end = async (id: string | "others") => {
    if (!admin.client || busy) return;
    setBusy(id);
    setOutcome(null);
    try {
      if (id === "others") setOutcome({ kind: "endedOthers", count: (await admin.client.revokeOtherSessions()).revoked });
      else { await admin.client.revokeSession(id); setOutcome({ kind: "ended" }); }
    } catch {
      setOutcome({ kind: "failed" });
    } finally {
      setBusy(null);
      await cache.invalidateQueries({ queryKey: SESSIONS_KEY });
    }
  };

  return <div className="admin-page" data-testid="security-page">
    <PageHeader actions={<Button onClick={() => void sessions.refetch()} variant="secondary"><RefreshCw size={15} />{t("common.refresh")}</Button>} description={t("security.body")} eyebrow={<><LockKeyhole size={13} />{t("common.account")}</>} title={t("security.nav")} />
    <SectionCard description={t("security.idpNote")} title={t("security.signInTitle")}>
      {methods.length > 0 ? <><p>{t("security.signInBody")}</p><ul className="security__methods">{methods.map((method) => <li key={method}><Badge tone={method === "pwd" || method === "other" ? "neutral" : "positive"}>{t(`security.amr.${method}` as MessageKey)}</Badge></li>)}</ul></> : <p>{t("security.amr.none")}</p>}
    </SectionCard>
    <SectionCard action={others.length > 0 && <Button disabled={busy !== null} onClick={() => void end("others")} variant="secondary"><LogOut size={15} />{t("security.endOthers")}</Button>} description={t("security.sessionsBody")} title={t("security.sessionsTitle")}>
      {sessions.isLoading && <div aria-busy="true" className="admin-route-loading"><span /><span /><span /></div>}
      {sessions.error && <p className="admin-live-state" role="alert">{t("admin.dataError")}</p>}
      {outcome && <p className="security__outcome" role={outcome.kind === "failed" ? "alert" : "status"}>{outcome.kind === "endedOthers" ? t("security.endedOthers", { count: outcome.count ?? 0 }) : t(outcome.kind === "ended" ? "security.ended" : "security.failed")}</p>}
      <ul className="security__sessions">{rows.map((session) => <li key={session.id}>
        <div>
          <strong>{deviceName(session.device, t)}</strong>{session.current && <Badge tone="positive">{t("security.current")}</Badge>}
          <small>{t("security.signedIn", { time: when(session.created_at) })} · {t("security.lastSeen", { time: when(session.last_seen_at) })} · {t("security.expires", { time: when(session.expires_at) })}</small>
        </div>
        {!session.current && <Button disabled={busy !== null} onClick={() => void end(session.id)} variant="secondary">{t("security.end")}</Button>}
      </li>)}</ul>
      {sessions.isSuccess && others.length === 0 && <p className="security__outcome">{t("security.noOthers")}</p>}
    </SectionCard>
  </div>;
}
