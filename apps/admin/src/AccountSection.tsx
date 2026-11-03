import { useI18n } from "@merchant/i18n";
import { Button, SectionCard } from "@merchant/ui";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Download, ExternalLink, Trash2 } from "lucide-react";
import { useState } from "react";
import { useAdmin } from "./AdminProvider";
import { AdminAPIError } from "./api/client";
import { downloadJSON, sameEmail, type DeletionOutcome } from "./account";

// REG-5 in the cabinet: the provider manages credentials and the email; the
// user can download their data and delete their account here.
export function AccountSection() {
  const { t } = useI18n();
  const admin = useAdmin();
  const cache = useQueryClient();
  const links = useQuery({ queryKey: ["account-links"], enabled: Boolean(admin.client), queryFn: ({ signal }) => admin.client!.accountLinks(signal), staleTime: 60_000 });
  const deletion = useQuery({ queryKey: ["admin", "account-deletion"], enabled: !admin.preview && admin.sessionState === "ready", queryFn: ({ signal }) => admin.clientFor(signal).accountDeletion(signal) });
  const [exporting, setExporting] = useState<"busy" | "failed" | null>(null);
  const [confirm, setConfirm] = useState("");
  const [closeMerchants, setCloseMerchants] = useState(false);
  const [deleting, setDeleting] = useState(false);
  const [outcome, setOutcome] = useState<DeletionOutcome>(null);
  const email = admin.principal?.email ?? "";
  const owned = deletion.data?.sole_owned_merchants ?? [];
  const ready = sameEmail(confirm, email) && (owned.length === 0 || closeMerchants) && !deleting;

  const exportData = async () => {
    if (!admin.client || exporting === "busy") return;
    setExporting("busy");
    try {
      downloadJSON(`ocrypt-account-${new Date().toISOString().slice(0, 10)}.json`, await admin.client.accountExport());
      setExporting(null);
    } catch {
      setExporting("failed");
    }
  };
  const remove = async () => {
    if (!admin.client || !ready) return;
    setDeleting(true);
    setOutcome(null);
    try {
      await admin.client.deleteAccount(confirm.trim(), closeMerchants);
      cache.clear();
      window.location.assign("/");
    } catch (error) {
      const code = error instanceof AdminAPIError ? error.code : "";
      setOutcome(code === "step_up_required" ? "stepUp" : code === "owner_of_merchants" ? "ownerConflict" : "failed");
      setDeleting(false);
      void deletion.refetch();
    }
  };

  return <>
    <SectionCard description={t("account.body")} title={t("account.title")}>
      <div className="account__actions">
        {links.data?.accountURL && <a className="mp-button mp-button--secondary mp-button--md" href={links.data.accountURL} rel="noopener noreferrer" target="_blank"><ExternalLink size={15} />{t("account.manage")}</a>}
        <Button disabled={exporting === "busy"} onClick={() => void exportData()} variant="secondary"><Download size={15} />{t("account.export")}</Button>
      </div>
      <p className="go-live__note">{t("account.exportBody")}</p>
      {exporting === "failed" && <p className="security__outcome" role="alert">{t("account.exportFailed")}</p>}
    </SectionCard>
    <SectionCard description={t("account.deleteBody")} title={t("account.deleteTitle")}>
      {deletion.data?.platform_operator && <p className="admin-live-state">{t("account.operator")}</p>}
      {deletion.data && !deletion.data.platform_operator && <div className="account__delete">
        {owned.length > 0 && <>
          <p>{t("account.soleOwned")}</p>
          <ul>{owned.map((merchant) => <li key={merchant.merchant_id}>{merchant.display_name}</li>)}</ul>
          <label className="notifications__switch"><input checked={closeMerchants} onChange={() => setCloseMerchants(!closeMerchants)} type="checkbox" />{t("account.closeConfirm")}</label>
        </>}
        <label>{t("account.confirmEmail")}<input autoComplete="off" onChange={(event) => setConfirm(event.target.value)} placeholder={email} spellCheck={false} type="email" value={confirm} /></label>
        <div className="go-live__submit"><Button disabled={!ready} onClick={() => void remove()} variant="danger"><Trash2 size={15} />{t(deleting ? "account.deleting" : "account.delete")}</Button></div>
        {outcome === "stepUp" && admin.client && <p role="alert">{t("signupReviews.stepUpBody")} <a href={admin.client.stepUpURL("/#/security")}>{t("signupReviews.stepUp")}</a></p>}
        {outcome === "ownerConflict" && <p role="alert">{t("account.ownerConflict")}</p>}
        {outcome === "failed" && <p role="alert">{t("account.failed")}</p>}
      </div>}
    </SectionCard>
  </>;
}
