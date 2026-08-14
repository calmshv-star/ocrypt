export type TrustWalletKind = "evm_personal_sign"|"solana_sign_message";

export type TrustWalletConnection = {
  kind:TrustWalletKind;
  address:string;
  source:"extension"|"mobile"|"in-app";
  sign(message:string):Promise<string>;
  disconnect():Promise<void>;
};

type EIP1193Provider = {
  request(input:{method:string;params?:unknown[]}):Promise<unknown>;
};

type EIP6963Detail = {
  info?:{rdns?:string;name?:string};
  provider?:EIP1193Provider;
};

type SolanaProvider = {
  publicKey?:{toString():string};
  connect():Promise<unknown>;
  disconnect?():Promise<void>;
  signMessage(message:Uint8Array,encoding?:string):Promise<unknown>;
};

function walletWindow():Window&{trustwallet?:{ethereum?:EIP1193Provider;solana?:SolanaProvider}} {
  return window as Window&{trustwallet?:{ethereum?:EIP1193Provider;solana?:SolanaProvider}};
}

function asStringArray(value:unknown):string[] {
  return Array.isArray(value)?value.filter((item):item is string=>typeof item==="string"):[];
}

function normalizedEVMAddress(value:string):string {
  const address=value.trim().toLowerCase();
  if(!/^0x[0-9a-f]{40}$/.test(address))throw new Error("invalid_wallet_address");
  return address;
}

export function walletConnectProjectId():string {
  const value=(import.meta as ImportMeta&{env?:Record<string,string|undefined>}).env?.VITE_WALLETCONNECT_PROJECT_ID?.trim()??"";
  return /^[0-9a-f]{32}$/i.test(value)?value:"";
}

export async function discoverTrustEVMProvider(timeoutMs=500):Promise<EIP1193Provider|null> {
  const direct=walletWindow().trustwallet?.ethereum;
  if(direct)return direct;
  return new Promise(resolve=>{
    let settled=false;
    const finish=(provider:EIP1193Provider|null)=>{if(settled)return;settled=true;clearTimeout(timer);window.removeEventListener("eip6963:announceProvider",announce as EventListener);resolve(provider)};
    const announce=(event:Event)=>{
      const detail=(event as CustomEvent<EIP6963Detail>).detail;
      if(detail?.info?.rdns==="com.trustwallet.app"&&detail.provider)finish(detail.provider);
    };
    const timer=window.setTimeout(()=>finish(null),timeoutMs);
    window.addEventListener("eip6963:announceProvider",announce as EventListener);
    window.dispatchEvent(new Event("eip6963:requestProvider"));
  });
}

function utf8Hex(message:string):string {
  return `0x${[...new TextEncoder().encode(message)].map(byte=>byte.toString(16).padStart(2,"0")).join("")}`;
}

export async function connectInjectedTrustEVM():Promise<TrustWalletConnection|null> {
  const provider=await discoverTrustEVMProvider();
  if(!provider)return null;
  const accounts=asStringArray(await provider.request({method:"eth_requestAccounts"}));
  const account=accounts[0];
  if(!account)throw new Error("wallet_no_account");
  const address=normalizedEVMAddress(account);
  return {
    kind:"evm_personal_sign",address,source:walletWindow().trustwallet?.ethereum===provider?"in-app":"extension",
    async sign(message){
      const signature=await provider.request({method:"personal_sign",params:[utf8Hex(message),address]});
      if(typeof signature!=="string"||!/^0x[0-9a-f]{130}$/i.test(signature))throw new Error("wallet_invalid_signature");
      return signature;
    },
    async disconnect(){return Promise.resolve()}
  };
}

function accountAddress(account:string):string {
  const parts=account.split(":");
  return parts.length>=3?parts.slice(2).join(":"):"";
}

export async function connectMobileTrustEVM(projectId:string,chainIDs:string[],onURI:(uri:string)=>void):Promise<TrustWalletConnection> {
  if(!/^[0-9a-f]{32}$/i.test(projectId)||chainIDs.length===0||chainIDs.some(chain=>!/^eip155:[1-9][0-9]*$/.test(chain)))throw new Error("walletconnect_not_configured");
  const authorizationChain=chainIDs.includes("eip155:1")?"eip155:1":chainIDs[0];
  if(!authorizationChain)throw new Error("walletconnect_not_configured");
  const {default:SignClient}=await import("@walletconnect/sign-client");
  const client=await SignClient.init({
    projectId,
    metadata:{name:"Ocrypt",description:"Connect a public receiving address",url:window.location.origin,icons:[`${window.location.origin}/favicon.svg`]}
  });
  const {uri,approval}=await client.connect({requiredNamespaces:{eip155:{chains:[authorizationChain],methods:["personal_sign"],events:["accountsChanged","chainChanged"]}}});
  if(!uri)throw new Error("walletconnect_pairing_failed");
  onURI(uri);
  const session=await approval();
  const namespace=session.namespaces.eip155;
  const account=namespace?.accounts?.find(item=>typeof item==="string"&&item.startsWith("eip155:"));
  if(!account)throw new Error("wallet_no_account");
  const address=normalizedEVMAddress(accountAddress(account));
  const chainID=account.split(":").slice(0,2).join(":");
  return {
    kind:"evm_personal_sign",address,source:"mobile",
    async sign(message){
      const signature=await client.request({topic:session.topic,chainId:chainID,request:{method:"personal_sign",params:[utf8Hex(message),address]}});
      if(typeof signature!=="string"||!/^0x[0-9a-f]{130}$/i.test(signature))throw new Error("wallet_invalid_signature");
      return signature;
    },
    async disconnect(){
      try{await client.disconnect({topic:session.topic,reason:{code:6000,message:"Import finished"}})}catch{return}
    }
  };
}

function publicKeyFrom(value:unknown,provider:SolanaProvider):string {
  if(value&&typeof value==="object"&&"publicKey" in value){
    const publicKey=(value as {publicKey?:unknown}).publicKey;
    if(typeof publicKey==="string")return publicKey;
    if(publicKey&&typeof publicKey==="object"&&"toString" in publicKey&&typeof publicKey.toString==="function")return publicKey.toString();
  }
  return provider.publicKey?.toString()??"";
}

function signatureBytes(value:unknown):Uint8Array {
  const candidate=value&&typeof value==="object"&&"signature" in value?(value as {signature?:unknown}).signature:value;
  if(candidate instanceof Uint8Array)return candidate;
  if(Array.isArray(candidate)&&candidate.every(item=>Number.isInteger(item)&&item>=0&&item<=255))return Uint8Array.from(candidate);
  throw new Error("wallet_invalid_signature");
}

function bytesToBase64(value:Uint8Array):string {
  let binary="";
  for(const byte of value)binary+=String.fromCharCode(byte);
  return btoa(binary);
}

export async function connectInjectedTrustSolana():Promise<TrustWalletConnection|null> {
  const provider=walletWindow().trustwallet?.solana;
  if(!provider)return null;
  const connected=await provider.connect();
  const address=publicKeyFrom(connected,provider).trim();
  if(!/^[1-9A-HJ-NP-Za-km-z]{32,44}$/.test(address))throw new Error("invalid_wallet_address");
  return {
    kind:"solana_sign_message",address,source:"in-app",
    async sign(message){
      const result=await provider.signMessage(new TextEncoder().encode(message),"utf8");
      const signature=signatureBytes(result);
      if(signature.length!==64)throw new Error("wallet_invalid_signature");
      return bytesToBase64(signature);
    },
    async disconnect(){if(provider.disconnect)await provider.disconnect()}
  };
}

export async function walletConnectQRCode(uri:string):Promise<string> {
  const {default:QRCode}=await import("qrcode");
  return QRCode.toDataURL(uri,{errorCorrectionLevel:"M",margin:2,width:320,color:{dark:"#111111ff",light:"#ffffffff"}});
}

export function trustWalletDeepLink(uri:string):string {
  return `https://link.trustwallet.com/wc?uri=${encodeURIComponent(uri)}`;
}

export function trustWalletDownloadURL():string {return "https://trustwallet.com/download"}
