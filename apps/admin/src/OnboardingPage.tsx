import { useI18n, type MessageKey } from "@merchant/i18n";
import { PageHeader, StatusBadge } from "@merchant/ui";
import { useQuery } from "@tanstack/react-query";
import { ArrowRight, CircleDollarSign, KeyRound, Link2, Rocket, Sparkles, Wallet, Webhook } from "lucide-react";
import type { ComponentType } from "react";
import { useAdmin, useAdminQuery } from "./AdminProvider";
import type { AdminScope } from "./api/types";

// Intent statuses that mean the customer's money arrived and is final
// (overpaid is reached only from settled).
const PAID_STATUSES = new Set(["settled", "overpaid"]);

type Step = { key: string; href: string; icon: ComponentType<{ size?: number; "aria-hidden"?: boolean }>; done: boolean; loading: boolean };

export function useOnboardingSteps() {
  const admin = useAdmin();
  // Onboarding is the merchant owner's checklist; platform operators reach
  // wallets through infrastructure pages instead.
  const canWallets = admin.can("wallets:read");
  const wallets = useQuery({
    queryKey: ["admin", "onboarding-wallets", admin.scope?.tenantId, admin.scope?.merchantId],
    enabled: !admin.preview && admin.sessionState === "ready" && Boolean(admin.scope?.merchantId) && canWallets,
    queryFn: ({ signal }) => admin.clientFor(signal).financialSettings(admin.scope as AdminScope, signal)
  });
  const apiClients = useAdminQuery("onboarding-api-clients", "api_clients:read", (client, scope) => client.apiClients(scope, "", 1));
  const webhooks = useAdminQuery("onboarding-webhooks", "webhook_settings:read", (client, scope) => client.webhookEndpoints(scope, "", 1));
  const links = useAdminQuery("onboarding-payment-links", "payment_links:read", (client, scope) => client.paymentLinks(scope, "", 1));
  const intents = useAdminQuery("onboarding-intents", "payments:read", (client, scope) => client.intents(scope, "", 50));
  const walletReady = Boolean(wallets.data?.wallets.some((wallet) => wallet.status === "active"));
  const paid = Boolean(intents.data?.items.some((intent) => PAID_STATUSES.has(intent.status)));
  const steps = ([
    canWallets && { key: "wallets", href: "#/financial-settings", icon: Wallet, done: walletReady, loading: wallets.isLoading },
    admin.can("api_clients:read") && { key: "apiKeys", href: "#/api-clients", icon: KeyRound, done: (apiClients.data?.data.length ?? 0) > 0, loading: apiClients.isLoading },
    admin.can("webhook_settings:read") && { key: "webhooks", href: "#/webhooks", icon: Webhook, done: (webhooks.data?.data.length ?? 0) > 0, loading: webhooks.isLoading },
    admin.can("payment_links:read") && { key: "paymentLink", href: "#/payment-links", icon: Link2, done: (links.data?.data.length ?? 0) > 0, loading: links.isLoading },
    admin.can("payments:read") && { key: "testPayment", href: "#/intents", icon: CircleDollarSign, done: paid, loading: intents.isLoading },
    canWallets && admin.can("payments:read") && { key: "goLive", href: "#/settings", icon: Rocket, done: walletReady && paid, loading: wallets.isLoading || intents.isLoading }
  ].filter(Boolean) as Step[]);
  return { steps, done: steps.filter((step) => step.done).length, loading: steps.some((step) => step.loading) };
}

// Shown on the overview until every visible step is done.
export function OnboardingBanner() {
  const { t } = useI18n();
  const { steps, done, loading } = useOnboardingSteps();
  if (loading || steps.length === 0 || done === steps.length) return null;
  return <a className="onboarding-banner" href="#/onboarding">
    <Sparkles aria-hidden={true} size={18} />
    <span><strong>{t("onboarding.title")}</strong><small>{t("onboarding.progress", { done, total: steps.length })}</small></span>
    <progress aria-hidden={true} max={steps.length} value={done} />
    <ArrowRight aria-hidden={true} size={16} />
  </a>;
}

export function OnboardingPage() {
  const { t } = useI18n();
  const { steps, done } = useOnboardingSteps();
  return <div className="admin-page" data-testid="onboarding-page">
    <PageHeader description={t("onboarding.body")} eyebrow={<><Sparkles size={13} />{t("onboarding.nav")}</>} title={t("onboarding.title")} />
    <p aria-live="polite" className="onboarding__progress"><progress max={steps.length} value={done} /><span>{t("onboarding.progress", { done, total: steps.length })}</span></p>
    <ol className="onboarding__steps">{steps.map((step) => {
      const Icon = step.icon;
      return <li key={step.key}><a className="settings-hub__link" data-done={step.done || undefined} href={step.href}>
        <span className="settings-hub__icon"><Icon aria-hidden={true} size={18} /></span>
        <span><strong>{t(`onboarding.${step.key}.title` as MessageKey)}</strong><small>{t(`onboarding.${step.key}.body` as MessageKey)}</small></span>
        <span className="onboarding__state">{!step.loading && <StatusBadge status={step.done ? "settled" : "pending"}>{t(step.done ? step.key === "goLive" ? "onboarding.ready" : "onboarding.done" : "onboarding.todo")}</StatusBadge>}<ArrowRight aria-label={t("onboarding.open")} size={16} /></span>
      </a></li>;
    })}</ol>
  </div>;
}
