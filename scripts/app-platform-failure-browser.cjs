const { createRequire } = require("node:module");
const { createServer } = require("node:http");
const { existsSync, mkdirSync, readFileSync, statSync, writeFileSync } = require("node:fs");
const path = require("node:path");

const root = path.resolve(__dirname, "..");
const packagePath = process.env.SDK_BROWSER_PACKAGE;
const evidenceDir = process.env.APP_PLATFORM_FAILURE_BROWSER_EVIDENCE;
const dist = path.resolve(
  process.env.APP_PLATFORM_FAILURE_DIST ||
    path.join(root, "internal/web/dist"),
);
if (!packagePath || !path.isAbsolute(packagePath)) {
  throw new Error("SDK_BROWSER_PACKAGE must name the Playwright package.json");
}
if (!evidenceDir || !path.isAbsolute(evidenceDir)) {
  throw new Error("APP_PLATFORM_FAILURE_BROWSER_EVIDENCE must be absolute");
}
if (!existsSync(path.join(dist, "index.html"))) {
  throw new Error("production SPA build is unavailable");
}
mkdirSync(evidenceDir, { recursive: true });

const requirePlaywright = createRequire(packagePath);
const { chromium } = requirePlaywright("playwright");
const axePath = requirePlaywright.resolve("axe-core/axe.min.js");

const mime = {
  ".css": "text/css; charset=utf-8",
  ".html": "text/html; charset=utf-8",
  ".js": "text/javascript; charset=utf-8",
  ".json": "application/json; charset=utf-8",
  ".png": "image/png",
  ".svg": "image/svg+xml",
  ".woff": "font/woff",
  ".woff2": "font/woff2",
};

const installationIDs = [
  "11111111-1111-4111-8111-111111111111",
  "22222222-2222-4222-8222-222222222222",
  "33333333-3333-4333-8333-333333333333",
  "44444444-4444-4444-8444-444444444444",
];

function json(route, status, value) {
  return route.fulfill({
    status,
    contentType: "application/json; charset=utf-8",
    body: JSON.stringify(value),
  });
}

function launchResponse(installationID, mode) {
  const release = "a".repeat(64);
  const frontend = "b".repeat(64);
  return {
    source:
      `/app-documents/${installationID}/1/1/${release}/${frontend}/index.html`,
    binding: {
      installationId: installationID,
      generation: "1",
      policyRevision: "1",
      releaseSha256: release,
    },
    capabilities: [],
    applicationId: "failure-fixture",
    routes: [{ id: "home", path: "/", layout: "document" }],
    layout: "document",
    timeZone: "UTC",
    backendAvailable: mode === "backend-stopped",
  };
}

async function installFixtureRoutes(context, options) {
  let launches = 0;
  let statusReads = 0;
  await context.route("**/*", async (route) => {
    const request = route.request();
    const url = new URL(request.url());
    if (url.pathname === "/api/me") {
      return json(route, 200, {
        user: options.admin ? "failure-admin" : "failure-reader",
        admin: options.admin,
        perms: {},
      });
    }
    if (url.pathname === "/api/apps") {
      return json(route, 200, { apps: [], builtin_apps: [], order: [] });
    }
    if (url.pathname === "/api/app-platform/apps") {
      return json(route, 200, {
        items: options.directory ? [
          {
            installationId: installationIDs[0],
            applicationId: "pending-fixture",
            state: "pending",
            openable: false,
          },
          {
            installationId: installationIDs[1],
            applicationId: "disabled-fixture",
            state: "disabled",
            openable: false,
          },
          {
            installationId: installationIDs[2],
            applicationId: "attention-fixture",
            state: "attention",
            openable: false,
          },
        ] : [],
        nextCursor: "",
      });
    }
    const statusMatch = url.pathname.match(
      /^\/api\/app-platform\/installations\/([0-9a-f-]{36})\/status$/,
    );
    if (statusMatch) {
      statusReads += 1;
      const statuses = {
        [installationIDs[0]]: {
          state: "pending",
          openable: false,
          action: "install",
          stage: "prepare_data",
          control_paused: false,
          timeline: [{
            at: "2026-11-04T12:00:00Z",
            event: "stage",
            stage: "prepare_data",
            private_actor: "private-audit",
            endpoint: "/run/private.sock",
          }],
        },
        [installationIDs[1]]: {
          state: "disabled",
          openable: false,
          action: "disable",
          stage: "complete",
          control_paused: false,
          timeline: [],
        },
        [installationIDs[2]]: {
          state: "attention",
          openable: false,
          control_paused: true,
          timeline: [],
        },
      };
      const status = statuses[statusMatch[1]];
      return status
        ? json(route, 200, status)
        : json(route, 404, { error: "private status diagnostic" });
    }
    const launchMatch = url.pathname.match(
      /^\/api\/app-platform\/installations\/([0-9a-f-]{36})\/launch$/,
    );
    if (launchMatch) {
      launches += 1;
      if (options.mode === "denied") {
        return json(route, 404, { error: "private authorization diagnostic" });
      }
      if (options.mode === "backend-stopped") {
        return json(route, 503, { error: "private backend diagnostic" });
      }
      return json(
        route,
        200,
        launchResponse(launchMatch[1], options.mode),
      );
    }
    if (url.pathname.startsWith("/app-documents/")) {
      const script =
        options.mode === "ready-timeout"
          ? ""
          : `window.parent.postMessage({type:"app:hello",protocols:["portal.app.bridge/1-dev.1"],childNonce:"BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"},"*");`;
      return route.fulfill({
        status: 200,
        contentType: "text/html; charset=utf-8",
        headers: {
          "Content-Security-Policy":
            "default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'",
        },
        body:
          "<!doctype html><meta charset=\"utf-8\">" +
          "<h1>PRIVATE APP CONTENT</h1>" +
          `<script>${script}</script>`,
      });
    }
    if (url.pathname.startsWith("/api/")) {
      return json(route, 200, {});
    }
    return route.continue();
  });
  return { launches: () => launches, statusReads: () => statusReads };
}

async function audit(page) {
  await page.addScriptTag({ path: axePath });
  return page.evaluate(async () => {
    const main = document.querySelector("main");
    if (!main) return [{ id: "missing-main", impact: "critical", nodes: 1, targets: [] }];
    const result = await window.axe.run(main, {
      runOnly: { type: "tag", values: ["wcag2a", "wcag2aa"] },
    });
    return result.violations
      .filter((violation) =>
        violation.impact === "critical" || violation.impact === "serious",
      )
      .map((violation) => ({
        id: violation.id,
        impact: violation.impact,
        nodes: violation.nodes.length,
        targets: violation.nodes.map((node) => node.target.join(" ")),
      }));
  });
}

async function contextFor(browser, options) {
  const context = await browser.newContext({
    viewport: { width: 1440, height: 900 },
    colorScheme: "light",
  });
  await context.addInitScript(() => {
    try {
      localStorage.setItem("rp_locale", "en-US");
      localStorage.setItem("rp_theme", "light");
    } catch {
      // Sandboxed app documents have an opaque origin and no storage.
    }
  });
  const launches = await installFixtureRoutes(context, options);
  const page = await context.newPage();
  page.setDefaultTimeout(20_000);
  const errors = [];
  page.on("pageerror", (error) => errors.push(error.message));
  page.on("console", (message) => {
    if (message.type() !== "error") return;
    const expectedLaunchRefusal =
      ["denied", "backend-stopped"].includes(options.mode) &&
      message.location().url.includes(
        `/api/app-platform/installations/${installationIDs[3]}/launch`,
      ) &&
      /Failed to load resource: the server responded with a status of (404|503)/.test(
        message.text(),
      );
    if (!expectedLaunchRefusal) errors.push(message.text());
  });
  return { context, page, launches, errors };
}

async function closeScenario(context, errors, name) {
  await context.close();
  if (errors.length) {
    throw new Error(`${name} browser errors: ${errors.join(" | ")}`);
  }
}

async function main() {
  const server = createServer((request, response) => {
    const url = new URL(request.url, "http://127.0.0.1");
    let pathname;
    try {
      pathname = decodeURIComponent(url.pathname);
    } catch {
      response.writeHead(404).end();
      return;
    }
    const candidate = path.resolve(dist, "." + pathname);
    const file =
      candidate.startsWith(dist + path.sep) &&
      existsSync(candidate) &&
      statSync(candidate).isFile()
        ? candidate
        : path.join(dist, "index.html");
    response.setHeader(
      "Content-Type",
      mime[path.extname(file)] || "application/octet-stream",
    );
    response.setHeader("Cache-Control", "no-store");
    response.end(readFileSync(file));
  });
  await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
  const address = server.address();
  const base = `http://127.0.0.1:${address.port}`;
  const browser = await chromium.launch({ headless: true });
  const report = {
    accepted: false,
    browserErrors: [],
    assertions: {
      nonopenableStatusActionsEnabled: false,
      readerStatusBoundedProjection: false,
      readerStatusRefresh: false,
      readerInternalIdentityHidden: false,
      readerStatusTimelineVisible: false,
      readerStatusTimelineSanitized: false,
      mainContentAxeSeriousOrCriticalViolations: 0,
    },
    scenarios: [],
  };
  try {
    {
      const admin = await contextFor(browser, {
        admin: true,
        directory: true,
        mode: "directory",
      });
      await admin.page.goto(`${base}/apps`, { waitUntil: "networkidle" });
      await admin.page.getByText("Not ready", { exact: true }).waitFor();
      await admin.page.getByText("Disabled", { exact: true }).waitFor();
      await admin.page.getByText("Unavailable", { exact: true }).waitFor();
      if ((await admin.page.getByRole("link", { name: /Review in management/ }).count()) !== 3) {
        throw new Error("admin directory recovery links missing");
      }
      const text = await admin.page.locator("body").innerText();
      if (installationIDs.some((id) => text.includes(id)) || text.includes("count")) {
        throw new Error("directory leaked installation identity or count");
      }
      const violations = await audit(admin.page);
      if (violations.length) throw new Error(`directory axe: ${JSON.stringify(violations)}`);
      await admin.page.screenshot({
        path: path.join(evidenceDir, "directory-states-admin.png"),
        fullPage: true,
      });
      report.scenarios.push({ name: "directory-states-admin", violations });
      await closeScenario(admin.context, admin.errors, "directory admin");
    }
    {
      const reader = await contextFor(browser, {
        admin: false,
        directory: true,
        mode: "directory",
      });
      await reader.page.goto(`${base}/apps`, { waitUntil: "networkidle" });
      await reader.page.getByText("Not ready", { exact: true }).waitFor();
      if ((await reader.page.getByRole("link", { name: /Review in management/ }).count()) !== 0) {
        throw new Error("reader directory exposed management recovery");
      }
      const directoryViolations = await audit(reader.page);
      if (directoryViolations.length) throw new Error(`reader directory axe: ${JSON.stringify(directoryViolations)}`);
      await reader.page.screenshot({
        path: path.join(evidenceDir, "directory-states-reader.png"),
        fullPage: true,
      });
      report.scenarios.push({ name: "directory-states-reader", violations: directoryViolations });
      await reader.page
        .getByRole("button", { name: "View status for pending-fixture" })
        .click();
      const statusDialog = reader.page.getByRole("dialog");
      await statusDialog
        .getByText("Checking data policy", { exact: true })
        .last()
        .waitFor();
      await statusDialog
        .getByText("Not ready", { exact: true })
        .waitFor();
      await statusDialog
        .getByText("Stage changed", { exact: true })
        .waitFor();
      await statusDialog
        .getByRole("button", { name: "Refresh status" })
        .click();
      for (let attempt = 0; attempt < 20 && reader.launches.statusReads() < 2; attempt++) {
        await reader.page.waitForTimeout(50);
      }
      if (reader.launches.statusReads() !== 2) {
        throw new Error("reader status refresh did not repeat the bounded read");
      }
      const text = await reader.page.locator("body").innerText();
      if (installationIDs.some((id) => text.includes(id)) ||
          text.includes("count") ||
          text.includes("private-audit") ||
          text.includes("/run/private.sock")) {
        throw new Error("reader directory leaked installation identity or count");
      }
      const violations = await audit(reader.page);
      if (violations.length) throw new Error(`reader directory axe: ${JSON.stringify(violations)}`);
      report.assertions.nonopenableStatusActionsEnabled = true;
      report.assertions.readerStatusBoundedProjection = true;
      report.assertions.readerStatusRefresh = true;
      report.assertions.readerInternalIdentityHidden = true;
      report.assertions.readerStatusTimelineVisible = true;
      report.assertions.readerStatusTimelineSanitized = true;
      await reader.page.waitForTimeout(400);
      report.scenarios.push({ name: "directory-reader-status", violations });
      await reader.page.screenshot({
        path: path.join(evidenceDir, "directory-reader-status.png"),
        fullPage: true,
      });
      await closeScenario(reader.context, reader.errors, "directory reader");
    }
    for (const mode of ["denied", "backend-stopped"]) {
      const scenario = await contextFor(browser, {
        admin: false,
        directory: false,
        mode,
      });
      await scenario.page.goto(
        `${base}/apps/v2/${installationIDs[3]}`,
        { waitUntil: "networkidle" },
      );
      await scenario.page.getByText(
        "This app is unavailable. Check your access or contact an administrator.",
        { exact: true },
      ).waitFor();
      if (await scenario.page.locator("iframe").count()) {
        throw new Error(`${mode} mounted executable content`);
      }
      const text = await scenario.page.locator("body").innerText();
      if (text.includes("private") || text.includes("diagnostic")) {
        throw new Error(`${mode} exposed server diagnostics`);
      }
      const retry = scenario.page.getByRole("button", { name: "Reopen app" });
      await retry.click();
      await scenario.page.getByText(
        "This app is unavailable. Check your access or contact an administrator.",
        { exact: true },
      ).waitFor();
      if (scenario.launches.launches() !== 2) {
        throw new Error(`${mode} retry did not repeat the refused launch`);
      }
      const violations = await audit(scenario.page);
      if (violations.length) throw new Error(`${mode} axe: ${JSON.stringify(violations)}`);
      await scenario.page.screenshot({
        path: path.join(evidenceDir, `${mode}.png`),
        fullPage: true,
      });
      report.scenarios.push({ name: mode, violations });
      await closeScenario(scenario.context, scenario.errors, mode);
    }
    for (const mode of ["ready-timeout", "protocol-incompatible"]) {
      const scenario = await contextFor(browser, {
        admin: false,
        directory: false,
        mode,
      });
      await scenario.page.goto(
        `${base}/apps/v2/${installationIDs[3]}`,
        { waitUntil: "networkidle" },
      );
      await scenario.page.getByText(
        "The app could not connect. Reopen it to try again.",
        { exact: true },
      ).waitFor({ timeout: 15_000 });
      if (await scenario.page.locator("iframe").count()) {
        throw new Error(`${mode} retained executable content`);
      }
      const text = await scenario.page.locator("body").innerText();
      if (text.includes("PRIVATE APP CONTENT")) {
        throw new Error(`${mode} exposed app content after refusal`);
      }
      await scenario.page.getByRole("button", { name: "Reopen app" }).waitFor();
      const violations = await audit(scenario.page);
      if (violations.length) throw new Error(`${mode} axe: ${JSON.stringify(violations)}`);
      await scenario.page.screenshot({
        path: path.join(evidenceDir, `${mode}.png`),
        fullPage: true,
      });
      report.scenarios.push({ name: mode, violations });
      await closeScenario(scenario.context, scenario.errors, mode);
    }
    report.accepted = true;
    writeFileSync(
      path.join(evidenceDir, "acceptance.json"),
      JSON.stringify(report, null, 2) + "\n",
    );
    console.log("APP_PLATFORM_FAILURE_BROWSER_ACCEPTED");
    console.log(JSON.stringify(report));
  } finally {
    await browser.close();
    await new Promise((resolve) => server.close(resolve));
  }
}

main().catch((error) => {
  console.error(error);
  process.exitCode = 1;
});
