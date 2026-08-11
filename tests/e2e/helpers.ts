import { expect, type Page } from "@playwright/test";

export const locales = ["en", "zh-CN", "es", "fr", "de", "ru"] as const;

export type Target = { name: string; url: string };

export function configuredTargets(): Target[] {
  return [
    ["landing", process.env.LANDING_E2E_URL],
    ["admin", process.env.ADMIN_E2E_URL],
    ["checkout", process.env.CHECKOUT_E2E_URL],
  ]
    .filter((entry): entry is [string, string] => Boolean(entry[1]))
    .map(([name, url]) => ({ name, url }));
}

export function withLocale(rawUrl: string, locale: string): string {
  const url = new URL(rawUrl);
  if (process.env.CHECKOUT_LOCALE_MODE === "path") {
    url.pathname = `/${locale}${url.pathname.startsWith("/") ? url.pathname : `/${url.pathname}`}`;
  } else {
    url.searchParams.set(process.env.CHECKOUT_LOCALE_PARAM ?? "locale", locale);
  }
  return url.toString();
}

export async function preferReducedMotion(page: Page): Promise<void> {
  await page.emulateMedia({ reducedMotion: "reduce" });
}

export async function expectNoHorizontalOverflow(page: Page): Promise<void> {
  const dimensions = await page.evaluate(() => ({
    documentWidth: document.documentElement.scrollWidth,
    viewportWidth: document.documentElement.clientWidth,
  }));
  expect(dimensions.documentWidth).toBeLessThanOrEqual(dimensions.viewportWidth + 1);
}

export async function expectNamedInteractiveControls(page: Page): Promise<void> {
  const unnamed = await page.locator("button, a[href], input, select, textarea").evaluateAll((elements) =>
    elements
      .filter((element) => {
        const node = element as HTMLElement;
        if (node.hidden || node.getAttribute("aria-hidden") === "true") return false;
        const style = getComputedStyle(node);
        if (style.display === "none" || style.visibility === "hidden") return false;
        const labelledBy = node.getAttribute("aria-labelledby");
        const label = node.getAttribute("aria-label") ?? "";
        const text = node.textContent ?? "";
        const title = node.getAttribute("title") ?? "";
        const input = node as HTMLInputElement;
        const nativeLabels = "labels" in input && input.labels ? input.labels.length : 0;
        return !(labelledBy || label.trim() || text.trim() || title.trim() || input.placeholder?.trim() || nativeLabels);
      })
      .map((element) => element.outerHTML.slice(0, 200)),
  );
  expect(unnamed, `interactive controls without an accessible name: ${unnamed.join("\n")}`).toEqual([]);
}
