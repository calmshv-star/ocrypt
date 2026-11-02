import { useI18n, type MessageKey } from "@merchant/i18n";
import { Button, PRODUCT_NAME, Select, ThemeToggle } from "@merchant/ui";
import { useQuery } from "@tanstack/react-query";
import { Store } from "lucide-react";
import { useMemo, useState, type FormEvent } from "react";
import type { AdminClient, SignupOptions } from "./api/client";

// ISO 3166-1 alpha-2 codes. Country names come from Intl.DisplayNames, so they
// follow the interface language; the server decides which countries are open.
const COUNTRIES = "AD AE AF AG AI AL AM AO AR AT AU AW AZ BA BB BD BE BF BG BH BI BJ BM BN BO BR BS BT BW BY BZ CA CD CF CG CH CI CL CM CN CO CR CV CY CZ DE DJ DK DM DO DZ EC EE EG ER ES ET FI FJ FM FR GA GB GD GE GH GI GM GN GQ GR GT GW GY HK HN HR HT HU ID IE IL IN IQ IS IT JM JO JP KE KG KH KI KM KN KR KW KY KZ LA LB LC LI LK LR LS LT LU LV LY MA MC MD ME MG MH MK ML MM MN MO MR MT MU MV MW MX MY MZ NA NE NG NI NL NO NP NR NZ OM PA PE PG PH PK PL PS PT PW PY QA RO RS RW SA SB SC SE SG SI SK SL SM SN SO SR SS ST SV SZ TD TG TH TJ TL TM TN TO TR TT TV TW TZ UA UG US UY UZ VA VC VE VG VN VU WS XK YE ZA ZM ZW".split(" ");

export function SignupPage({ client, locale, redirect = (url) => window.location.assign(url) }: { client: AdminClient; locale: string; redirect?: (url: string) => void }) {
  const { t, setLocale, locales, localeNames, locale: current } = useI18n();
  const options = useQuery<SignupOptions>({ queryKey: ["signup-options"], queryFn: ({ signal }) => client.signupOptions(signal), staleTime: 60_000 });
  const [company, setCompany] = useState("");
  const [country, setCountry] = useState("");
  const [business, setBusiness] = useState("");
  const [website, setWebsite] = useState("");
  const [accepted, setAccepted] = useState(false);
  const [state, setState] = useState<"idle" | "submitting" | "rejected">("idle");
  const countries = useMemo(() => {
    const names = typeof Intl.DisplayNames === "function" ? new Intl.DisplayNames([locale], { type: "region" }) : undefined;
    return COUNTRIES.map((code) => ({ code, name: names?.of(code) ?? code })).sort((a, b) => a.name.localeCompare(b.name, locale));
  }, [locale]);
  const open = options.data?.enabled ? options.data : undefined;
  const websiteValid = website.trim() === "" || /^https:\/\/[^\s/?#]+(\/\S*)?$/.test(website.trim());
  const valid = company.trim().length >= 2 && company.trim().length <= 120 && country !== "" && business !== "" && websiteValid && accepted;
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (!open || !valid || state === "submitting") return;
    setState("submitting");
    try {
      const location = await client.beginSignup({ company_name: company.trim(), country, business_type: business, ...(website.trim() ? { website: website.trim() } : {}), terms_version: open.termsVersion });
      redirect(location);
    } catch {
      setState("rejected");
    }
  };
  const label = (type: string) => {
    const key = `signup.business.${type}` as MessageKey;
    const text = t(key);
    return text === key ? type : text;
  };
  return <main className="admin-access">
    <div className="admin-access__controls"><Select aria-label={t("common.locale")} onChange={(event) => setLocale(event.target.value as typeof current)} value={current}>{locales.map((item) => <option key={item} value={item}>{localeNames[item]}</option>)}</Select><ThemeToggle label={t("common.theme")} /></div>
    <section aria-busy={options.isLoading || undefined} className="admin-signup">
      <div className="admin-access__brand"><span aria-hidden="true" className="mp-brand__mark"><span/><span/></span><strong>{PRODUCT_NAME}</strong></div>
      <div className="admin-access__content"><span aria-hidden="true" className="admin-access__shield"><Store size={24}/></span><div><h1>{t("signup.title")}</h1><p>{open ? t("signup.body") : options.isLoading ? "" : t("signup.closed")}</p></div></div>
      {open && <form className="admin-signup__form" noValidate onSubmit={submit}>
        <label>{t("signup.company")}<input autoComplete="organization" maxLength={120} name="company" onChange={(event) => setCompany(event.target.value)} required value={company} /></label>
        <label>{t("signup.country")}<Select name="country" onChange={(event) => setCountry(event.target.value)} required value={country}><option value="">{t("signup.countryPlaceholder")}</option>{countries.map((item) => <option key={item.code} value={item.code}>{item.name}</option>)}</Select></label>
        <label>{t("signup.business")}<Select name="business" onChange={(event) => setBusiness(event.target.value)} required value={business}><option value="">—</option>{open.businessTypes.map((type) => <option key={type} value={type}>{label(type)}</option>)}</Select></label>
        <label>{t("signup.website")}<input aria-describedby="signup-website-hint" aria-invalid={!websiteValid || undefined} autoComplete="url" inputMode="url" maxLength={255} name="website" onChange={(event) => setWebsite(event.target.value)} placeholder="https://" value={website} /><small id="signup-website-hint">{t("signup.websiteHint")}</small></label>
        <label className="admin-signup__terms"><input checked={accepted} name="terms" onChange={(event) => setAccepted(event.target.checked)} required type="checkbox" />{t("signup.terms", { version: open.termsVersion })}</label>
        <p className="admin-signup__note">{t("signup.mfaNote")}</p>
        {state === "rejected" && <p role="alert">{t("signup.rejected")}</p>}
        <Button disabled={!valid || state === "submitting"} type="submit">{state === "submitting" ? t("signup.submitting") : t("signup.submit")}</Button>
      </form>}
      <a className="admin-signup__signin" href={client.loginURL("/")}>{t("signup.haveAccount")}</a>
    </section>
  </main>;
}
