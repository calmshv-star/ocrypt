import { defineConfig, devices } from "@playwright/test";

const desktopChrome = devices["Desktop Chrome"];
if (!desktopChrome) throw new Error("Playwright Desktop Chrome device profile is unavailable");

export default defineConfig({
  testDir: "./e2e",
  outputDir: "./test-results",
  fullyParallel: true,
  forbidOnly: Boolean(process.env.CI),
  retries: process.env.CI ? 2 : 0,
  workers: process.env.CI ? 2 : undefined,
  timeout: 30_000,
  expect: { timeout: 7_500 },
  reporter: process.env.CI
    ? [["line"], ["html", { outputFolder: "playwright-report", open: "never" }]]
    : "line",
  use: {
    ...desktopChrome,
    // Release runners may use a managed Chrome installation instead of
    // downloading Playwright's bundled Chromium. Keep the default unchanged
    // unless the runner opts in explicitly (for example, PLAYWRIGHT_CHANNEL=chrome).
    channel: process.env.PLAYWRIGHT_CHANNEL,
    ignoreHTTPSErrors: process.env.PLAYWRIGHT_IGNORE_HTTPS_ERRORS === "true",
    locale: "en-US",
    colorScheme: "light",
    trace: "retain-on-failure",
    screenshot: "only-on-failure",
    video: process.env.PLAYWRIGHT_VIDEO === "off" ? "off" : "retain-on-failure",
  },
});
