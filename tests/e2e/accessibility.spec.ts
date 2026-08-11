import AxeBuilder from "@axe-core/playwright";
import { expect, test } from "@playwright/test";

import {
  configuredTargets,
  expectNamedInteractiveControls,
  expectNoHorizontalOverflow,
  preferReducedMotion,
} from "./helpers";

const targets = configuredTargets();

test("all required browser targets are configured for a release gate", () => {
  const required = process.env.REQUIRE_E2E_TARGETS === "1";
  const missing = [
    "LANDING_E2E_URL",
    "ADMIN_E2E_URL",
    "ADMIN_MANAGEMENT_E2E_URL",
    "UNMATCHED_E2E_URL",
    "CHECKOUT_E2E_URL",
  ].filter((name) => !process.env[name]);
  if (!required) test.skip(true, "set REQUIRE_E2E_TARGETS=1 in release CI");
  expect(missing).toEqual([]);
});

if (targets.length === 0) {
  test("browser accessibility targets are available", () => {
    test.skip(true, "LANDING_E2E_URL, ADMIN_E2E_URL, and CHECKOUT_E2E_URL are not set");
  });
} else {
  for (const target of targets) {
    test(`${target.name} has no automated WCAG 2.2 A/AA violations`, async ({ page }) => {
      await preferReducedMotion(page);
      await page.goto(target.url, { waitUntil: "domcontentloaded" });
      await expect(page.locator("html")).toHaveAttribute("lang", /\S+/);
      await expect(page.locator("main")).toBeVisible();
      await expect(page.locator("h1").first()).toBeVisible();
      await expectNamedInteractiveControls(page);

      const results = await new AxeBuilder({ page })
        .withTags(["wcag2a", "wcag2aa", "wcag21aa", "wcag22aa"])
        .analyze();
      const summary = results.violations
        .map(
          (violation) =>
            `${violation.id} (${violation.impact ?? "unknown"}): ${violation.nodes.length} node(s); ${violation.nodes
              .slice(0, 3)
              .map((node) => node.target.join(" "))
              .join(", ")}`,
        )
        .join("\n");
      expect(summary, `WCAG violations:\n${summary}`).toBe("");
    });

    test(`${target.name} is keyboard reachable and responsive at 360px`, async ({ page }) => {
      await preferReducedMotion(page);
      await page.setViewportSize({ width: 360, height: 800 });
      await page.goto(target.url, { waitUntil: "domcontentloaded" });
      await expectNoHorizontalOverflow(page);
      await page.keyboard.press("Tab");
      const focus = await page.evaluate(() => ({
        tag: document.activeElement?.tagName,
        hidden: document.activeElement?.getAttribute("aria-hidden"),
      }));
      expect(focus.tag).not.toBe("BODY");
      expect(focus.hidden).not.toBe("true");
    });
  }
}
