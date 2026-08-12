import { useI18n, type MessageKey } from "@merchant/i18n";
import { Badge, Button, PRODUCT_NAME, Select, ThemeToggle } from "@merchant/ui";
import { Check, CheckCircle2, ChevronDown, Clock3, Copy, ExternalLink, FileCheck2, LockKeyhole, RadioTower, RefreshCw, ShieldCheck, Upload } from "lucide-react";
import QRCode from "qrcode";
import { type ReactNode, useEffect, useMemo, useRef, useState } from "react";

type CheckoutStatus = "pending" | "detected" | "partially_paid" | "confirming" | "needs_review" | "settled" | "expired" | "preparing_payment_route" | "payment_route_failed";
type OnChainRoute = { id: string; provider: "on_chain"; network: string; asset: string; amount: string; address: string; receivedAmount?: string; remainingAmount?: string; paymentCount?: number; topUpAllowed?: boolean; transactionHash?: string };
type HostedRoute = { id: string; provider: "hosted_gateway"; providerId: string; asset: string; amount: string; paymentURL: string };
type Route = OnChainRoute | HostedRoute;
type CheckoutSession = { intentId: string; orderId: string; merchantName: string; amountMinor: string; currency: string; currencyScale: number; description: string; status: CheckoutStatus; expiresAt: string; routes: Route[]; selectedRouteId: string };
type PaymentLinkSelector = { provider: "on_chain"; chainId: string; assetId: string } | { provider: "hosted_gateway"; providerId: string; assetId: string };
type PaymentLink = { name: string; amountMinor: string; currency: string; currencyScale: number; description: string; allowedRoutes: PaymentLinkSelector[]; expiresAt?: string };
type ReturnTargets = { success?: string; cancel?: string };
type Redemption = { checkoutToken: string; checkoutURL: URL; session: CheckoutSession; returns: ReturnTargets };
type ReceiptSubmission = { id: string; paymentId: string; status: "proof_queued" | "transaction_not_visible"; proofId?: string; chainId: string; transactionId?: string; message: string };

const checkoutTokenPattern = /^cs_[A-Za-z0-9_-]{43}$/;
const paymentLinkTokenPattern = /^pl_[A-Za-z0-9_-]{43}$/;
const fixtureRoutes: Route[] = [
  { id: "tron-usdt", provider: "on_chain", network: "Tron", asset: "USDT", amount: "1280.00", address: "TWb4A6kVtQJ4z9Yp2mR7sX8cN1hL5uD3eF", transactionHash: "70e31d825cf84e0114c93c5f29dbbe2408eeab421e8a14d49f97d6fba2483f0d" },
  { id: "ethereum-usdc", provider: "on_chain", network: "Ethereum", asset: "USDC", amount: "1280.00", address: "0x8077444bed90f3ca9157ab8bf8d2c51103b2ce89", transactionHash: "0xe6843b6fa52ca5c2de30c9220e8768a0d05a9cecd6272430c193ec3f04bac022" }
];
const explorerBases: Record<string, string> = {
  "tron:mainnet": "https://tronscan.org/#/transaction/",
  "ethereum:mainnet": "https://etherscan.io/tx/",
  "solana:mainnet": "https://solscan.io/tx/",
  "bitcoin:mainnet": "https://mempool.space/tx/",
  "ton:mainnet": "https://tonscan.org/tx/",
  Tron: "https://tronscan.org/#/transaction/",
  Ethereum: "https://etherscan.io/tx/"
};
const statusKeys: Record<CheckoutStatus, [MessageKey, MessageKey]> = {
  pending: ["checkout.pending", "checkout.pendingHelp"],
  detected: ["checkout.detected", "checkout.confirmingHelp"],
  partially_paid: ["checkout.partiallyPaid", "checkout.partiallyPaidHelp"],
  confirming: ["checkout.confirming", "checkout.confirmingHelp"],
  needs_review: ["checkout.needsReview", "checkout.needsReviewHelp"],
  settled: ["checkout.settled", "checkout.settledHelp"],
  expired: ["checkout.expired", "checkout.expiredHelp"],
  preparing_payment_route: ["checkout.preparingRoute", "checkout.preparingRouteHelp"],
  payment_route_failed: ["checkout.routePreparationFailed", "checkout.routePreparationFailedHelp"]
};

function apiBase() {
  return String(import.meta.env.VITE_CHECKOUT_API_URL || window.location.origin).replace(/\/$/, "");
}

function readFixtureStatus(params: URLSearchParams): CheckoutStatus {
  const value = params.get("status");
  return value === "detected" || value === "partially_paid" || value === "confirming" || value === "needs_review" || value === "settled" || value === "expired" ? value : "pending";
}

function fixtureSession(params: URLSearchParams): CheckoutSession {
  const expiresIn = Math.max(1, Number(params.get("expires_in") ?? 900));
  const status = readFixtureStatus(params);
  const routes = status === "partially_paid" ? fixtureRoutes.map((route, index) => index === 0 ? { ...route, receivedAmount: "980", remainingAmount: "300", paymentCount: 1, topUpAllowed: true } : route) : fixtureRoutes;
  return { intentId: params.get("intent_id") ?? "pi_preview_01JQ8H6G2PE3", orderId: params.get("order_id") ?? "CHECKOUT-84913", merchantName: "Demo Store", amountMinor: "128000", currency: "USD", currencyScale: 2, description: "", status, expiresAt: new Date(Date.now() + expiresIn * 1000).toISOString(), routes, selectedRouteId: routes.some((route) => route.id === params.get("route")) ? params.get("route")! : routes[0]!.id };
}

function parseSession(value: unknown): CheckoutSession | null {
  if (!value || typeof value !== "object" || Array.isArray(value)) return null;
  const input = value as Record<string, unknown>;
  const statuses: CheckoutStatus[] = ["pending", "detected", "partially_paid", "confirming", "needs_review", "settled", "expired", "preparing_payment_route", "payment_route_failed"];
  if (typeof input.intent_id !== "string" || typeof input.order_id !== "string" || typeof input.merchant_name !== "string" || input.merchant_name.length < 1 || input.merchant_name.length > 200 || typeof input.amount_minor !== "string" || !/^[0-9]+$/.test(input.amount_minor) || typeof input.currency !== "string" || !/^[A-Z]{3}$/.test(input.currency) || typeof input.currency_scale !== "number" || !Number.isInteger(input.currency_scale) || input.currency_scale < 0 || input.currency_scale > 9 || typeof input.description !== "string" || input.description.length > 1000 || typeof input.status !== "string" || !statuses.includes(input.status as CheckoutStatus) || typeof input.expires_at !== "string" || !Number.isFinite(Date.parse(input.expires_at)) || !Array.isArray(input.routes) || typeof input.selected_route_id !== "string") return null;
  const waitingForRoute = input.status === "preparing_payment_route" || input.status === "payment_route_failed";
  if (waitingForRoute ? input.routes.length !== 0 || input.selected_route_id !== "" : input.routes.length === 0) return null;
  const parsedRoutes: Route[] = [];
  for (const route of input.routes) {
    if (!route || typeof route !== "object" || Array.isArray(route)) return null;
    const candidate = route as Record<string, unknown>;
    if (typeof candidate.id !== "string" || typeof candidate.asset !== "string" || typeof candidate.amount !== "string" || !/^[0-9]+(?:\.[0-9]+)?$/.test(candidate.amount)) return null;
    if (candidate.provider === "on_chain") {
      if (typeof candidate.network !== "string" || typeof candidate.address !== "string" || candidate.address.length < 16 || candidate.address.length > 256 || candidate.transaction_hash !== undefined && typeof candidate.transaction_hash !== "string" || candidate.provider_id !== undefined || candidate.payment_url !== undefined) return null;
      const hasProgress = candidate.received_amount !== undefined || candidate.remaining_amount !== undefined || candidate.payment_count !== undefined || candidate.top_up_allowed !== undefined;
      if (hasProgress && (typeof candidate.received_amount !== "string" || !/^[0-9]+(?:\.[0-9]+)?$/.test(candidate.received_amount) || typeof candidate.remaining_amount !== "string" || !/^[0-9]+(?:\.[0-9]+)?$/.test(candidate.remaining_amount) || typeof candidate.payment_count !== "number" || !Number.isSafeInteger(candidate.payment_count) || candidate.payment_count < 1 || typeof candidate.top_up_allowed !== "boolean" || candidate.top_up_allowed && /^0+(?:\.0+)?$/.test(candidate.remaining_amount))) return null;
      parsedRoutes.push({ id: candidate.id, provider: "on_chain", network: candidate.network, asset: candidate.asset, amount: candidate.amount, address: candidate.address, receivedAmount: hasProgress ? candidate.received_amount as string : undefined, remainingAmount: hasProgress ? candidate.remaining_amount as string : undefined, paymentCount: hasProgress ? candidate.payment_count as number : undefined, topUpAllowed: hasProgress ? candidate.top_up_allowed as boolean : undefined, transactionHash: typeof candidate.transaction_hash === "string" ? candidate.transaction_hash : undefined });
      continue;
    }
    if (candidate.provider === "hosted_gateway") {
      if (typeof candidate.provider_id !== "string" || typeof candidate.payment_url !== "string" || !safeProviderPaymentURL(candidate.payment_url) || candidate.network !== undefined || candidate.address !== undefined || candidate.transaction_hash !== undefined || candidate.explorer_url !== undefined || candidate.received_amount !== undefined || candidate.remaining_amount !== undefined || candidate.payment_count !== undefined || candidate.top_up_allowed !== undefined) return null;
      parsedRoutes.push({ id: candidate.id, provider: "hosted_gateway", providerId: candidate.provider_id, asset: candidate.asset, amount: candidate.amount, paymentURL: candidate.payment_url });
      continue;
    }
    return null;
  }
  if (input.selected_route_id !== "" && !parsedRoutes.some((route) => route.id === input.selected_route_id)) return null;
  if (input.status === "partially_paid" && parsedRoutes.filter((route) => route.provider === "on_chain" && route.receivedAmount !== undefined && route.remainingAmount !== undefined).length !== 1) return null;
  return { intentId: input.intent_id, orderId: input.order_id, merchantName: input.merchant_name, amountMinor: input.amount_minor, currency: input.currency, currencyScale: input.currency_scale, description: input.description, status: input.status as CheckoutStatus, expiresAt: input.expires_at, routes: parsedRoutes, selectedRouteId: input.selected_route_id };
}

function safeProviderPaymentURL(value: string): boolean {
  try {
    const parsed = new URL(value);
    return parsed.protocol === "https:" && parsed.username === "" && parsed.password === "" && parsed.hash === "";
  } catch {
    return false;
  }
}

function parsePaymentLink(value: unknown): PaymentLink | null {
  if (!value || typeof value !== "object" || Array.isArray(value)) return null;
  const input = value as Record<string, unknown>;
  if (typeof input.name !== "string" || typeof input.amount_minor !== "string" || !/^[0-9]+$/.test(input.amount_minor) || typeof input.currency !== "string" || typeof input.currency_scale !== "number" || !Number.isInteger(input.currency_scale) || input.currency_scale < 0 || input.currency_scale > 9 || typeof input.description !== "string" || !Array.isArray(input.allowed_routes) || input.allowed_routes.length !== 1 || input.expires_at !== undefined && (typeof input.expires_at !== "string" || !Number.isFinite(Date.parse(input.expires_at)))) return null;
  const allowedRoutes: PaymentLink["allowedRoutes"] = [];
  for (const route of input.allowed_routes) {
    if (!route || typeof route !== "object" || Array.isArray(route)) return null;
    const candidate = route as Record<string, unknown>;
    if (candidate.provider === "on_chain" && typeof candidate.chain_id === "string" && typeof candidate.asset_id === "string" && candidate.provider_id === undefined) {
      allowedRoutes.push({ provider: "on_chain", chainId: candidate.chain_id, assetId: candidate.asset_id });
      continue;
    }
    if (candidate.provider === "hosted_gateway" && typeof candidate.provider_id === "string" && typeof candidate.asset_id === "string" && candidate.chain_id === undefined) {
      allowedRoutes.push({ provider: "hosted_gateway", providerId: candidate.provider_id, assetId: candidate.asset_id });
      continue;
    }
    return null;
  }
  return { name: input.name, amountMinor: input.amount_minor, currency: input.currency, currencyScale: input.currency_scale, description: input.description, allowedRoutes, expiresAt: typeof input.expires_at === "string" ? input.expires_at : undefined };
}

function safeReturnURL(value: unknown): string | undefined {
  if (typeof value !== "string" || value === "") return undefined;
  try {
    const parsed = new URL(value);
    if (!parsed.username && !parsed.password && (parsed.protocol === "https:" || parsed.protocol === "http:" && (parsed.hostname === "localhost" || parsed.hostname === "127.0.0.1"))) return parsed.href;
  } catch {
    return undefined;
  }
  return undefined;
}

function parseRedemption(value: unknown): Redemption | null {
  if (!value || typeof value !== "object" || Array.isArray(value)) return null;
  const input = value as Record<string, unknown>;
  const checkout = input.checkout;
  const session = parseSession(input.session);
  if (typeof input.intent_id !== "string" || !checkout || typeof checkout !== "object" || Array.isArray(checkout) || !session || session.intentId !== input.intent_id || typeof input.success_url !== "string" || typeof input.cancel_url !== "string") return null;
  const issue = checkout as Record<string, unknown>;
  if (typeof issue.token !== "string" || !checkoutTokenPattern.test(issue.token) || typeof issue.url !== "string" || typeof issue.expires_at !== "string" || !Number.isFinite(Date.parse(issue.expires_at))) return null;
  try {
    const checkoutURL = new URL(issue.url, window.location.origin);
    if (checkoutURL.origin !== window.location.origin || checkoutURL.pathname !== "/checkout" || checkoutURL.hash || checkoutURL.searchParams.get("token") !== issue.token || [...checkoutURL.searchParams.keys()].some((key) => key !== "token")) return null;
    const success = safeReturnURL(input.success_url);
    const cancel = safeReturnURL(input.cancel_url);
    if (input.success_url !== "" && !success || input.cancel_url !== "" && !cancel) return null;
    return { checkoutToken: issue.token, checkoutURL, session, returns: { success, cancel } };
  } catch {
    return null;
  }
}

function parseReceiptSubmission(value: unknown): ReceiptSubmission | null {
  if (!value || typeof value !== "object" || Array.isArray(value)) return null;
  const input = value as Record<string, unknown>;
  if (typeof input.id !== "string" || typeof input.payment_id !== "string" || input.status !== "proof_queued" && input.status !== "transaction_not_visible" || typeof input.chain_id !== "string" || typeof input.message !== "string") return null;
  if (input.proof_id !== undefined && typeof input.proof_id !== "string" || input.transaction_id !== undefined && typeof input.transaction_id !== "string") return null;
  if (input.status === "proof_queued" && (typeof input.proof_id !== "string" || typeof input.transaction_id !== "string")) return null;
  return { id: input.id, paymentId: input.payment_id, status: input.status, proofId: input.proof_id as string | undefined, chainId: input.chain_id, transactionId: input.transaction_id as string | undefined, message: input.message };
}

function formatMinor(value: string, scale: number) {
  const digits = value.replace(/^0+(?=\d)/, "");
  if (scale === 0) return digits;
  const padded = digits.padStart(scale + 1, "0");
  return `${padded.slice(0, -scale)}.${padded.slice(-scale)}`;
}

function storageGet(key: string) {
  try { return window.sessionStorage.getItem(key); } catch { return null; }
}

function storageSet(key: string, value: string) {
  try { window.sessionStorage.setItem(key, value); } catch { /* browser storage can be disabled */ }
}

function storageRemove(key: string) {
  try { window.sessionStorage.removeItem(key); } catch { /* browser storage can be disabled */ }
}

function stableIdempotency(key: string) {
  const stored = storageGet(key);
  const longEnough = stored ? stored.length >= 8 : false;
  const shortEnough = stored ? stored.length <= 255 : false;
  if (stored && longEnough && shortEnough) return stored;
  const generated = globalThis.crypto?.randomUUID?.() ?? `checkout-${Date.now()}-${Math.random().toString(16).slice(2)}`;
  storageSet(key, generated);
  return generated;
}

function returnsKey(token: string) {
  return `checkout-returns:${token}`;
}

function storeReturns(token: string, targets: ReturnTargets) {
  storageSet(returnsKey(token), JSON.stringify(targets));
}

function readReturns(token: string): ReturnTargets {
  try {
    const value = JSON.parse(storageGet(returnsKey(token)) ?? "null") as Record<string, unknown> | null;
    if (!value) return {};
    return { success: safeReturnURL(value.success), cancel: safeReturnURL(value.cancel) };
  } catch {
    return {};
  }
}

function CheckoutChrome({ children }: { children: ReactNode }) {
  const { locale, locales, localeNames, setLocale, t } = useI18n();
  return <div className="checkout-page"><a className="mp-skip-link" href="#checkout-main">{t("common.skipContent")}</a><header className="checkout-header"><a aria-label={PRODUCT_NAME} className="checkout-brand" href="/"><span aria-hidden="true" className="checkout-brand__mark"><i /><i /></span><strong>{PRODUCT_NAME}</strong></a><div><Select aria-label={t("common.locale")} onChange={(event) => setLocale(event.target.value as typeof locale)} value={locale}>{locales.map((item) => <option key={item} value={item}>{localeNames[item]}</option>)}</Select><ThemeToggle label={t("common.theme")} /></div></header>{children}</div>;
}

function CheckoutUnavailable({ loading = false }: { loading?: boolean }) {
  const { t } = useI18n();
  return <CheckoutChrome><main className="checkout-main checkout-main--state" id="checkout-main"><section aria-live="polite" className="checkout-state" role={loading ? "status" : "alert"}><span aria-hidden="true" className="checkout-state__icon">{loading ? <RadioTower size={26} /> : <Clock3 size={26} />}</span><Badge tone={loading ? "info" : "negative"}>{t("checkout.secureCheckout")}</Badge><h1>{t(loading ? "checkout.loading" : "checkout.unavailable")}</h1>{!loading && <><p>{t("checkout.unavailableHelp")}</p><Button onClick={() => window.location.reload()} variant="secondary"><RefreshCw size={16} />{t("common.retry")}</Button></>}</section></main></CheckoutChrome>;
}

function PaymentLinkPage({ token, onRedeemed }: { token: string; onRedeemed: (redemption: Redemption) => void }) {
  const { locale, t } = useI18n();
  const [link, setLink] = useState<PaymentLink | null>(null);
  const [state, setState] = useState<"loading" | "ready" | "redeeming" | "unavailable" | "rate" | "conflict" | "error">("loading");
  useEffect(() => {
    const controller = new AbortController();
    void fetch(`${apiBase()}/v1/public/payment-links/${encodeURIComponent(token)}`, { headers: { Accept: "application/json" }, credentials: "omit", redirect: "error", cache: "no-store", signal: controller.signal }).then(async (response) => {
      if (response.status === 404) { setState("unavailable"); return; }
      if (response.status === 429) { setState("rate"); return; }
      if (!response.ok) { setState("error"); return; }
      const parsed = parsePaymentLink(await response.json());
      if (!parsed) { setState("error"); return; }
      setLink(parsed); setState("ready");
    }).catch(() => { if (!controller.signal.aborted) setState("error"); });
    return () => controller.abort();
  }, [token]);
  const redeem = async () => {
    setState("redeeming");
    const idempotency = stableIdempotency(`payment-link-redeem:${token}`);
    try {
      const response = await fetch(`${apiBase()}/v1/public/payment-links/${encodeURIComponent(token)}/redeem`, { method: "POST", headers: { Accept: "application/json", "Content-Type": "application/json", "Idempotency-Key": idempotency }, body: "{}", credentials: "omit", redirect: "error" });
      if (response.status === 404) { setState("unavailable"); return; }
      if (response.status === 409) { setState("conflict"); return; }
      if (response.status === 429) { setState("rate"); return; }
      if (response.status !== 201) { setState("error"); return; }
      const parsed = parseRedemption(await response.json());
      if (!parsed) { setState("error"); return; }
      storeReturns(parsed.checkoutToken, parsed.returns);
      onRedeemed(parsed);
    } catch {
      setState("error");
    }
  };
  const errorCopy = state === "rate" ? "checkout.rateLimited" : state === "conflict" ? "checkout.redeemConflict" : state === "unavailable" ? "checkout.unavailable" : "admin.dataError";
  const selector = link?.allowedRoutes[0];
  return <CheckoutChrome><main className="checkout-main" id="checkout-main"><section className="checkout-intro"><Badge tone="info">{t("checkout.offer")}</Badge><h1>{t("checkout.paymentLinkTitle")}</h1><p>{t("checkout.paymentLinkDescription")}</p></section>{link && selector && (state === "ready" || state === "redeeming") ? <section className="checkout-card payment-link-card" data-testid="payment-link-offer"><div><Badge tone="violet">{t("checkout.offer")}</Badge><h2>{link.name}</h2>{link.description && <p>{link.description}</p>}</div><dl><div><dt>{t("checkout.offerAmount")}</dt><dd data-testid="payment-link-amount">{formatMinor(link.amountMinor, link.currencyScale)} {link.currency}</dd></div><div><dt>{t(selector.provider === "on_chain" ? "common.network" : "checkout.provider")}</dt><dd>{selector.provider === "on_chain" ? selector.chainId : selector.providerId} · {selector.assetId}</dd></div>{link.expiresAt && <div><dt>{t("checkout.offerExpires")}</dt><dd>{new Intl.DateTimeFormat(locale, { dateStyle: "medium", timeStyle: "short" }).format(Date.parse(link.expiresAt))}</dd></div>}</dl><Button data-testid="redeem-payment-link" disabled={state === "redeeming"} onClick={() => void redeem()}>{t(state === "redeeming" ? "checkout.redeeming" : "checkout.payNow")}</Button></section> : <div className="checkout-link-state" role={state === "loading" ? "status" : "alert"}><strong>{t(state === "loading" ? "checkout.loading" : errorCopy as MessageKey)}</strong>{state !== "loading" && <Button onClick={() => window.location.reload()} variant="secondary">{t("common.retry")}</Button>}</div>}</main></CheckoutChrome>;
}

function CheckoutPage({ token, initialSession, fixture }: { token: string; initialSession?: CheckoutSession; fixture: boolean }) {
  const { t } = useI18n();
  const params = useMemo(() => new URLSearchParams(window.location.search), []);
  const seed = fixture ? fixtureSession(params) : initialSession ?? null;
  const [session, setSession] = useState<CheckoutSession | null>(seed);
  const [loadFailed, setLoadFailed] = useState(false);
  const [routeId, setRouteId] = useState(seed?.selectedRouteId || seed?.routes.find((item) => item.provider === "on_chain" && item.receivedAmount !== undefined)?.id || seed?.routes[0]?.id || "");
  const [selecting, setSelecting] = useState(false);
  const [selectionFailed, setSelectionFailed] = useState(false);
  const [receiptState, setReceiptState] = useState<"idle" | "uploading" | "queued" | "missing" | "error">("idle");
  const [copied, setCopied] = useState<"amount" | "address" | null>(null);
  const [secondsLeft, setSecondsLeft] = useState(seed ? Math.max(0, Math.floor((Date.parse(seed.expiresAt) - Date.now()) / 1000)) : 0);
  const [returnTargets, setReturnTargets] = useState<ReturnTargets>(() => fixture ? {} : readReturns(token));
  const canvasRef = useRef<HTMLCanvasElement>(null);
  const route = session?.routes.find((item) => item.id === routeId) ?? session?.routes[0];
  const status: CheckoutStatus = session ? secondsLeft === 0 && session.status !== "settled" && session.status !== "needs_review" ? "expired" : session.status : "pending";
  const canSendToRoute = route?.provider === "on_chain" && (status === "pending" && route.receivedAmount === undefined || status === "partially_paid" && route.topUpAllowed === true);
  const qrPayload = canSendToRoute && route?.provider === "on_chain" ? route.address : "";

  useEffect(() => {
    if (fixture) return;
    let stopped = false;
    let pollTimer: number | undefined;
    let activeController: AbortController | undefined;
    let retryAttempt = 0;
    let hasSession = Boolean(seed);
    let etag = "";
    const schedule = (delay: number) => { if (!stopped) pollTimer = window.setTimeout(load, delay); };
    const load = async () => {
      const controller = new AbortController(); activeController = controller;
      const timeout = window.setTimeout(() => controller.abort(), 10_000);
      try {
        const headers = new Headers({ Accept: "application/json" });
        if (etag) headers.set("If-None-Match", etag);
        const response = await fetch(`${apiBase()}/v1/checkout-sessions/${encodeURIComponent(token)}`, { headers, credentials: "omit", redirect: "error", cache: "no-store", signal: controller.signal });
        if (response.status === 304 && hasSession) { setLoadFailed(false); retryAttempt = 0; schedule(5000); return; }
        if (response.status === 404) { setSession(null); setLoadFailed(true); return; }
        if (!response.ok) throw new Error("checkout_unavailable");
        const parsed = parseSession(await response.json());
        if (!parsed) throw new Error("invalid_checkout_contract");
        if (stopped) return;
        etag = response.headers.get("ETag") ?? "";
        retryAttempt = 0; hasSession = true; setSession(parsed);
        setRouteId((current) => parsed.selectedRouteId || parsed.routes.find((item) => item.provider === "on_chain" && item.receivedAmount !== undefined)?.id || (parsed.routes.some((item) => item.id === current) ? current : parsed.routes[0]?.id ?? ""));
        setSecondsLeft(Math.max(0, Math.floor((Date.parse(parsed.expiresAt) - Date.now()) / 1000)));
        setLoadFailed(false);
        if (parsed.status !== "settled" && parsed.status !== "expired" && parsed.status !== "payment_route_failed") schedule(5000);
      } catch {
        if (!stopped) { setLoadFailed(true); const baseDelay = Math.min(30_000, 2000 * (2 ** retryAttempt)); retryAttempt += 1; schedule(Math.min(30_000, baseDelay + Math.floor(baseDelay * .2 * Math.random()))); }
      } finally { window.clearTimeout(timeout); }
    };
    void load();
    return () => { stopped = true; activeController?.abort(); if (pollTimer !== undefined) window.clearTimeout(pollTimer); };
  }, [fixture, token]);

  useEffect(() => {
    if (status === "settled") {
      setReturnTargets((targets) => { const settledTargets = { success: targets.success }; storeReturns(token, settledTargets); return settledTargets; });
      return;
    }
    if (status === "expired") {
      storageRemove(returnsKey(token));
      setReturnTargets({});
      return;
    }
    const timer = window.setInterval(() => setSecondsLeft((value) => Math.max(0, value - 1)), 1000);
    return () => window.clearInterval(timer);
  }, [status, token]);
  useEffect(() => { if (canvasRef.current && qrPayload) void QRCode.toCanvas(canvasRef.current, qrPayload, { width: 208, margin: 1, color: { dark: "#111827", light: "#ffffff" }, errorCorrectionLevel: "M" }); }, [qrPayload]);

  const selectRoute = async (nextRoute: string) => {
    if (status !== "pending") return;
    if (fixture) { setRouteId(nextRoute); return; }
    if (!session || session.selectedRouteId || selecting || nextRoute === routeId) return;
    setSelecting(true); setSelectionFailed(false);
    try {
      const idempotency = stableIdempotency(`checkout-select:${token}:${nextRoute}`);
      const response = await fetch(`${apiBase()}/v1/checkout-sessions/${encodeURIComponent(token)}/select-route`, { method: "POST", headers: { Accept: "application/json", "Content-Type": "application/json", "Idempotency-Key": idempotency }, body: JSON.stringify({ route_id: nextRoute }), credentials: "omit", redirect: "error" });
      if (!response.ok) throw new Error("route_selection_failed");
      const parsed = parseSession(await response.json());
      if (!parsed || parsed.selectedRouteId !== nextRoute) throw new Error("invalid_route_selection");
      setSession(parsed); setRouteId(nextRoute); setSecondsLeft(Math.max(0, Math.floor((Date.parse(parsed.expiresAt) - Date.now()) / 1000)));
    } catch { setSelectionFailed(true); } finally { setSelecting(false); }
  };
  const copy = async (kind: "amount" | "address", value: string) => { await navigator.clipboard.writeText(value); setCopied(kind); window.setTimeout(() => setCopied(null), 1600); };
  const submitReceipt = async (file: File) => {
    if (fixture) { setReceiptState("queued"); return; }
    const paymentId = session?.intentId;
    if (!paymentId) { setReceiptState("error"); return; }
    if (!/^image\/(?:jpeg|png|webp)$/.test(file.type) || file.size < 128 || file.size > 5 * 1024 * 1024) { setReceiptState("error"); return; }
    setReceiptState("uploading");
    try {
      const idempotency = stableIdempotency(`checkout-receipt:${token}:${file.name}:${file.size}:${file.lastModified}`);
      const response = await fetch(`${apiBase()}/v1/checkout-sessions/${encodeURIComponent(token)}/receipt`, { method: "POST", headers: { Accept: "application/json", "Content-Type": file.type, "Idempotency-Key": idempotency }, body: file, credentials: "omit", redirect: "error" });
      if (response.status !== 202) throw new Error("receipt_upload_failed");
      const result = parseReceiptSubmission(await response.json());
      if (!result || result.paymentId !== paymentId) throw new Error("invalid_receipt_contract");
      setReceiptState(result.status === "proof_queued" ? "queued" : "missing");
    } catch { setReceiptState("error"); }
  };
  if (!session) return <CheckoutUnavailable loading={!loadFailed} />;
  if (!route) {
    const preparationStatus = status === "payment_route_failed" || status === "expired" ? "payment_route_failed" : "preparing_payment_route";
    const [title, help] = statusKeys[preparationStatus];
    const cancelURL = returnTargets.cancel;
    return <CheckoutChrome><main className="checkout-main" id="checkout-main"><section className="checkout-intro"><Badge tone={preparationStatus === "payment_route_failed" ? "negative" : "info"}>{t("checkout.secureCheckout")}</Badge><h1>{t(title)}</h1><p>{t(help)}</p>{loadFailed && <p className="checkout-degraded" role="alert">{t("checkout.degraded")}</p>}</section><section className="checkout-card checkout-link-state" data-testid="payment-route-preparation" role="status"><span>{preparationStatus === "payment_route_failed" ? <Clock3 size={28} /> : <RadioTower size={28} />}</span><strong>{t(title)}</strong><p>{t(help)}</p>{preparationStatus === "payment_route_failed" && cancelURL && <a className="checkout-return" href={cancelURL}>{t("checkout.cancelPayment")}</a>}</section></main></CheckoutChrome>;
  }
  const remaining = `${String(Math.floor(secondsLeft / 60)).padStart(2, "0")}:${String(secondsLeft % 60).padStart(2, "0")}`;
  const [statusTitle, statusHelp] = statusKeys[status];
  const step = status === "settled" ? 4 : status === "confirming" ? 3 : status === "detected" || status === "partially_paid" || status === "needs_review" ? 2 : status === "expired" ? 0 : 1;
  const explorerUrl = route.provider === "on_chain" && route.transactionHash && explorerBases[route.network] ? `${explorerBases[route.network]}${encodeURIComponent(route.transactionHash)}` : null;
  const returnURL = status === "settled" ? returnTargets.success : status === "expired" ? undefined : returnTargets.cancel;
  const returnControl = returnURL
    ? <a className="checkout-return" href={returnURL}>{t(status === "settled" ? "checkout.returnToMerchant" : "checkout.cancelPayment")}</a>
    : status === "settled"
      ? <small className="checkout-return-empty">{t("checkout.returnUnavailable")}</small>
      : null;
  const routeLabel = route.provider === "on_chain" ? `${route.network} · ${route.asset}` : `${t("checkout.provider")} · ${route.asset}`;
  const routeStatusTitle = route.provider === "hosted_gateway" && status === "pending" ? "checkout.providerPending" : route.provider === "hosted_gateway" && (status === "detected" || status === "confirming") ? "checkout.providerConfirming" : statusTitle;
  const routeStatusHelp = route.provider === "hosted_gateway" && (status === "pending" || status === "detected" || status === "confirming") ? "checkout.providerPendingHelp" : statusHelp;
  const statusSteps: MessageKey[] = route.provider === "hosted_gateway"
    ? ["checkout.providerPending", "checkout.detected", "checkout.providerConfirming", "checkout.settled"]
    : ["checkout.pending", "checkout.detected", "checkout.confirming", "checkout.settled"];
  const isPartial = route.provider === "on_chain" && status === "partially_paid" && route.receivedAmount !== undefined && route.remainingAmount !== undefined;
  const amountToSend = isPartial ? route.remainingAmount! : route.amount;
  const amountLabel = isPartial ? "checkout.remainingAmount" : canSendToRoute ? "checkout.payExact" : "checkout.expectedAmount";
  const badgeTone = status === "settled" ? "positive" : status === "expired" ? "negative" : status === "partially_paid" || status === "needs_review" ? "warning" : "info";
  const routeSelectable = status === "pending" && !session.selectedRouteId && session.routes.length > 1;
  const displayTotal = `${formatMinor(session.amountMinor, session.currencyScale)} ${session.currency}`;
  return <CheckoutChrome><main className="checkout-main" id="checkout-main">
    <section className="checkout-intro">
      <div className="checkout-intro__copy"><Badge tone="info">{t(fixture ? "checkout.preview" : "checkout.secureCheckout")}</Badge><h1>{t("checkout.title")}</h1><p>{session.description || t("checkout.description")}</p></div>
      <div className="checkout-order-context"><strong>{session.merchantName}</strong><span>{session.orderId}</span><div><small>{t("checkout.offerAmount")}</small><b>{displayTotal}</b></div></div>
      {loadFailed && <p className="checkout-degraded" role="alert">{t("checkout.degraded")}</p>}
      {selectionFailed && <p className="checkout-degraded" role="alert">{t("checkout.routeSelectionFailed")}</p>}
    </section>
    <div className="checkout-layout">
      <section className="checkout-card checkout-card--payment">
        <div className="checkout-route-head">
          {routeSelectable ? <label><span>{selecting ? t("checkout.selectingRoute") : t("checkout.selectRoute")}</span><Select aria-label={t("checkout.selectRoute")} disabled={selecting} onChange={(event) => void selectRoute(event.target.value)} value={route.id}>{session.routes.map((item) => <option key={item.id} value={item.id}>{item.provider === "on_chain" ? `${item.network} · ${item.asset}` : `${t("checkout.provider")} · ${item.asset}`}</option>)}</Select></label> : <div className="checkout-route-fixed"><small>{t("checkout.selectRoute")}</small><strong><ShieldCheck size={16} />{routeLabel}</strong></div>}
          <Badge tone={badgeTone}>{t(routeStatusTitle)}</Badge>
        </div>
        {isPartial && <div className="checkout-progress" data-testid="payment-progress">
          <div><small>{t("checkout.expectedAmount")}</small><strong>{route.amount} <span>{route.asset}</span></strong></div>
          <div><small>{t("checkout.receivedAmount")}</small><strong data-testid="payment-received">{route.receivedAmount} <span>{route.asset}</span></strong></div>
          <div><small>{t("checkout.remainingAmount")}</small><strong data-testid="payment-remaining">{route.remainingAmount} <span>{route.asset}</span></strong></div>
        </div>}
        {route.provider === "on_chain" ? <div className="checkout-payment-grid">
          <div className="checkout-qr">
            {canSendToRoute ? <canvas aria-label={t("checkout.qrLabel")} ref={canvasRef} role="img" /> : <div className="checkout-qr-lock"><LockKeyhole size={34} /><strong>{t("checkout.transferLocked")}</strong></div>}
            <span data-testid="payment-network"><ShieldCheck size={14} />{routeLabel}</span>
          </div>
          <div className="checkout-payment-data">
            <div><small>{t(amountLabel)}</small><strong data-copy-value={amountToSend} data-testid="payment-amount">{amountToSend} <span>{route.asset}</span></strong>{canSendToRoute && <Button data-testid="copy-amount" onClick={() => void copy("amount", amountToSend)} size="sm" variant="secondary">{copied === "amount" ? <Check size={14} /> : <Copy size={14} />}{copied === "amount" ? t("common.copied") : t("checkout.copyAmount")}</Button>}</div>
            <div><small>{t("checkout.address")}</small><code data-testid="payment-address">{route.address}</code>{canSendToRoute && <Button data-testid="copy-address" onClick={() => void copy("address", route.address)} size="sm" variant="secondary">{copied === "address" ? <Check size={14} /> : <Copy size={14} />}{copied === "address" ? t("common.copied") : t("checkout.copyAddress")}</Button>}</div>
            {isPartial && <p className={route.topUpAllowed ? "checkout-topup-note" : "checkout-topup-note is-locked"} data-testid="top-up-instruction">{t(route.topUpAllowed ? "checkout.topUpInstruction" : "checkout.topUpUnavailable")}{route.topUpAllowed && <><br />{t("checkout.topUpFeeWarning")}</>}</p>}
            <div className="checkout-expiry"><Clock3 size={16} /><span>{t("checkout.expiresIn", { time: remaining })}</span></div>
          </div>
        </div> : <div className="checkout-provider"><ShieldCheck size={42} /><small>{t("checkout.providerAmount")}</small><strong data-testid="payment-amount">{route.amount} <span>{route.asset}</span></strong><p>{t("checkout.providerInstructions")}</p>{status !== "expired" && status !== "settled" && <a className="checkout-provider-link" data-testid="provider-payment-link" href={route.paymentURL} rel="noopener noreferrer" target="_blank">{t("checkout.continueProvider")}<ExternalLink size={16} /></a>}<div className="checkout-expiry"><Clock3 size={16} /><span>{t("checkout.expiresIn", { time: remaining })}</span></div></div>}
        <div className={`checkout-status is-${status}`} role="status"><span>{status === "settled" ? <CheckCircle2 size={22} /> : <RadioTower size={22} />}</span><div><strong>{t(routeStatusTitle)}</strong><p>{t(routeStatusHelp)}</p></div></div>
        {route.provider === "on_chain" && status !== "settled" && status !== "expired" && <details className={`checkout-receipt is-${receiptState}`} data-testid="receipt-assistance" open={receiptState !== "idle" || undefined}><summary><span aria-hidden="true">{receiptState === "queued" ? <FileCheck2 size={20} /> : <Upload size={20} />}</span><strong>{t(receiptState === "queued" ? "checkout.receiptQueued" : receiptState === "missing" ? "checkout.receiptMissing" : receiptState === "error" ? "checkout.receiptFailed" : receiptState === "uploading" ? "checkout.receiptUploading" : "checkout.receiptTitle")}</strong><ChevronDown aria-hidden="true" size={17} /></summary><div className="checkout-receipt__body"><p>{t(receiptState === "queued" ? "checkout.receiptQueuedHelp" : receiptState === "missing" ? "checkout.receiptMissingHelp" : receiptState === "error" ? "checkout.receiptFailedHelp" : "checkout.receiptHelp")}</p>{(receiptState === "idle" || receiptState === "missing" || receiptState === "error") && <label className="checkout-receipt-button"><Upload size={14} />{t(receiptState === "idle" ? "checkout.receiptAction" : "checkout.receiptRetry")}<input accept="image/jpeg,image/png,image/webp" data-testid="receipt-file" onChange={(event) => { const file=event.target.files?.[0]; if(file) void submitReceipt(file); event.currentTarget.value=""; }} type="file" /></label>}</div></details>}
        <ol aria-label={t("common.status")} className="checkout-steps">{statusSteps.map((key, index) => <li className={index + 1 <= step ? "is-complete" : ""} key={key}><span>{index + 1 <= step ? <Check size={12} /> : index + 1}</span><strong>{t(key)}</strong></li>)}</ol>
      </section>
      <aside className="checkout-card checkout-summary"><h2>{t("common.details")}</h2><strong className="checkout-summary__merchant">{session.merchantName}</strong><dl><div><dt>{t("checkout.order")}</dt><dd>{session.orderId}</dd></div>{route.provider === "on_chain" ? <div><dt>{t("common.network")}</dt><dd>{route.network}</dd></div> : <div><dt>{t("checkout.provider")}</dt><dd>{route.providerId}</dd></div>}<div><dt>{t("common.asset")}</dt><dd>{route.asset}</dd></div>{route.provider === "on_chain" && route.paymentCount !== undefined && <div><dt>{t("checkout.paymentCount")}</dt><dd>{route.paymentCount}</dd></div>}</dl><details className="checkout-technical"><summary>{t("checkout.intent")}</summary><code>{session.intentId}</code></details>{explorerUrl && <a className="checkout-explorer" href={explorerUrl} rel="noreferrer" target="_blank">{t("checkout.openExplorer")}<ExternalLink size={14} /></a>}{returnControl}<p><LockKeyhole size={16} />{t(route.provider === "hosted_gateway" ? "checkout.providerSecureNote" : "checkout.secureNote")}</p></aside>
    </div>
  </main></CheckoutChrome>;
}

type Target = { kind: "checkout"; token: string; initial?: CheckoutSession } | { kind: "payment-link"; token: string } | { kind: "invalid" };
function targetFromURL(): Target {
  const token = new URLSearchParams(window.location.search).get("token") ?? "";
  if (window.location.pathname === "/checkout" && checkoutTokenPattern.test(token)) return { kind: "checkout", token };
  if (window.location.pathname === "/pay" && paymentLinkTokenPattern.test(token)) return { kind: "payment-link", token };
  return { kind: "invalid" };
}

export function App() {
  // A QA build may contain deterministic fixture support, but it is enabled
  // only on this dedicated path. Public /pay and /checkout routes still use
  // the real capability-token API contract in the same build.
  const fixture = import.meta.env.VITE_CHECKOUT_FIXTURE_MODE === "true" && window.location.pathname === "/checkout/fixture";
  const [target, setTarget] = useState<Target>(() => fixture ? { kind: "checkout", token: "cs_fixture" } : targetFromURL());
  useEffect(() => { if (fixture) return; const update = () => setTarget(targetFromURL()); window.addEventListener("popstate", update); return () => window.removeEventListener("popstate", update); }, [fixture]);
  if (target.kind === "payment-link") return <PaymentLinkPage onRedeemed={(redemption) => { window.history.replaceState({}, "", `${redemption.checkoutURL.pathname}${redemption.checkoutURL.search}`); setTarget({ kind: "checkout", token: redemption.checkoutToken, initial: redemption.session }); }} token={target.token} />;
  if (target.kind === "checkout") return <CheckoutPage fixture={fixture} initialSession={target.initial} token={target.token} />;
  return <Unavailable />;
}

function Unavailable() {
  return <CheckoutUnavailable />;
}
