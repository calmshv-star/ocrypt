import { useI18n } from "@merchant/i18n";
import { Button, PageHeader, SectionCard } from "@merchant/ui";
import { useQuery } from "@tanstack/react-query";
import { Download, FileSpreadsheet } from "lucide-react";
import { useMemo, useState } from "react";
import { useAdmin } from "./AdminProvider";
import type { DailyTotal, DailyTotalsReport } from "./api/types";
import { downloadCSV, exactMinor, tableCSV } from "./csvExport";
import { downloadXLSX, toXLSX, type XLSXCell } from "./xlsx";

function localDay(date: Date, timeZone: string) {
  try {
    return new Intl.DateTimeFormat("en-CA", { timeZone, year: "numeric", month: "2-digit", day: "2-digit" }).format(date);
  } catch {
    return date.toISOString().slice(0, 10);
  }
}

const DAY = /^\d{4}-\d{2}-\d{2}$/;
const MAX_DAYS = 366;

// Inclusive days between two YYYY-MM-DD dates, by calendar, not by clock.
export function daysInPeriod(from: string, to: string) {
  return Math.round((Date.parse(`${to}T00:00:00Z`) - Date.parse(`${from}T00:00:00Z`)) / 86_400_000) + 1;
}

function browserZone() {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
  } catch {
    return "UTC";
  }
}

// Sums exact minor-unit strings without floating point.
export function addMinor(left: string, right: string) {
  return (BigInt(left) + BigInt(right)).toString();
}

export function totalsByCurrency(rows: DailyTotal[]) {
  const totals = new Map<string, { currency: string; scale: number; count: number; amount: string }>();
  for (const row of rows) {
    const key = `${row.currency}:${row.currency_scale}`;
    const current = totals.get(key) ?? { currency: row.currency, scale: row.currency_scale, count: 0, amount: "0" };
    totals.set(key, { ...current, count: current.count + row.paid_count, amount: addMinor(current.amount, row.paid_amount_minor) });
  }
  return [...totals.values()].sort((a, b) => a.currency.localeCompare(b.currency));
}

// Accounting report (TZ "Отчёты"): paid payments per day and currency at
// the exact invoice amount, in the chosen time zone, exportable as CSV/XLSX.
export function ReportsPage() {
  const { t } = useI18n();
  const admin = useAdmin();
  const [timeZone, setTimeZone] = useState(browserZone);
  const [from, setFrom] = useState(() => localDay(new Date(Date.now() - 29 * 86_400_000), browserZone()));
  const [to, setTo] = useState(() => localDay(new Date(), browserZone()));
  // "Today" belongs to the chosen zone: keep the period ending today when the
  // zone changes, or the last hours of the day in a zone ahead are missed.
  const changeZone = (next: string) => {
    if (to === localDay(new Date(), timeZone)) setTo(localDay(new Date(), next));
    setTimeZone(next);
  };
  const zones = useMemo(() => {
    const supported = typeof Intl.supportedValuesOf === "function" ? Intl.supportedValuesOf("timeZone") : [];
    return [...new Set(["UTC", browserZone(), ...supported])];
  }, []);
  const problem = !DAY.test(from) || !DAY.test(to) ? "reports.periodMissing" : from > to ? "reports.invalidPeriod" : daysInPeriod(from, to) > MAX_DAYS ? "reports.periodTooLong" : null;
  const valid = problem === null;
  const report = useQuery<DailyTotalsReport, Error>({
    queryKey: ["admin", "daily-totals", admin.scope?.tenantId, admin.scope?.merchantId, from, to, timeZone],
    enabled: !admin.preview && admin.sessionState === "ready" && Boolean(admin.scope) && admin.can("payments:read") && valid,
    queryFn: ({ signal }) => admin.clientFor(signal).dailyTotals(admin.scope!, from, to, timeZone, signal)
  });
  const rows = report.data?.data ?? [];
  const totals = totalsByCurrency(rows);
  const table = (): XLSXCell[][] => [
    [t("reports.day"), t("common.currency"), t("reports.paidCount"), t("reports.paidAmount")],
    ...rows.map((row): XLSXCell[] => [row.day, row.currency, { number: String(row.paid_count) }, { number: exactMinor(row.paid_amount_minor, row.currency_scale) }]),
    ...totals.map((total): XLSXCell[] => [t("reports.total"), total.currency, { number: String(total.count) }, { number: exactMinor(total.amount, total.scale) }]),
    [],
    [t("reports.timeZone"), timeZone],
    [t("reports.scope"), admin.scope?.merchantId ?? admin.scope?.tenantId ?? ""],
    [t("reports.generatedAt"), new Date().toISOString()]
  ];
  const name = `daily-totals-${from}-${to}-${timeZone.replaceAll("/", "_")}`;
  return <div className="admin-page" data-testid="reports-page">
    <PageHeader description={t("reports.dailyBody")} eyebrow={<><FileSpreadsheet size={13} />{t("reports.nav")}</>} title={t("reports.dailyTitle")} actions={<span className="admin-export__buttons">
      <Button disabled={rows.length === 0} onClick={() => downloadCSV(`${name}.csv`, tableCSV(table()))} variant="secondary"><Download size={15} />{t("reports.exportCsv")}</Button>
      <Button disabled={rows.length === 0} onClick={() => downloadXLSX(`${name}.xlsx`, toXLSX(t("reports.dailyTitle"), table()))} variant="secondary"><Download size={15} />{t("reports.exportXlsx")}</Button>
    </span>} />
    <SectionCard title={t("reports.period")}>
      <div className="admin-report__filters">
        <label>{t("reports.from")}<input max={to} onChange={(event) => setFrom(event.target.value)} type="date" value={from} /></label>
        <label>{t("reports.to")}<input min={from} onChange={(event) => setTo(event.target.value)} type="date" value={to} /></label>
        <label>{t("reports.timeZone")}<select onChange={(event) => changeZone(event.target.value)} value={timeZone}>{zones.map((zone) => <option key={zone} value={zone}>{zone}</option>)}</select></label>
      </div>
      <p className="admin-report__note">{t("reports.dailyNote")}</p>
    </SectionCard>
    {problem && <p className="admin-live-state" role="alert">{t(problem, { max: MAX_DAYS })}</p>}
    {report.isLoading && <div aria-busy="true" className="admin-route-loading"><span /><span /><span /></div>}
    {report.error && <p className="admin-live-state" role="alert">{t("admin.dataError")}</p>}
    {report.isSuccess && rows.length === 0 && <p className="admin-live-state">{t("reports.empty")}</p>}
    {rows.length > 0 && <SectionCard title={t("reports.dailyTitle")}>
      <table className="admin-report__table">
        <thead><tr><th scope="col">{t("reports.day")}</th><th scope="col">{t("common.currency")}</th><th scope="col">{t("reports.paidCount")}</th><th scope="col">{t("reports.paidAmount")}</th></tr></thead>
        <tbody>{rows.map((row) => <tr key={`${row.day}:${row.currency}:${row.currency_scale}`}><td>{row.day}</td><td>{row.currency}</td><td>{row.paid_count}</td><td className="admin-report__amount">{exactMinor(row.paid_amount_minor, row.currency_scale)}</td></tr>)}</tbody>
        <tfoot>{totals.map((total) => <tr key={`${total.currency}:${total.scale}`}><th scope="row">{t("reports.total")}</th><td>{total.currency}</td><td>{total.count}</td><td className="admin-report__amount">{exactMinor(total.amount, total.scale)}</td></tr>)}</tfoot>
      </table>
    </SectionCard>}
  </div>;
}
