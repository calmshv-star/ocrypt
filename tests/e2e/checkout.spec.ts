import AxeBuilder from "@axe-core/playwright";
import { expect, test } from "@playwright/test";

import { expectNoHorizontalOverflow, locales, preferReducedMotion, withLocale } from "./helpers";

const checkoutUrl = process.env.CHECKOUT_E2E_URL;

if (!checkoutUrl) {
  test("hosted checkout fixture is available", () => {
    test.skip(true, "CHECKOUT_E2E_URL is not set");
  });
} else {
  for (const locale of locales) {
    test(`checkout renders a complete ${locale} locale`, async ({ page }) => {
      await preferReducedMotion(page);
      await page.goto(withLocale(checkoutUrl, locale), { waitUntil: "domcontentloaded" });
      await expect(page.locator("html")).toHaveAttribute("lang", locale);
      await expect(page.locator("main")).toBeVisible();

      const amount = page.locator('[data-testid="payment-amount"]');
      const address = page.locator('[data-testid="payment-address"]');
      const network = page.locator('[data-testid="payment-network"]');
      const status = page.locator('[role="status"], [aria-live="polite"], [aria-live="assertive"]').first();
      await expect(amount).toBeVisible();
      await expect(amount).toContainText(/\d/);
      await expect(address).toBeVisible();
      await expect(network).toBeVisible();
      await expect(status).toBeVisible();
      await expect(status).not.toBeEmpty();

      const copyControls = page.locator(
        '[data-testid="copy-address"], [data-testid="copy-amount"], [data-testid="copy-memo"]',
      );
      expect(await copyControls.count()).toBeGreaterThanOrEqual(2);
      for (let index = 0; index < (await copyControls.count()); index += 1) {
        await expect(copyControls.nth(index)).toHaveAccessibleName(/\S+/);
      }

      const accessibility = await new AxeBuilder({ page })
        .withTags(["wcag2a", "wcag2aa", "wcag22aa"])
        .analyze();
      expect(accessibility.violations).toEqual([]);
    });
  }

  test("checkout preserves exact copy values and remains usable at 200% zoom", async ({ page }) => {
    await preferReducedMotion(page);
    await page.setViewportSize({ width: 640, height: 800 });
    await page.goto(withLocale(checkoutUrl, "en"), { waitUntil: "domcontentloaded" });
    const amount = page.locator('[data-testid="payment-amount"]');
    const exactValue = await amount.getAttribute("data-copy-value");
    expect(exactValue).toMatch(/^\d+(\.\d+)?$/);
    expect(exactValue).not.toMatch(/[eE]/);

    await page.evaluate(() => {
      (document.documentElement.style as CSSStyleDeclaration & { zoom: string }).zoom = "2";
    });
    await expectNoHorizontalOverflow(page);
    await expect(page.locator('[data-testid="copy-address"]')).toBeVisible();
    await expect(page.locator('[data-testid="copy-amount"]')).toBeVisible();
  });

  test("partial payment asks only for the durable remainder on the same route", async ({ page }) => {
    await preferReducedMotion(page);
    await page.setViewportSize({ width: 390, height: 844 });
    const partialURL = new URL(withLocale(checkoutUrl, "ru"));
    partialURL.searchParams.set("status", "partially_paid");
    await page.goto(partialURL.toString(), { waitUntil: "domcontentloaded" });

    await expect(page.locator('[data-testid="payment-progress"]')).toContainText("Получено");
    await expect(page.locator('[data-testid="payment-received"]')).toContainText("980 USDT");
    await expect(page.locator('[data-testid="payment-remaining"]')).toContainText("300 USDT");
    await expect(page.locator('[data-testid="payment-amount"]')).toContainText("300 USDT");
    await expect(page.locator('[data-testid="top-up-instruction"]')).toContainText("тот же адрес");
    await expect(page.locator('[data-testid="copy-amount"]')).toBeVisible();
    await expect(page.locator('[data-testid="copy-address"]')).toBeVisible();
    await expect(page.locator('select[aria-label="Выберите маршрут оплаты"]')).toBeDisabled();
    await expectNoHorizontalOverflow(page);
  });

  test("public payment link redeems through the production contract", async ({ page }) => {
    const paymentLinkToken = `pl_${"c".repeat(43)}`;
    const checkoutToken = `cs_${"d".repeat(43)}`;
    const origin = new URL(checkoutUrl).origin;
    const routeID = "20000000-0000-4000-8000-000000000001";
    const expiresAt = new Date(Date.now() + 900_000).toISOString();
    const session = {
      intent_id: "10000000-0000-4000-8000-000000000001",
      order_id: "order_from_browser_contract",
      status: "pending",
      expires_at: expiresAt,
      selected_route_id: routeID,
      routes: [{ id: routeID, provider: "on_chain", network: "tron:mainnet", asset: "usdt-tron", amount: "38.13", address: "TWb4A6kVtQJ4z9Yp2mR7sX8cN1hL5uD3eF" }],
    };
    let redeemIdempotency = "";

    await page.route(`**/v1/public/payment-links/${paymentLinkToken}`, async (route) => {
      await route.fulfill({
        contentType: "application/json",
        body: JSON.stringify({
          name: "Six-month plan",
          amount_minor: "3813",
          currency: "USD",
          currency_scale: 2,
          description: "Subscription renewal",
          allowed_routes: [{ provider: "on_chain", chain_id: "tron:mainnet", asset_id: "usdt-tron" }],
        }),
      });
    });
    await page.route(`**/v1/public/payment-links/${paymentLinkToken}/redeem`, async (route) => {
      redeemIdempotency = route.request().headers()["idempotency-key"] ?? "";
      await route.fulfill({
        status: 201,
        contentType: "application/json",
        body: JSON.stringify({
          intent_id: "10000000-0000-4000-8000-000000000001",
          checkout: { token: checkoutToken, url: `${origin}/checkout?token=${checkoutToken}`, expires_at: expiresAt },
          session,
          success_url: "https://merchant.example/orders/complete",
          cancel_url: "https://merchant.example/cart",
        }),
      });
    });
    await page.route(`**/v1/checkout-sessions/${checkoutToken}`, async (route) => {
      await route.fulfill({ contentType: "application/json", body: JSON.stringify(session), headers: { ETag: '"checkout-browser-contract"' } });
    });

    await page.goto(`${origin}/pay?token=${paymentLinkToken}`, { waitUntil: "domcontentloaded" });
    await expect(page.locator('[data-testid="payment-link-amount"]')).toContainText("38.13 USD");
    await page.locator('[data-testid="redeem-payment-link"]').click();
    await expect(page).toHaveURL(`${origin}/checkout?token=${checkoutToken}`);
    await expect(page.locator('[data-testid="payment-address"]')).toContainText("TWb4A6");
    expect(redeemIdempotency.length).toBeGreaterThanOrEqual(8);
  });
}
