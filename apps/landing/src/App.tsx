import { useI18n, type MessageKey } from "@merchant/i18n";
import {
  Button,
  PRODUCT_NAME,
  PRODUCT_SHORT_NAME,
  Select,
  ThemeToggle,
  cn,
  type ButtonSize,
  type ButtonVariant
} from "@merchant/ui";
import {
  ArrowRight,
  Blocks,
  Check,
  CheckCircle2,
  ChevronRight,
  CircleDollarSign,
  Clipboard,
  Code2,
  DatabaseZap,
  Fingerprint,
  Gauge,
  KeyRound,
  Menu,
  RadioTower,
  RotateCcw,
  ShieldCheck,
  Webhook,
  X,
  Zap
} from "lucide-react";
import { useState, type ReactNode } from "react";

export const API_EXAMPLE = `body='{"merchant_order_id":"checkout_84913","amount_minor":"128000","currency":"USD","currency_scale":2,"allowed_routes":[{"provider":"on_chain","chain_id":"tron:mainnet","asset_id":"usdt-tron"}]}'
timestamp=$(date +%s)
nonce=$(openssl rand -hex 16)
digest_hex=$(printf %s "$body" | openssl dgst -sha256 -hex | awk '{print $2}')
digest_b64=$(printf %s "$body" | openssl dgst -sha256 -binary | openssl base64 -A)
canonical=$(printf 'POST\\n/v1/payment-intents\\n%s\\n%s\\n%s' "$timestamp" "$nonce" "$digest_hex")
signature=$(printf %s "$canonical" | openssl dgst -sha256 -hmac "$MERCHANT_SECRET" -binary | openssl base64 -A | tr '+/' '-_' | tr -d '=')

curl "$MERCHANT_API_URL/v1/payment-intents" \\
  -H "Content-Type: application/json" \\
  -H "Idempotency-Key: checkout_84913" \\
  -H "Merchant-Key-Id: $MERCHANT_KEY_ID" \\
  -H "Merchant-Timestamp: $timestamp" \\
  -H "Merchant-Nonce: $nonce" \\
  -H "Content-Digest: sha-256=:$digest_b64:" \\
  -H "Merchant-Signature: $signature" \\
  --data-binary "$body"`;

const ADMIN_URL = import.meta.env.VITE_ADMIN_URL || "../admin/#/overview";
const SANDBOX_URL = import.meta.env.VITE_SANDBOX_URL || "#developers";
const DOCS_URL = import.meta.env.VITE_DOCS_URL || "#developers";
const SALES_URL = import.meta.env.VITE_SALES_URL || "#contact";

const networks: Array<{ name: string; mark: string; detailKey: MessageKey; tone: string }> = [
  { name: "Tron", mark: "TRX", detailKey: "landing.network.tronDetail", tone: "cyan" },
  { name: "Ethereum", mark: "ETH", detailKey: "landing.network.ethereumDetail", tone: "violet" },
  { name: "Solana", mark: "SOL", detailKey: "landing.network.solanaDetail", tone: "green" },
  { name: "TON", mark: "TON", detailKey: "landing.network.tonDetail", tone: "blue" },
  { name: "Aptos", mark: "APT", detailKey: "landing.network.aptosDetail", tone: "orange" }
] as const;

function Brand() {
  return (
    <a aria-label={PRODUCT_NAME} className="landing-brand" href="#top">
      <span aria-hidden="true" className="landing-brand__mark"><span /><span /></span>
      <span className="landing-brand__full">{PRODUCT_NAME}</span>
      <span className="landing-brand__short">{PRODUCT_SHORT_NAME}</span>
    </a>
  );
}

function ActionLink({
  children,
  href,
  className,
  size = "md",
  variant = "primary"
}: {
  children: ReactNode;
  href: string;
  className?: string;
  size?: ButtonSize;
  variant?: ButtonVariant;
}) {
  const external = /^https?:\/\//i.test(href);
  return (
    <a
      className={cn("mp-button", `mp-button--${variant}`, `mp-button--${size}`, className)}
      href={href}
      rel={external ? "noreferrer" : undefined}
      target={external ? "_blank" : undefined}
    >
      {children}
    </a>
  );
}

function SectionIntro({ eyebrow, title, body, align = "left" }: { eyebrow: string; title: string; body: string; align?: "left" | "center" }) {
  return <header className={`landing-section-intro is-${align}`}><span>{eyebrow}</span><h2>{title}</h2><p>{body}</p></header>;
}

function FeatureCard({ icon, title, body, index }: { icon: ReactNode; title: string; body: string; index: string }) {
  return (
    <article className="landing-feature-card">
      <div className="landing-feature-card__top"><span className="landing-feature-card__icon">{icon}</span><span>{index}</span></div>
      <h3>{title}</h3><p>{body}</p>
      <span className="landing-feature-card__line" />
    </article>
  );
}

export function App() {
  const { locale, locales, localeNames, setLocale, t } = useI18n();
  const [menuOpen, setMenuOpen] = useState(false);
  const [copied, setCopied] = useState(false);
  const capabilityKeys: Array<[MessageKey, MessageKey, ReactNode]> = [
    ["landing.feature.intents.title", "landing.feature.intents.body", <CircleDollarSign size={20} />],
    ["landing.feature.chain.title", "landing.feature.chain.body", <Blocks size={20} />],
    ["landing.feature.review.title", "landing.feature.review.body", <Fingerprint size={20} />],
    ["landing.feature.delivery.title", "landing.feature.delivery.body", <Webhook size={20} />]
  ];
  const pipelineKeys: Array<[MessageKey, ReactNode]> = [
    ["landing.pipeline.intent", <CircleDollarSign size={17} />],
    ["landing.pipeline.observe", <RadioTower size={17} />],
    ["landing.pipeline.finality", <ShieldCheck size={17} />],
    ["landing.pipeline.match", <DatabaseZap size={17} />],
    ["landing.pipeline.deliver", <Webhook size={17} />]
  ];

  const copyRequest = async () => {
    await navigator.clipboard?.writeText(API_EXAMPLE);
    setCopied(true);
    window.setTimeout(() => setCopied(false), 1800);
  };

  return (
    <div className="landing" id="top">
      <a className="mp-skip-link" href="#main">{t("common.skipContent")}</a>
      <header className="landing-nav-shell">
        <nav aria-label={t("common.primaryNavigation")} className="landing-nav">
          <Brand />
          <div className="landing-nav__links">
            <a href="#product">{t("landing.nav.product")}</a>
            <a href="#networks">{t("landing.nav.networks")}</a>
            <a href="#reliability">{t("landing.nav.reliability")}</a>
            <a href="#developers">{t("common.developers")}</a>
          </div>
          <div className="landing-nav__actions">
            <Select aria-label={t("common.locale")} onChange={(event) => setLocale(event.target.value as typeof locale)} value={locale}>
              {locales.map((item) => <option key={item} value={item}>{localeNames[item]}</option>)}
            </Select>
            <ThemeToggle label={t("common.theme")} />
            <ActionLink className="landing-sign-in" href={ADMIN_URL} variant="quiet">{t("common.signIn")}</ActionLink>
            <ActionLink href={SANDBOX_URL}>{t("common.getStarted")}<ArrowRight size={15} /></ActionLink>
            <button aria-label={menuOpen ? t("common.closeNavigation") : t("common.openNavigation")} className="landing-menu-button" onClick={() => setMenuOpen((value) => !value)}>{menuOpen ? <X size={20} /> : <Menu size={20} />}</button>
          </div>
        </nav>
        {menuOpen && <div className="landing-mobile-menu"><a href="#product" onClick={() => setMenuOpen(false)}>{t("landing.nav.product")}</a><a href="#networks" onClick={() => setMenuOpen(false)}>{t("landing.nav.networks")}</a><a href="#reliability" onClick={() => setMenuOpen(false)}>{t("landing.nav.reliability")}</a><a href="#developers" onClick={() => setMenuOpen(false)}>{t("common.developers")}</a><ActionLink href={SANDBOX_URL}>{t("common.getStarted")}</ActionLink></div>}
      </header>

      <main id="main">
        <section className="landing-hero">
          <div className="landing-hero__glow" />
          <div className="landing-container landing-hero__grid">
            <div className="landing-hero__copy">
              <span className="landing-eyebrow"><span className="landing-live-dot" />{t("landing.eyebrow")}</span>
              <h1>{t("landing.hero.title")}</h1>
              <p>{t("landing.hero.body")}</p>
              <div className="landing-hero__actions"><ActionLink href={SANDBOX_URL} size="lg">{t("landing.hero.primary")}<ArrowRight size={17} /></ActionLink><ActionLink href={DOCS_URL} size="lg" variant="secondary">{t("landing.hero.secondary")}<ChevronRight size={17} /></ActionLink></div>
              <small><ShieldCheck size={14} />{t("landing.hero.note")}</small>
            </div>
            <div className="settlement-card">
              <div className="settlement-card__head"><span><span className="landing-live-dot" />{t("common.sandbox")} · {t("landing.signal.live")}</span><code>pi_01JQ8H6G2PE3</code></div>
              <div className="settlement-card__amount"><span>1,280.00</span><strong>USDT</strong><small>≈ 1,280.00 USD</small></div>
              <div className="settlement-card__route"><span className="network-orb">TRX</span><div><strong>Tron · USDT</strong><small>TWb4…19Vp</small></div><span>22 / 20</span></div>
              <ol className="settlement-steps">
                {["landing.signal.detected", "landing.signal.finalized", "landing.signal.settled", "landing.signal.delivered"].map((key, index) => <li key={key}><span><Check size={12} /></span><strong>{t(key as MessageKey)}</strong><time>{["11:43:08.102", "11:43:15.440", "11:43:15.517", "11:43:15.801"][index]}</time></li>)}
              </ol>
              <div className="settlement-card__foot"><span><KeyRound size={14} />HMAC-SHA256 · kv_4</span><span>trace_9M42</span></div>
            </div>
          </div>
          <div className="landing-container landing-preview-label"><span>{t("common.previewData")}</span></div>
          <div className="landing-container landing-metrics">
            {[['99.99%', 'landing.metric.uptime'], ['0', 'landing.metric.doubleCredit'], ['100%', 'landing.metric.trace'], ['5', 'landing.metric.networks']].map(([value, key]) => <div key={key}><strong>{value}</strong><span>{t(key as MessageKey)}</span></div>)}
          </div>
        </section>

        <section className="landing-section" id="product">
          <div className="landing-container">
            <SectionIntro body={t("landing.capabilities.body")} eyebrow={t("landing.capabilities.eyebrow")} title={t("landing.capabilities.title")} />
            <div className="landing-feature-grid">{capabilityKeys.map(([title, body, icon], index) => <FeatureCard body={t(body)} icon={icon} index={`0${index + 1}`} key={title} title={t(title)} />)}</div>
          </div>
        </section>

        <section className="landing-section landing-section--ink">
          <div className="landing-container landing-pipeline-layout">
            <SectionIntro body={t("landing.pipeline.body")} eyebrow={t("landing.pipeline.eyebrow")} title={t("landing.pipeline.title")} />
            <div className="landing-pipeline">
              {pipelineKeys.map(([key, icon], index) => <div className="landing-pipeline__step" key={key}><span>{icon}</span><div><small>{String(index + 1).padStart(2, "0")}</small><strong>{t(key)}</strong></div>{index < pipelineKeys.length - 1 && <i />}</div>)}
            </div>
          </div>
        </section>

        <section className="landing-section" id="networks">
          <div className="landing-container">
            <SectionIntro align="center" body={t("landing.networks.body")} eyebrow={t("landing.nav.networks")} title={t("landing.networks.title")} />
            <div className="landing-preview-label landing-preview-label--center"><span>{t("common.previewData")}</span></div>
            <div className="landing-network-grid">{networks.map((network) => <article className="landing-network-card" key={network.name}><span className={`network-mark is-${network.tone}`}>{network.mark}</span><h3>{network.name}</h3><p>{t(network.detailKey)}</p><span><CheckCircle2 size={14} />{t("status.healthy")}</span></article>)}</div>
          </div>
        </section>

        <section className="landing-section landing-reliability" id="reliability">
          <div className="landing-container landing-reliability__layout">
            <div><SectionIntro body={t("landing.reliability.body")} eyebrow={t("landing.reliability.eyebrow")} title={t("landing.reliability.title")} /><div className="reliability-list">{[
              ["landing.reliability.scanners", "landing.reliability.scannersBody", <RadioTower size={18} />],
              ["landing.reliability.callbacks", "landing.reliability.callbacksBody", <RotateCcw size={18} />],
              ["landing.reliability.audit", "landing.reliability.auditBody", <ShieldCheck size={18} />]
            ].map(([title, body, icon]) => <div key={title as string}><span>{icon}</span><div><strong>{t(title as MessageKey)}</strong><p>{t(body as MessageKey)}</p></div></div>)}</div></div>
            <div className="operations-panel">
              <div className="operations-panel__head"><div><span className="landing-live-dot" />{t("common.previewData")}</div><span>{t("landing.operations.timestamp")}</span></div>
              <div className="operations-panel__metric"><span><Gauge size={17} />{t("assets.scannerLag")}</span><strong>2.4 s</strong></div>
              <div className="operations-chart"><span style={{ height: "35%" }} /><span style={{ height: "42%" }} /><span style={{ height: "39%" }} /><span style={{ height: "68%" }} /><span style={{ height: "46%" }} /><span style={{ height: "31%" }} /><span style={{ height: "27%" }} /><span style={{ height: "38%" }} /><span style={{ height: "30%" }} /><span style={{ height: "24%" }} /><span style={{ height: "29%" }} /><span style={{ height: "22%" }} /></div>
              <div className="operations-panel__rows"><div><span><i className="is-green" />Tron</span><strong>{t("landing.operations.oneBlock")}</strong><small>74,118,924</small></div><div><span><i className="is-green" />Ethereum</span><strong>{t("landing.operations.twoBlocks")}</strong><small>21,983,445</small></div><div><span><i className="is-yellow" />TON</span><strong>{t("landing.operations.eighteenBlocks")}</strong><small>48,991,224</small></div><div><span><i className="is-green" />Solana</span><strong>{t("landing.operations.fourSlots")}</strong><small>358,728,050</small></div></div>
            </div>
          </div>
        </section>

        <section className="landing-section landing-developer" id="developers">
          <div className="landing-container landing-developer__layout">
            <div><SectionIntro body={t("landing.developer.body")} eyebrow={t("landing.developer.eyebrow")} title={t("landing.developer.title")} /><ActionLink href={DOCS_URL} size="lg" variant="secondary">{t("common.documentation")}<ArrowRight size={16} /></ActionLink></div>
            <div className="code-window"><div className="code-window__bar"><span><i /><i /><i /></span><button aria-label={t("landing.developer.copy")} onClick={copyRequest}>{copied ? <Check size={14} /> : <Clipboard size={14} />}{copied ? t("common.copied") : t("landing.developer.copy")}</button></div><pre tabIndex={0}><code>{API_EXAMPLE}</code></pre><div className="code-window__response"><Zap size={14} /><span>201 · 84 ms</span><code>pi_01JQ8H6G2PE3</code></div></div>
          </div>
        </section>

        <section className="landing-cta" id="contact">
          <div className="landing-container landing-cta__inner"><div><span><Code2 size={17} />{t("landing.developer.eyebrow")}</span><h2>{t("landing.cta.title")}</h2><p>{t("landing.cta.body")}</p></div><div><ActionLink href={SANDBOX_URL} size="lg">{t("landing.cta.primary")}<ArrowRight size={17} /></ActionLink><ActionLink href={SALES_URL} size="lg" variant="secondary">{t("landing.cta.secondary")}</ActionLink></div></div>
        </section>
      </main>

      <footer className="landing-footer"><div className="landing-container landing-footer__grid"><div><Brand /><p>{t("landing.footer.rights")}</p><span><i />{t("common.previewData")}</span></div><div><strong>{t("landing.footer.product")}</strong><a href="#product">{t("landing.nav.product")}</a><a href="#networks">{t("landing.nav.networks")}</a><a href="#reliability">{t("landing.nav.reliability")}</a></div><div><strong>{t("landing.footer.resources")}</strong><a href="#developers">{t("common.documentation")}</a><a href="#developers">{t("common.developers")}</a><a href="#top">{t("common.security")}</a></div><div><strong>{t("landing.footer.company")}</strong><a href={SALES_URL}>{t("common.contactSales")}</a><a href="#top">{t("common.pricing")}</a></div></div></footer>
    </div>
  );
}
