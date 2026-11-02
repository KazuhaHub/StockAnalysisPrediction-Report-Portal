const { once } = require("node:events");
const { createRequire } = require("node:module");
const { mkdirSync, writeFileSync } = require("node:fs");
const path = require("node:path");
const { execFileSync, spawn } = require("node:child_process");

const root = path.resolve(__dirname, "..");
const packagePath = process.env.SDK_BROWSER_PACKAGE;
if (!packagePath || !path.isAbsolute(packagePath)) {
  throw new Error("SDK_BROWSER_PACKAGE must name the Playwright package.json");
}
const requirePlaywright = createRequire(packagePath);
const { chromium } = requirePlaywright("playwright");
const axePath = requirePlaywright.resolve("axe-core/axe.min.js");
const output = path.resolve(
  process.env.APP_INSTALL_BROWSER_EVIDENCE ||
    path.join(root, "docs/app-platform/handoff/evidence/platform-install"),
);
mkdirSync(output, { recursive: true });

function startFixture(scenario = "install") {
  const fixture = spawn(
    "python3",
    ["-B", "scripts/app-install-browser-fixture.py", "--scenario", scenario],
    {
      cwd: root,
      env: {
        ...process.env,
        GOWORK: "off",
        GOTOOLCHAIN: "go1.26.10",
        GOMAXPROCS: "2",
        TEST_POSTGRES_DSN: "",
      },
      stdio: ["ignore", "pipe", "pipe"],
    },
  );
  let outputLog = "";
  fixture.stdout.on("data", (chunk) => {
    outputLog += chunk;
  });
  fixture.stderr.on("data", (chunk) => {
    outputLog += chunk;
  });
  return { fixture, outputLog: () => outputLog };
}

async function fixtureUrl(fixture, outputLog) {
  return new Promise((resolve, reject) => {
    let buffered = "";
    const timeout = setTimeout(
      () => finish(new Error("installation fixture startup timed out")),
      240_000,
    );
    function finish(error, url) {
      clearTimeout(timeout);
      fixture.stdout.off("data", read);
      fixture.off("exit", exited);
      if (error) reject(error);
      else resolve(url);
    }
    function read(chunk) {
      buffered += chunk;
      const match = buffered.match(/^PROBE_URL=(http:\/\/127\.0\.0\.1:\d+\/manage\/apps)$/m);
      if (match) finish(null, match[1]);
    }
    function exited(code) {
      finish(
        new Error(
          `installation fixture exited before readiness (${code})\n${outputLog()}`,
        ),
      );
    }
    fixture.stdout.on("data", read);
    fixture.once("exit", exited);
  });
}

async function closeFixture(fixture) {
  if (fixture.exitCode !== null) return;
  fixture.kill("SIGINT");
  await Promise.race([
    once(fixture, "exit"),
    new Promise((resolve) => setTimeout(resolve, 20_000)),
  ]);
  if (fixture.exitCode === null) {
    fixture.kill("SIGTERM");
    await once(fixture, "exit");
  }
}

async function auditDialog(page) {
  await page.addScriptTag({ path: axePath });
  return page.evaluate(async () => {
    const dialog = document.querySelector('[role="dialog"]');
    if (!dialog) return [{ id: "missing-dialog", impact: "critical" }];
    const result = await window.axe.run(dialog, {
      runOnly: { type: "tag", values: ["wcag2a", "wcag2aa"] },
    });
    return result.violations.map((violation) => ({
      id: violation.id,
      impact: violation.impact,
      nodes: violation.nodes.length,
      targets: violation.nodes.map((node) => node.target.join(" ")),
    }));
  });
}

async function openReview(page, selectNoAudience = true, rowText = "fixture-publisher") {
  await page.getByRole("tab").nth(1).click();
  await page.getByText(rowText, { exact: false }).first().waitFor({
    timeout: 20_000,
  });
  const row = page
    .getByRole("row")
    .filter({ hasText: rowText })
    .first();
  await row.waitFor({ timeout: 20_000 });
  if (selectNoAudience) {
    await page.getByRole("checkbox").first().click();
  }
  const reviewButton = row.getByRole("button").first();
  await reviewButton.click();
  const dialog = page.getByRole("dialog");
  await dialog.waitFor();
  return { dialog, reviewButton };
}

async function scenario(browser, url, options) {
  const context = await browser.newContext({
    viewport: options.viewport,
    colorScheme: options.dark ? "dark" : "light",
  });
  const page = await context.newPage();
  page.setDefaultTimeout(15_000);
  const errors = [];
  page.on("pageerror", (error) => errors.push(error.message));
  page.on("console", (message) => {
    if (message.type() === "error") errors.push(message.text());
  });
  try {
    await page.goto(url, { waitUntil: "networkidle" });
    await page.getByRole("button", { name: options.language, exact: true }).click();
    if (options.dark) {
      await page.getByRole("button", { name: "Toggle theme", exact: true }).click();
    }
    await page.getByRole("tab").nth(1).waitFor();
    const { dialog, reviewButton } = await openReview(
      page,
      options.selectNoAudience !== false,
      options.rowText,
    );
    await page.waitForTimeout(400);
    const dialogBox = await dialog.boundingBox();
    const geometry = await page.evaluate(() => ({
      scrollWidth: document.documentElement.scrollWidth,
      width: window.innerWidth,
      activeInside: !!document.activeElement?.closest('[role="dialog"]'),
    }));
    if (
      geometry.scrollWidth > geometry.width + 1 ||
      !dialogBox ||
      dialogBox.x < 0 ||
      dialogBox.x + dialogBox.width > geometry.width + 1 ||
      !geometry.activeInside
    ) {
      throw new Error(`unsafe ${options.name} geometry or focus`);
    }
    const violations = await auditDialog(page);
    const serious = violations.filter(
      (violation) => violation.impact === "critical" || violation.impact === "serious",
    );
    if (serious.length) {
      throw new Error(`axe violations in ${options.name}: ${JSON.stringify(serious)}`);
    }
    const base = path.join(output, options.name);
    await page.screenshot({ path: `${base}-review.png`, fullPage: false });
    await dialog.getByRole("button").first().click();
    await dialog.waitFor({ state: "hidden" });
    if (
      !(await reviewButton.evaluate(
        (element) => document.activeElement === element,
      ))
    ) {
      throw new Error(`${options.name} did not restore trigger focus`);
    }
    if (errors.length) {
      throw new Error(`${options.name} console/page errors: ${errors.join(" | ")}`);
    }
    return { name: options.name, violations, height: page.viewportSize().height };
  } catch (error) {
    await page.screenshot({
      path: path.join(output, `${options.name}-failure.png`),
      fullPage: true,
    });
    throw error;
  } finally {
    await context.close();
  }
}

async function installScenario(browser, url) {
  const context = await browser.newContext({ viewport: { width: 1440, height: 900 } });
  const page = await context.newPage();
  page.setDefaultTimeout(20_000);
  try {
    await page.goto(url, { waitUntil: "networkidle" });
    await page.getByRole("tab").nth(1).click();
    const row = page
      .getByRole("row")
      .filter({ hasText: "fixture-publisher" });
    await page.getByRole("checkbox").first().click();
    await row.getByRole("button").first().click();
    const dialog = page.getByRole("dialog");
    await dialog.waitFor();
    await dialog.getByRole("button").last().click();
    await page.waitForURL(/[?&]operation=[0-9a-f-]{36}(?:&|$)/);
    await page.getByText("Operation complete", { exact: true }).waitFor({
      timeout: 60_000,
    });
    const body = await page.locator("body").innerText();
    if (!body.includes("Installed or operation in progress")) {
      throw new Error("completed operation did not refresh installed state");
    }
    await page.screenshot({
      path: path.join(output, "install-complete-1440-en-light.png"),
      fullPage: true,
    });
    return { operation: new URL(page.url()).searchParams.get("operation") };
  } finally {
    await context.close();
  }
}

async function uninstallScenario(browser, url) {
  const context = await browser.newContext({
    viewport: { width: 1440, height: 900 },
  });
  const page = await context.newPage();
  page.setDefaultTimeout(20_000);
  try {
    await page.goto(url, { waitUntil: "networkidle" });
    await page.getByRole("tab").nth(1).click();
    const row = page
      .getByRole("row")
      .filter({ hasText: "fixture-publisher" })
      .first();
    await row.waitFor();
    const trigger = row.getByRole("button").first();
    await trigger.click();
    const dialog = page.getByRole("dialog");
    await dialog.waitFor();
    await page.waitForTimeout(400);
    const text = await dialog.innerText();
    if (!text.includes("other plugins are unaffected")) {
      throw new Error("uninstall review did not state its isolation boundary");
    }
    const geometry = await page.evaluate(() => ({
      scrollWidth: document.documentElement.scrollWidth,
      width: window.innerWidth,
      activeInside: !!document.activeElement?.closest('[role="dialog"]'),
    }));
    const box = await dialog.boundingBox();
    if (
      geometry.scrollWidth > geometry.width + 1 ||
      !box ||
      box.x < 0 ||
      box.x + box.width > geometry.width + 1 ||
      !geometry.activeInside
    ) {
      throw new Error("unsafe uninstall review geometry or focus");
    }
    const violations = (await auditDialog(page)).filter(
      (violation) =>
        violation.impact === "critical" || violation.impact === "serious",
    );
    if (violations.length) {
      throw new Error(`uninstall review axe: ${JSON.stringify(violations)}`);
    }
    await page.screenshot({
      path: path.join(output, "uninstall-review-1440-en-light.png"),
      fullPage: false,
    });
    await dialog.getByRole("button").first().click();
    await dialog.waitFor({ state: "hidden" });
    if (!(await trigger.evaluate((element) => document.activeElement === element))) {
      throw new Error("uninstall review did not restore trigger focus");
    }
    return { violations };
  } finally {
    await context.close();
  }
}

async function main() {
  let browser;
  try {
    browser = await chromium.launch({ headless: true });
    const report = {
      gitCommit: execFileSync("git", ["rev-parse", "HEAD"], {
        cwd: root,
        encoding: "utf8",
      }).trim(),
      scenarios: [],
      install: null,
      uninstall: null,
    };
    async function withFixture(mode, run) {
      const { fixture, outputLog } = startFixture(mode);
      try {
        return await run(await fixtureUrl(fixture, outputLog));
      } finally {
        await closeFixture(fixture);
      }
    }
    await withFixture("install", async (url) => {
      for (const options of [
        {
          name: "install-review-1440-en-light",
          language: "English",
          dark: false,
          viewport: { width: 1440, height: 900 },
        },
        {
          name: "install-review-320-zh-cn-dark",
          language: "Simplified Chinese",
          dark: true,
          viewport: { width: 320, height: 800 },
        },
        {
          name: "install-review-390-zh-tw-light",
          language: "Traditional Chinese",
          dark: false,
          viewport: { width: 390, height: 844 },
        },
      ]) {
        report.scenarios.push(await scenario(browser, url, options));
      }
      report.install = await installScenario(browser, url);
      report.uninstall = await uninstallScenario(browser, url);
    });
    await withFixture("updateable", async (url) => {
      report.scenarios.push(
        await scenario(browser, url, {
          name: "update-review-1440-en-light",
          language: "English",
          dark: false,
          viewport: { width: 1440, height: 900 },
          selectNoAudience: false,
          rowText: "1.0.0-beta.2",
        }),
      );
    });
    await withFixture("disabled", async (url) => {
      report.scenarios.push(
        await scenario(browser, url, {
          name: "enable-review-1440-en-light",
          language: "English",
          dark: false,
          viewport: { width: 1440, height: 900 },
          selectNoAudience: false,
        }),
      );
    });
    report.accepted = true;
    writeFileSync(
      path.join(output, "acceptance.json"),
      JSON.stringify(report, null, 2) + "\n",
    );
    console.log("APP_INSTALL_BROWSER_ACCEPTED");
    console.log(JSON.stringify(report));
  } finally {
    await browser?.close();
  }
}

main().catch((error) => {
  console.error(error);
  process.exitCode = 1;
});
