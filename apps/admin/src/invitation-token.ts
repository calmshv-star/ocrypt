export const invitationTokenStorageKey="merchant-admin-invitation-token";
export const invitationPhaseStorageKey="merchant-admin-invitation-oidc-started";
export const invitationIdempotencyStorageKey="merchant-admin-invitation-accept-idempotency";

export function validInvitationToken(value:string):boolean{return /^[A-Za-z0-9_-]{43}$/.test(value)}

// Invite delivery uses a fragment so the credential never reaches an HTTP
// server. Scrub it before React, session loading, analytics, or login links can
// observe the location; only this tab's sessionStorage retains it.
export function captureInvitationTokenFromLocation():void{
  if(typeof window==="undefined")return;
  const hash=window.location.hash;
  if(!hash.startsWith("#/invite?"))return;
  const params=new URLSearchParams(hash.slice(hash.indexOf("?")+1));
  const token=params.get("token")??"";
  if(validInvitationToken(token)){
    const previous=window.sessionStorage.getItem(invitationTokenStorageKey);
    if(previous!==token){window.sessionStorage.removeItem(invitationPhaseStorageKey);window.sessionStorage.removeItem(invitationIdempotencyStorageKey)}
    window.sessionStorage.setItem(invitationTokenStorageKey,token);
  }
  window.history.replaceState(window.history.state,"",`${window.location.pathname}${window.location.search}#/invite`);
}
