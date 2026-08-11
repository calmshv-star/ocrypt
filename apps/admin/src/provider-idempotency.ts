const storageKey="merchant.admin.provider-ops.pending.v1";
const maximum=24;
type Entry={f:string;k:string;t:number};
const keyPattern=/^provider-[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i;
function valid(value:unknown):value is Entry{if(!value||typeof value!=="object")return false;const item=value as Partial<Entry>;return typeof item.f==="string"&&item.f.length>0&&item.f.length<=4096&&typeof item.k==="string"&&keyPattern.test(item.k)&&typeof item.t==="number"&&Number.isSafeInteger(item.t)&&item.t>=0}
function read(storage:Storage):Entry[]{const raw=storage.getItem(storageKey);if(!raw)return[];if(raw.length>131072){storage.removeItem(storageKey);return[]}try{const parsed:unknown=JSON.parse(raw);if(!Array.isArray(parsed)||parsed.length>maximum||!parsed.every(valid))throw new Error("invalid");return parsed}catch{storage.removeItem(storageKey);return[]}}
function write(storage:Storage,entries:Entry[]){if(entries.length===0)storage.removeItem(storageKey);else storage.setItem(storageKey,JSON.stringify(entries.slice(-maximum)))}
function browserStorage():Storage|undefined{try{return typeof window==="undefined"?undefined:window.sessionStorage}catch{return undefined}}
export function pendingProviderMutationKey(fingerprint:string,storage=browserStorage()):string{if(!fingerprint||fingerprint.length>4096)throw new Error("invalid fingerprint");if(!storage)return `provider-${crypto.randomUUID()}`;const entries=read(storage);const existing=entries.find(item=>item.f===fingerprint);if(existing)return existing.k;const key=`provider-${crypto.randomUUID()}`;entries.push({f:fingerprint,k:key,t:Date.now()});write(storage,entries);return key}
export function completeProviderMutation(fingerprint:string,storage=browserStorage()){if(storage)write(storage,read(storage).filter(item=>item.f!==fingerprint))}
