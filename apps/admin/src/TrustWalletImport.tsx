import { useI18n } from "@merchant/i18n";
import { Button } from "@merchant/ui";
import { Check, ExternalLink, ShieldCheck, Smartphone, Wallet, X } from "lucide-react";
import { useRef, useState } from "react";
import type { AdminScope, FinancialSettingsWallet, WatchWalletImportItem } from "./api/types";
import { AdminAPIError, type AdminClient } from "./api/client";
import {
  connectInjectedTrustEVM,
  connectInjectedTrustSolana,
  connectMobileTrustEVM,
  trustWalletDeepLink,
  trustWalletDownloadURL,
  walletConnectProjectId,
  walletConnectQRCode,
  type TrustWalletConnection
} from "./trust-wallet";

type Props = {
  canEdit:boolean;
  client:AdminClient|null;
  scope:AdminScope;
  wallets:FinancialSettingsWallet[];
  onImported():Promise<void>;
};

type Phase = "closed"|"connecting"|"pairing"|"ready"|"signing"|"done"|"error";
const solanaMainnet="solana:mainnet";

function shortAddress(address:string):string {
  return address.length>20?`${address.slice(0,10)}…${address.slice(-8)}`:address;
}

function itemsFor(wallets:FinancialSettingsWallet[],address:string):WatchWalletImportItem[] {
  return wallets.map(wallet=>({wallet_id:wallet.id,chain_id:wallet.chain_id,address,version:wallet.version}));
}

export function TrustWalletImport({canEdit,client,scope,wallets,onImported}:Props) {
  const { t }=useI18n();
  const attempt=useRef(0);
  const [phase,setPhase]=useState<Phase>("closed");
  const [family,setFamily]=useState<"evm"|"solana">("evm");
  const [connection,setConnection]=useState<TrustWalletConnection|null>(null);
  const [available,setAvailable]=useState<FinancialSettingsWallet[]>([]);
  const [selected,setSelected]=useState<Set<string>>(new Set());
  const [uri,setURI]=useState("");
  const [qr,setQR]=useState("");
  const [error,setError]=useState("");
  const [stepUp,setStepUp]=useState(false);

  const close=async()=>{
    attempt.current++;
    if(connection)await connection.disconnect();
    setConnection(null);setAvailable([]);setSelected(new Set());setURI("");setQR("");setError("");setStepUp(false);setPhase("closed");
  };

  const ready=(next:TrustWalletConnection,nextWallets:FinancialSettingsWallet[])=>{
    const changed=nextWallets.filter(wallet=>wallet.address.toLowerCase()!==next.address.toLowerCase());
    setConnection(next);setAvailable(nextWallets);setSelected(new Set((changed.length?changed:nextWallets).map(wallet=>wallet.id)));setPhase("ready");
  };

  const beginEVM=async()=>{
    const current=++attempt.current;
    setFamily("evm");
    setPhase("connecting");setError("");setStepUp(false);setURI("");setQR("");
    const evmWallets=wallets.filter(wallet=>wallet.chain_id.startsWith("eip155:")&&wallet.status==="active");
    if(evmWallets.length===0){setError("no_supported_networks");setPhase("error");return}
    try{
      let next=await connectInjectedTrustEVM();
      if(!next){
        const projectId=walletConnectProjectId();
        if(!projectId){setError("walletconnect_not_configured");setPhase("error");return}
        setPhase("pairing");
        next=await connectMobileTrustEVM(projectId,evmWallets.map(wallet=>wallet.chain_id),pairingURI=>{
          if(current!==attempt.current)return;
          setURI(pairingURI);
          void walletConnectQRCode(pairingURI).then(value=>{if(current===attempt.current)setQR(value)});
        });
      }
      if(current===attempt.current)ready(next,evmWallets);
    }catch(cause){
      if(current!==attempt.current)return;
      setError(cause instanceof Error?cause.message:"wallet_connection_failed");setPhase("error");
    }
  };

  const beginSolana=async()=>{
    const current=++attempt.current;
    setFamily("solana");
    setPhase("connecting");setError("");setStepUp(false);
    const solana=wallets.filter(wallet=>wallet.chain_id==="solana:mainnet"&&wallet.status==="active");
    if(solana.length===0){setError("no_supported_networks");setPhase("error");return}
    try{
      const next=await connectInjectedTrustSolana();
      if(!next){setError("solana_in_app_required");setPhase("error");return}
      if(current===attempt.current)ready(next,solana);
    }catch(cause){if(current===attempt.current){setError(cause instanceof Error?cause.message:"wallet_connection_failed");setPhase("error")}}
  };

  const submit=async()=>{
    if(!client||!connection||selected.size===0)return;
    const chosen=available.filter(wallet=>selected.has(wallet.id));
    setPhase("signing");setError("");setStepUp(false);
    try{
      await client.refreshCSRF();
      const challenge=await client.createWatchWalletImportChallenge(scope,connection.kind,connection.address,itemsFor(chosen,connection.address));
      const signature=await connection.sign(challenge.message);
      await client.importWatchWallets(scope,challenge,signature,"Imported from Trust Wallet after ownership verification");
      await onImported();
      setPhase("done");
    }catch(cause){
      setStepUp(cause instanceof AdminAPIError&&cause.code==="step_up_required");
      setError(cause instanceof Error?cause.message:"wallet_import_failed");setPhase("error");
    }
  };

  const toggle=(id:string)=>setSelected(current=>{const next=new Set(current);if(next.has(id))next.delete(id);else next.add(id);return next});
  const errorKey=stepUp?"financialSettings.stepUpRequired":error==="walletconnect_not_configured"?"trustWallet.mobileNotConfigured":error==="solana_in_app_required"?"trustWallet.solanaInApp":error==="no_supported_networks"?"trustWallet.noNetworks":"trustWallet.connectionError";
  const hasSolana=wallets.some(wallet=>wallet.chain_id===solanaMainnet);

  return <>
    <div className="trust-wallet-entry">
      <div className="trust-wallet-entry__icon"><Wallet size={22}/></div>
      <div><strong>{t("trustWallet.title")}</strong><span>{t("trustWallet.body")}</span></div>
      <Button disabled={!canEdit||!client||phase!=="closed"} onClick={()=>void beginEVM()} size="sm"><Wallet size={16}/>{t("trustWallet.connect")}</Button>
      {hasSolana&&<Button disabled={!canEdit||!client||phase!=="closed"} onClick={()=>void beginSolana()} size="sm" variant="secondary">{t("trustWallet.solana")}</Button>}
    </div>
    {phase!=="closed"&&<div aria-label={t("trustWallet.dialogTitle")} aria-modal="true" className="trust-wallet-overlay" role="dialog">
      <div className="trust-wallet-dialog">
        <button aria-label={t("common.close")} className="trust-wallet-dialog__close" disabled={phase==="signing"} onClick={()=>void close()} type="button"><X size={20}/></button>
        {(phase==="connecting"||phase==="pairing")&&<div className="trust-wallet-dialog__center">
          <div className="trust-wallet-dialog__mark"><Smartphone size={26}/></div>
          <h2>{t(phase==="pairing"?"trustWallet.scanTitle":"trustWallet.connecting")}</h2>
          <p>{t(phase==="pairing"?"trustWallet.scanBody":"trustWallet.connectingBody")}</p>
          {phase==="pairing"&&<>{qr?<img alt={t("trustWallet.qrAlt")} className="trust-wallet-qr" src={qr}/>:<div aria-busy="true" className="trust-wallet-qr-placeholder"/>}{uri&&<a className="mp-button mp-button--secondary mp-button--sm" href={trustWalletDeepLink(uri)} rel="noreferrer">{t("trustWallet.openMobile")}<ExternalLink size={15}/></a>}</>}
        </div>}
        {phase==="ready"&&connection&&<>
          <div className="trust-wallet-dialog__heading"><div className="trust-wallet-dialog__mark"><ShieldCheck size={26}/></div><div><h2>{t("trustWallet.chooseTitle")}</h2><p>{t("trustWallet.chooseBody")}</p></div></div>
          <div className="trust-wallet-address"><span>{t("trustWallet.publicAddress")}</span><strong title={connection.address}>{shortAddress(connection.address)}</strong></div>
          <div className="trust-wallet-network-list">{available.map(wallet=><label key={wallet.id} className="trust-wallet-network">
            <input checked={selected.has(wallet.id)} onChange={()=>toggle(wallet.id)} type="checkbox"/>
            <span><strong>{wallet.chain_name}</strong><small>{wallet.address.toLowerCase()===connection.address.toLowerCase()?t("trustWallet.alreadyUsed"):t("trustWallet.willReplace")}</small></span>
          </label>)}</div>
          <div className="trust-wallet-actions"><Button disabled={selected.size===0} onClick={()=>void submit()}><Check size={16}/>{t("trustWallet.verifyImport")}</Button><Button onClick={()=>void close()} variant="secondary">{t("common.cancel")}</Button></div>
          <p className="trust-wallet-safety">{t("trustWallet.safety")}</p>
        </>}
        {phase==="signing"&&<div className="trust-wallet-dialog__center"><div className="trust-wallet-dialog__mark"><ShieldCheck size={26}/></div><h2>{t("trustWallet.confirmTitle")}</h2><p>{t("trustWallet.confirmBody")}</p></div>}
        {phase==="done"&&<div className="trust-wallet-dialog__center"><div className="trust-wallet-dialog__mark is-success"><Check size={26}/></div><h2>{t("trustWallet.doneTitle")}</h2><p>{t("trustWallet.doneBody")}</p><Button onClick={()=>void close()}>{t("common.close")}</Button></div>}
        {phase==="error"&&<div className="trust-wallet-dialog__center"><div className="trust-wallet-dialog__mark"><Wallet size={26}/></div><h2>{t("trustWallet.errorTitle")}</h2><p>{t(errorKey)}</p><div className="trust-wallet-actions">{stepUp&&client?<a className="mp-button mp-button--primary" href={client.stepUpURL("/admin/#/financial-settings")}>{t("financialSettings.confirmLogin")}</a>:<Button onClick={()=>void (family==="solana"?beginSolana():beginEVM())}>{t("common.retry")}</Button>}<a className="mp-button mp-button--secondary" href={trustWalletDownloadURL()} rel="noreferrer" target="_blank">{t("trustWallet.getWallet")}<ExternalLink size={15}/></a><Button onClick={()=>void close()} variant="secondary">{t("common.cancel")}</Button></div></div>}
      </div>
    </div>}
  </>;
}
