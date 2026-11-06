const { createRequire } = require("node:module");
const { mkdirSync, writeFileSync } = require("node:fs");
const path = require("node:path");

const packagePath = process.env.SDK_BROWSER_PACKAGE;
const baseURL = process.env.APP_PLATFORM_BROWSER_URL;
const cookieName = process.env.APP_PLATFORM_BROWSER_COOKIE_NAME;
const cookieValue = process.env.APP_PLATFORM_BROWSER_COOKIE;
const installation = process.env.APP_PLATFORM_BROWSER_INSTALLATION;
const action = process.env.APP_PLATFORM_BROWSER_ACTION;
const mode = process.env.APP_PLATFORM_BROWSER_MODE;
const evidenceDir = process.env.APP_PLATFORM_BROWSER_EVIDENCE;
const operation = process.env.APP_PLATFORM_BROWSER_OPERATION || "";

if (!packagePath || !path.isAbsolute(packagePath)) {
  throw new Error("SDK_BROWSER_PACKAGE must name the Playwright package.json");
}
if (!baseURL || !cookieName || !cookieValue) {
  throw new Error("lifecycle browser session is incomplete");
}
if (!["repair", "restore"].includes(action)) {
  throw new Error("APP_PLATFORM_BROWSER_ACTION must be repair or restore");
}
if (!["confirm", "progress"].includes(mode)) {
  throw new Error("APP_PLATFORM_BROWSER_MODE must be confirm or progress");
}
if (!evidenceDir || !path.isAbsolute(evidenceDir)) {
  throw new Error("APP_PLATFORM_BROWSER_EVIDENCE must be absolute");
}
const target = new URL(baseURL);
if (
  target.protocol !== "http:" ||
  !["127.0.0.1", "::1", "[::1]"].includes(target.hostname)
) {
  throw new Error("lifecycle browser target must be owned loopback HTTP");
}
mkdirSync(evidenceDir, { recursive: true });

const requirePlaywright = createRequire(packagePath);
const { chromium } = requirePlaywright("playwright");

function operationIDFromURL(value) {
  const match = String(value).match(/[?&]operation=([0-9a-f-]{36})(?:&|$)/);
  return match?.[1] ?? "";
}

async function firstEnabled(locator) {
  for (let index = 0; index < (await locator.count()); index += 1) {
    const candidate = locator.nth(index);
    if (await candidate.isEnabled()) return candidate;
  }
  throw new Error(`no enabled control found for ${locator}`);
}

async function main() {
  const browser = await chromium.launch({ headless: true });
  const context = await browser.newContext({
    viewport: { width: 1440, height: 900 },
    colorScheme: "light",
  });
  try {
    await context.addCookies([
      {
        name: cookieName,
        value: cookieValue,
        url: target.origin,
        httpOnly: true,
        sameSite: "Lax",
      },
    ]);
    await context.addInitScript(() => {
      localStorage.setItem("rp_locale", "en-US");
      localStorage.setItem("rp_theme", "light");
    });
    const page = await context.newPage();
    page.setDefaultTimeout(30_000);
    const errors = [];
    page.on("pageerror", (error) => errors.push(error.message));
    page.on("console", (message) => {
      if (message.type() === "error") errors.push(message.text());
    });
    const settingsURL =
      `${target.origin}/manage/apps/installations/` +
      encodeURIComponent(installation);

    if (mode === "confirm") {
      await page.goto(settingsURL, { waitUntil: "networkidle" });
      await page.getByText("Application settings", { exact: true }).waitFor();
      const reviewName =
        action === "repair" ? "Review repair" : "Review restore";
      const confirmName =
        action === "repair" ? "Repair application" : "Restore application data";
      const boundary =
        action === "repair"
          ? "creates a fresh checkpoint"
          : "Writes after that checkpoint may be lost";
      await (await firstEnabled(
        page.getByRole("button", { name: reviewName }),
      )).click();
      const dialog = page.getByRole("dialog");
      await dialog.waitFor();
      await page.waitForFunction(() => {
        const current = document.querySelector('[role="dialog"]');
        return current && getComputedStyle(current).opacity === "1";
      });
      const text = await dialog.innerText();
      if (!text.includes(boundary)) {
        throw new Error(`${action} review boundary missing: ${text}`);
      }
      await page.screenshot({
        path: path.join(evidenceDir, `${action}-review.png`),
        fullPage: false,
      });
      await dialog.getByRole("button", { name: confirmName }).click();
      await page.waitForURL(/[?&]operation=[0-9a-f-]{36}(?:&|$)/);
      const operationID = operationIDFromURL(page.url());
      if (!operationID) {
        throw new Error(`confirmed ${action} did not expose its operation`);
      }
      await page.getByText("Operation progress", { exact: true }).waitFor();
      await page.screenshot({
        path: path.join(evidenceDir, `${action}-confirmed.png`),
        fullPage: true,
      });
      const result = { mode, action, operationID, stage: "confirmed" };
      writeFileSync(
        path.join(evidenceDir, `${action}-confirm.json`),
        JSON.stringify(result, null, 2) + "\n",
      );
      console.log(`APP_LIFECYCLE_BROWSER_CONFIRMED ${JSON.stringify(result)}`);
    } else {
      if (!/^[0-9a-f-]{36}$/.test(operation)) {
        throw new Error("progress mode requires an operation ID");
      }
      await page.goto(
        `${target.origin}/manage/apps?operation=${encodeURIComponent(operation)}`,
        { waitUntil: "networkidle" },
      );
      await page.getByText("Operation progress", { exact: true }).waitFor();
      await page
        .getByText("Operation complete", { exact: true })
        .waitFor({ timeout: 180_000 });
      await page.screenshot({
        path: path.join(evidenceDir, `${action}-complete.png`),
        fullPage: true,
      });
      await page.goto(settingsURL, { waitUntil: "networkidle" });
      await page.getByText("Ready", { exact: true }).first().waitFor();
      await page.screenshot({
        path: path.join(evidenceDir, `${action}-ready.png`),
        fullPage: true,
      });
      const result = { mode, action, operationID: operation, stage: "complete" };
      writeFileSync(
        path.join(evidenceDir, `${action}-progress.json`),
        JSON.stringify(result, null, 2) + "\n",
      );
      console.log(`APP_LIFECYCLE_BROWSER_COMPLETE ${JSON.stringify(result)}`);
    }
    if (errors.length) {
      throw new Error(`${action} browser errors: ${errors.join(" | ")}`);
    }
  } finally {
    await browser.close();
  }
}

main().catch((error) => {
  console.error(error);
  process.exitCode = 1;
});
