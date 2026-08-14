import { useI18n } from "@merchant/i18n";
import { Badge, Button, Input, PageHeader, SectionCard, StatusBadge } from "@merchant/ui";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { CircleDollarSign } from "lucide-react";
import { useState } from "react";
import { useAdmin } from "./AdminProvider";
import { AdminAPIError } from "./api/client";
import type { FinancialSettingsWallet } from "./api/types";

function Status({ value }: { value: string }) {
  const { t } = useI18n();
  const active = value === "active" || value === "enabled";
  return <StatusBadge status={active ? "healthy" : "paused"}>{active ? t("financialSettings.active") : t("financialSettings.disabled")}</StatusBadge>;
}

type SaveInput = { wallet:FinancialSettingsWallet;address:string };

export function FinancialSettingsPage() {
  const { t } = useI18n();
  const admin = useAdmin();
  const queryClient = useQueryClient();
  const canRead = admin.can("infrastructure:read");
  const canEdit = admin.can("infrastructure:edit");
  const scope = admin.scope;
  const [editing,setEditing] = useState("");
  const [address,setAddress] = useState("");
  const [saved,setSaved] = useState(false);
  const query = useQuery({
    queryKey: ["financial-settings", scope?.tenantId, scope?.merchantId],
    enabled: !admin.preview && admin.sessionState === "ready" && Boolean(scope?.merchantId) && canRead,
    queryFn: ({ signal }) => admin.clientFor(signal).financialSettings(scope!, signal)
  });
  const save = useMutation({
    mutationFn: async ({wallet,address:nextAddress}:SaveInput) => {
      const client = admin.client;
      if (!client || !scope?.merchantId) throw new Error("Admin client is unavailable");
      await client.refreshCSRF();
      return client.replaceWatchWallet(scope!,wallet.id,wallet.chain_id,nextAddress,wallet.version,"Receiving address changed in financial settings",crypto.randomUUID());
    },
    onSuccess: async () => {
      setSaved(true);
      setEditing("");
      setAddress("");
      await queryClient.invalidateQueries({queryKey:["financial-settings",scope?.tenantId,scope?.merchantId]});
    }
  });
  if (!canRead) return <div className="admin-page"><PageHeader description={t("financialSettings.description")} title={t("financialSettings.title")}/><div className="admin-live-state"><strong>{t("admin.permissionTitle")}</strong><p>{t("admin.permissionBody")}</p></div></div>;
  if (!scope?.merchantId) return <div className="admin-page"><PageHeader description={t("financialSettings.description")} title={t("financialSettings.title")}/><div className="admin-live-state"><strong>{t("financialSettings.merchantScope")}</strong></div></div>;
  if (query.isLoading) return <div className="admin-page"><PageHeader description={t("financialSettings.description")} title={t("financialSettings.title")}/><div aria-busy="true" className="admin-live-state"><strong>{t("admin.sessionLoading")}</strong></div></div>;
  if (query.error) return <div className="admin-page"><PageHeader description={t("financialSettings.description")} title={t("financialSettings.title")}/><div className="admin-live-state" role="alert"><strong>{t("admin.dataError")}</strong><p>{t("admin.dataErrorBody")}</p></div></div>;
  const inventory = query.data;
  const stepUpRequired = save.error instanceof AdminAPIError && save.error.code === "step_up_required";
  return <div className="admin-page" data-testid="financial-settings-page">
    <PageHeader description={t("financialSettings.description")} eyebrow={<><CircleDollarSign size={13}/>{t("common.production")}</>} title={t("financialSettings.title")}/>
    {saved&&<div className="financial-settings-feedback" role="status">{t("financialSettings.walletSaved")}</div>}
    {save.isError&&<div className="financial-settings-feedback financial-settings-feedback--error" role="alert">
      <span>{t(stepUpRequired?"financialSettings.stepUpRequired":"financialSettings.walletSaveError")}</span>
      {stepUpRequired&&admin.client&&<a className="mp-button mp-button--secondary mp-button--sm" href={admin.client.stepUpURL("/admin/#/financial-settings")}>{t("financialSettings.confirmLogin")}</a>}
    </div>}
    <SectionCard description={t("financialSettings.walletsBody")} title={t("financialSettings.walletsTitle")}>
      <div className="financial-wallet-list">{inventory?.wallets.map(wallet => <article className="financial-wallet-row" key={wallet.id}>
        <div className="financial-wallet-main"><strong>{wallet.chain_name}</strong><span>{wallet.address || t("financialSettings.noWalletAddress")}</span></div>
        {editing!==wallet.id&&<Button disabled={!canEdit||save.isPending} onClick={()=>{setSaved(false);save.reset();setEditing(wallet.id);setAddress(wallet.address);}} size="sm" variant="secondary">{t(wallet.address?"financialSettings.changeWallet":"financialSettings.addWallet")}</Button>}
        {editing===wallet.id&&<form className="financial-wallet-form" onSubmit={event=>{event.preventDefault();save.mutate({wallet,address:address.trim()});}}>
          <label><span>{t("financialSettings.publicAddress")}</span><Input autoComplete="off" autoFocus onChange={event=>setAddress(event.target.value)} placeholder={t("financialSettings.publicAddressPlaceholder")} spellCheck={false} value={address}/></label>
          <div><Button disabled={!address.trim()||save.isPending} size="sm" type="submit">{t("common.save")}</Button><Button disabled={save.isPending} onClick={()=>{setEditing("");setAddress("");save.reset();}} size="sm" type="button" variant="secondary">{t("common.cancel")}</Button></div>
        </form>}
      </article>)}</div>
      {(inventory?.wallets.length??0)===0&&<div className="admin-live-state"><strong>{t("financialSettings.noWallets")}</strong></div>}
    </SectionCard>
    <SectionCard description={t("financialSettings.routesBody")} title={t("financialSettings.routesTitle")}>
      <div className="financial-settings-routes">{inventory?.routes.map(route => <article key={`${route.currency}:${route.chain_id}:${route.asset_id}`}>
        <header><div><strong>{route.asset_symbol}</strong><small>{route.chain_id} · {route.asset_id}</small></div><Status value={route.route_status}/></header>
        <dl><div><dt>{t("financialSettings.fiat")}</dt><dd><Badge tone="neutral">{route.currency}</Badge></dd></div><div><dt>{t("financialSettings.asset")}</dt><dd><Status value={route.asset_status}/></dd></div><div><dt>{t("financialSettings.network")}</dt><dd><Status value={route.chain_status}/></dd></div><div><dt>{t("financialSettings.addressCapacity")}</dt><dd>{t("financialSettings.addressCapacityValue", { available: route.usable_address_count, total: route.address_count })}</dd></div></dl>
      </article>)}</div>
      {(inventory?.routes.length ?? 0) === 0 && <div className="admin-live-state"><strong>{t("financialSettings.empty")}</strong></div>}
    </SectionCard>
  </div>;
}
