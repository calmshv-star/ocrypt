import { useI18n } from "@merchant/i18n";
import { Badge, Button, Input, PageHeader, SectionCard, StatusBadge } from "@merchant/ui";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { CircleDollarSign } from "lucide-react";
import { useState } from "react";
import { useAdmin } from "./AdminProvider";
import { AdminAPIError } from "./api/client";
import type { FinancialSettingsRoute, FinancialSettingsWallet } from "./api/types";

function Status({ value }: { value: string }) {
  const { t } = useI18n();
  const active = value === "active" || value === "enabled";
  return <StatusBadge status={active ? "healthy" : "paused"}>{active ? t("financialSettings.active") : t("financialSettings.disabled")}</StatusBadge>;
}

type SaveInput = { wallet:FinancialSettingsWallet;address:string };
type NetworkRow = { chainID:string;chainName:string;wallet?:FinancialSettingsWallet;routes:FinancialSettingsRoute[];placeholder?:boolean };

function networkRows(wallets:FinancialSettingsWallet[],routes:FinancialSettingsRoute[],aptosName:string):NetworkRow[] {
  const rows = new Map<string,NetworkRow>();
  for (const wallet of wallets) rows.set(wallet.chain_id,{chainID:wallet.chain_id,chainName:wallet.chain_name,wallet,routes:[]});
  for (const route of routes) {
    const row = rows.get(route.chain_id) ?? {chainID:route.chain_id,chainName:route.chain_id,routes:[]};
    row.routes.push(route);
    rows.set(route.chain_id,row);
  }
  if (!rows.has("aptos:1")) rows.set("aptos:1",{chainID:"aptos:1",chainName:aptosName,routes:[],placeholder:true});
  return [...rows.values()].sort((left,right)=>{
    const availability = Number(Boolean(right.wallet||right.routes.length))-Number(Boolean(left.wallet||left.routes.length));
    return availability || left.chainName.localeCompare(right.chainName);
  });
}

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
  const networks = networkRows(inventory?.wallets??[],inventory?.routes??[],t("financialSettings.aptosName"));
  const stepUpRequired = save.error instanceof AdminAPIError && save.error.code === "step_up_required";
  return <div className="admin-page" data-testid="financial-settings-page">
    <PageHeader description={t("financialSettings.description")} eyebrow={<><CircleDollarSign size={13}/>{t("common.production")}</>} title={t("financialSettings.title")}/>
    {saved&&<div className="financial-settings-feedback" role="status">{t("financialSettings.walletSaved")}</div>}
    {save.isError&&<div className="financial-settings-feedback financial-settings-feedback--error" role="alert">
      <span>{t(stepUpRequired?"financialSettings.stepUpRequired":"financialSettings.walletSaveError")}</span>
      {stepUpRequired&&admin.client&&<a className="mp-button mp-button--secondary mp-button--sm" href={admin.client.stepUpURL("/admin/#/financial-settings")}>{t("financialSettings.confirmLogin")}</a>}
    </div>}
    <SectionCard description={t("financialSettings.networksBody")} title={t("financialSettings.networksTitle")}>
      <div className="financial-network-list">{networks.map(network => {
        const enabledRoutes = network.routes.filter(route=>route.route_status==="enabled"&&route.asset_status==="active"&&route.chain_status==="active");
        const currencies = [...new Set(enabledRoutes.map(route=>route.currency))];
        const wallet = network.wallet;
        return <article className={`financial-network-row${network.placeholder?" is-unavailable":""}`} key={network.chainID}>
          <header className="financial-network-heading">
            <strong>{network.chainName}</strong>
            {wallet&&enabledRoutes.length>0?<Status value="active"/>:<Badge tone="neutral">{t("financialSettings.notConnected")}</Badge>}
          </header>
          <div className="financial-network-assets">
            <span>{t("financialSettings.acceptedAssets")}</span>
            <div>{enabledRoutes.length>0?enabledRoutes.map(route=><Badge key={route.asset_id} tone="neutral">{route.asset_symbol}</Badge>):<span>{t("financialSettings.noAcceptedAssets")}</span>}</div>
          </div>
          <div className="financial-network-address">
            <span>{t("financialSettings.publicAddress")}</span>
            <strong>{wallet?.address||t("financialSettings.noWalletAddress")}</strong>
          </div>
          <div className="financial-network-meta">
            {currencies.map(currency=><Badge key={currency} tone="neutral">{currency}</Badge>)}
          </div>
          {wallet&&editing!==wallet.id&&<Button disabled={!canEdit||save.isPending} onClick={()=>{setSaved(false);save.reset();setEditing(wallet.id);setAddress(wallet.address);}} size="sm" variant="secondary">{t(wallet.address?"financialSettings.changeWallet":"financialSettings.addWallet")}</Button>}
          {wallet&&editing===wallet.id&&<form className="financial-wallet-form" onSubmit={event=>{event.preventDefault();save.mutate({wallet,address:address.trim()});}}>
            <label><span>{t("financialSettings.publicAddress")}</span><Input autoComplete="off" autoFocus onChange={event=>setAddress(event.target.value)} placeholder={t("financialSettings.publicAddressPlaceholder")} spellCheck={false} value={address}/></label>
            <div><Button disabled={!address.trim()||save.isPending} size="sm" type="submit">{t("common.save")}</Button><Button disabled={save.isPending} onClick={()=>{setEditing("");setAddress("");save.reset();}} size="sm" type="button" variant="secondary">{t("common.cancel")}</Button></div>
          </form>}
        </article>;
      })}</div>
    </SectionCard>
  </div>;
}
