import { expect, test } from "@playwright/test";

import { preferReducedMotion } from "./helpers";

const unmatchedUrl = process.env.UNMATCHED_E2E_URL;

if (!unmatchedUrl) {
  test("unmatched operations fixture is available", () => {
    test.skip(true, "UNMATCHED_E2E_URL is not set");
  });
} else {
  test("AI ranking is advisory and cannot resolve a case", async ({ page }) => {
    await preferReducedMotion(page);
    await page.goto(unmatchedUrl, { waitUntil: "domcontentloaded" });
    await expect(page.locator('[data-testid="unmatched-queue"], .unmatched-queue')).toBeVisible();
    await page.locator('[data-testid="unmatched-case"], .unmatched-case').first().click();
    await expect(page.locator('[data-testid="deterministic-candidates"], .unmatched-candidates')).toBeVisible();
    const advisory = page.locator('[data-testid="ai-advisory"], .mp-ai-advisory');
    await expect(advisory).toBeVisible();
    await expect(advisory).toContainText(/advis|rank|recommend/i);
    await expect(page.locator('[data-testid="ai-resolve"]')).toHaveCount(0);
  });

  test("manual cross-asset or shortfall resolution requires a reason then removes the credited case", async ({ page }) => {
    await preferReducedMotion(page);
    await page.goto(unmatchedUrl, { waitUntil: "domcontentloaded" });
    await page.locator('[data-testid="unmatched-case"]').first().click();
    await page.locator('[data-testid="accept-cross-asset"]').check();
    const submit = page.locator('[data-testid="request-resolution"]');
    await expect(submit).toBeDisabled();
    await page.locator('[data-testid="resolution-reason"]').fill("Verified customer claim and chain evidence");
    await expect(submit).toBeEnabled();
    await submit.click();
    await expect(page.locator('[data-testid="resolution-status"]')).toHaveCount(0);
    await expect(page.getByText(/verification in progress/i)).toHaveCount(0);
    await expect(submit).toHaveCount(0);
  });
}
