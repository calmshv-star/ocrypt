import { useI18n } from "@merchant/i18n";
import { Button, PageHeader, SectionCard, Select } from "@merchant/ui";
import { Save, Settings2, ShieldCheck } from "lucide-react";

export function SettingsPage() {
  const { t } = useI18n();
  return <div className="admin-page">
    <PageHeader actions={<Button disabled><Save size={15} />{t("settings.save")}</Button>} description={t("page.settings.description")} eyebrow={<><Settings2 size={13} />{t("common.previewData")}</>} title={t("page.settings.title")} />
    <div className="reconciliation-grid">
      <SectionCard title={t("settings.finalityPolicy")}><Select aria-label={t("settings.finalityPolicy")} defaultValue="economic"><option value="economic">{t("settings.economicFinality")}</option><option value="safe">{t("settings.safeConfirmations")}</option></Select></SectionCard>
      <SectionCard title={t("settings.paymentTolerance")}><Select aria-label={t("settings.paymentTolerance")} defaultValue="exact"><option value="exact">{t("settings.exactAmount")}</option><option value="review">{t("settings.manualReviewThreshold")}</option></Select></SectionCard>
      <SectionCard title={t("settings.webhookPolicy")}><Select aria-label={t("settings.webhookPolicy")} defaultValue="durable"><option value="durable">{t("settings.durableRecovery")}</option><option value="strict">{t("settings.strictAcknowledgement")}</option></Select></SectionCard>
    </div>
    <div className="webhook-security-strip"><span><ShieldCheck size={17} /></span><div><strong>{t("settings.tenantDefaults")}</strong><p>{t("settings.dualControl")}</p></div></div>
  </div>;
}
