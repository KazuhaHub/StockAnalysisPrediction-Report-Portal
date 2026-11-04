const { createRequire } = require("node:module");
const { createServer } = require("node:http");
const {
  existsSync,
  mkdirSync,
  readFileSync,
  statSync,
  writeFileSync,
} = require("node:fs");
const path = require("node:path");

const root = path.resolve(__dirname, "..");
const fixtureRoot =
  process.env.APP_REFERENCE_BROWSER_ROOT ||
  process.env.APP_NOTES_BROWSER_ROOT;
const evidenceDir =
  process.env.APP_REFERENCE_BROWSER_EVIDENCE ||
  process.env.APP_NOTES_BROWSER_EVIDENCE;
const packagePath = process.env.SDK_BROWSER_PACKAGE;
if (!fixtureRoot || !path.isAbsolute(fixtureRoot)) {
  throw new Error("APP_REFERENCE_BROWSER_ROOT must be absolute");
}
if (!evidenceDir || !path.isAbsolute(evidenceDir)) {
  throw new Error("APP_REFERENCE_BROWSER_EVIDENCE must be absolute");
}
if (!packagePath || !path.isAbsolute(packagePath)) {
  throw new Error("SDK_BROWSER_PACKAGE must name the Playwright package.json");
}
const requirePlaywright = createRequire(packagePath);
const { chromium } = requirePlaywright("playwright");
const axePath = requirePlaywright.resolve("axe-core/axe.min.js");
const axeSource = readFileSync(axePath, "utf8");
mkdirSync(evidenceDir, { recursive: true });
const packageDigests = JSON.parse(
  readFileSync(path.join(fixtureRoot, "packages.json"), "utf8"),
);

const notesInstallation = "00000000-0000-4000-8000-000000000042";
const digestInstallation = "00000000-0000-4000-8000-000000000044";
const port = 31935;
const release = "a".repeat(64);
const notesBinding = {
  installationId: notesInstallation,
  generation: "1",
  policyRevision: "1",
  releaseSha256: release,
};
const digestBinding = {
  installationId: digestInstallation,
  generation: "1",
  policyRevision: "1",
  releaseSha256: release,
};
const notesFrontend = packageDigests["notes-sample"];
const digestFrontend = packageDigests["scheduled-digest"];
if (!/^[a-f0-9]{64}$/.test(notesFrontend) || !/^[a-f0-9]{64}$/.test(digestFrontend)) {
  throw new Error("fixture package digests are missing");
}
const notesDocumentRoot = `/app-documents/${notesInstallation}/1/1/${release}/${notesFrontend}/`;
const digestDocumentRoot = `/app-documents/${digestInstallation}/1/1/${release}/${digestFrontend}/`;
const notesDisplay = {
  icon: "file-text",
  texts: [
    { locale: "en-US", name: "Notes", description: "Personal notes" },
    { locale: "zh-CN", name: "Notes", description: "个人笔记" },
    { locale: "zh-TW", name: "Notes", description: "個人筆記" },
  ],
};
const digestDisplay = {
  icon: "calendar",
  texts: [
    {
      locale: "en-US",
      name: "Scheduled Digest",
      description: "Scheduled background digest with an independent backend",
    },
    {
      locale: "zh-CN",
      name: "Scheduled Digest",
      description: "带独立后端的定时摘要示例",
    },
    {
      locale: "zh-TW",
      name: "Scheduled Digest",
      description: "帶獨立後端的定時摘要範例",
    },
  ],
};
const notesLaunch = {
  source: notesDocumentRoot + "index.html",
  binding: notesBinding,
  capabilities: ["storage.files"],
  backendAvailable: false,
  applicationId: "notes-sample",
  display: notesDisplay,
  routes: [
    { id: "home", path: "/", layout: "document" },
    { id: "editor", path: "/editor", layout: "viewport" },
  ],
  layout: "document",
  timeZone: "UTC",
};
const digestLaunch = {
  source: digestDocumentRoot + "index.html",
  binding: digestBinding,
  capabilities: ["reports.read", "secrets.use"],
  backendAvailable: true,
  applicationId: "scheduled-digest",
  display: digestDisplay,
  routes: [
    { id: "home", path: "/", layout: "document" },
    { id: "history", path: "/history", layout: "document" },
  ],
  layout: "document",
  timeZone: "UTC",
};
const directory = {
  items: [
    {
      installationId: notesInstallation,
      applicationId: "notes-sample",
      state: "installed",
      openable: true,
      display: notesDisplay,
    },
    {
      installationId: digestInstallation,
      applicationId: "scheduled-digest",
      state: "setup_required",
      openable: false,
      display: digestDisplay,
    },
  ],
  nextCursor: "",
};
const digestState = {
  format: "scheduled-digest.state/1-dev.1",
  scheduleId: "daily-digest",
  lastRun: {
    occurrenceId: "fixture-digest-2026-11-04",
    generatedAt: "2026-11-04T16:00:00Z",
    itemCount: 3,
    digest: "c".repeat(64),
    duplicate: false,
  },
  recentRuns: [],
};

let note = null;
let noteContent = "";
const requests = [];

function summary() {
  return note ? { ...note } : null;
}

function json(response, status, value) {
  response.writeHead(status, {
    "Content-Type": "application/json; charset=utf-8",
    "Cache-Control": "no-store",
  });
  response.end(JSON.stringify(value));
}

function readBody(request) {
  return new Promise((resolve, reject) => {
    let body = "";
    request.setEncoding("utf8");
    request.on("data", (chunk) => {
      body += chunk;
      if (body.length > 2 * 1024 * 1024) reject(new Error("request too large"));
    });
    request.on("end", () => resolve(body));
    request.on("error", reject);
  });
}

function sendFile(response, base, relative, headers = {}) {
  const file = path.resolve(base, relative);
  if (!file.startsWith(base + path.sep) || !existsSync(file) || !statSync(file).isFile()) {
    response.writeHead(404);
    response.end("not found");
    return;
  }
  const extension = path.extname(file);
  const type =
    {
      ".css": "text/css; charset=utf-8",
      ".html": "text/html; charset=utf-8",
      ".js": "text/javascript; charset=utf-8",
      ".json": "application/json; charset=utf-8",
      ".svg": "image/svg+xml",
      ".woff": "font/woff",
      ".woff2": "font/woff2",
    }[extension] || "application/octet-stream";
  response.writeHead(200, {
    "Content-Type": type,
    "Cache-Control": "no-store",
    ...headers,
  });
  response.end(readFileSync(file));
}

function objectResponse(body) {
  const action = body.action;
  if (action === "list") {
    return { action: "list", items: note ? [summary()] : [] };
  }
  if (action === "get") {
    if (!note || body.id !== note.id) return null;
    return { action: "get", object: summary(), contentBase64: noteContent };
  }
  if (action === "create") {
    const content = Buffer.from(body.contentBase64, "base64");
    note = {
      id: "00000000-0000-4000-8000-000000000043",
      collection: body.collection,
      label: body.label,
      mediaType: body.mediaType,
      sizeBytes: content.length,
      revision: 1,
      createdAt: 1760000000000,
      updatedAt: 1760000000000,
    };
    noteContent = body.contentBase64;
    return { action: "create", object: summary() };
  }
  if (action === "update") {
    if (!note || body.id !== note.id || body.expectedRevision !== note.revision) {
      return null;
    }
    const content = Buffer.from(body.contentBase64, "base64");
    note = {
      ...note,
      label: body.label,
      mediaType: body.mediaType,
      sizeBytes: content.length,
      revision: note.revision + 1,
      updatedAt: note.updatedAt + 1,
    };
    noteContent = body.contentBase64;
    return { action: "update", object: summary() };
  }
  if (action === "delete") {
    if (!note || body.id !== note.id || body.expectedRevision !== note.revision) {
      return null;
    }
    const id = note.id;
    note = null;
    noteContent = "";
    return { action: "delete", id };
  }
  return null;
}

async function server() {
  const hostRoot = path.join(fixtureRoot, "host");
  const notesRoot = path.join(fixtureRoot, "notes-sample");
  const digestRoot = path.join(fixtureRoot, "scheduled-digest");
  const shell = (
    '<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><link rel="stylesheet" href="/__shell/style.css"><div id="host"></div><script src="/__shell/host.js"></script>'
  );
  const instance = createServer(async (request, response) => {
    const url = new URL(request.url, "http://127.0.0.1");
    if (url.pathname === "/__blank") {
      response.writeHead(200, { "Content-Type": "text/html; charset=utf-8" });
      response.end("<!doctype html><title>Preferences</title>");
      return;
    }
    if (url.pathname === "/__shell/index.html") {
      response.writeHead(200, {
        "Content-Type": "text/html; charset=utf-8",
        "Cache-Control": "no-store",
        "Content-Security-Policy":
          "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'none'; form-action 'none'",
      });
      response.end(shell);
      return;
    }
    if (url.pathname === "/__shell/host.js" || url.pathname === "/__shell/style.css") {
      sendFile(response, hostRoot, path.basename(url.pathname));
      return;
    }
    if (
      (url.pathname === notesDocumentRoot + "__axe.js" ||
        url.pathname === digestDocumentRoot + "__axe.js") &&
      request.method === "GET"
    ) {
      response.writeHead(200, {
        "Content-Type": "text/javascript; charset=utf-8",
        "Cache-Control": "no-store",
      });
      response.end(axeSource);
      return;
    }
    if (url.pathname.startsWith(notesDocumentRoot)) {
      sendFile(
        response,
        notesRoot,
        url.pathname.slice(notesDocumentRoot.length),
        {
          "Content-Security-Policy":
            "default-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self' data:; connect-src 'none'; base-uri 'none'; form-action 'none'",
        },
      );
      return;
    }
    if (url.pathname.startsWith(digestDocumentRoot)) {
      sendFile(
        response,
        digestRoot,
        url.pathname.slice(digestDocumentRoot.length),
        {
          "Content-Security-Policy":
            "default-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self' data:; connect-src 'none'; base-uri 'none'; form-action 'none'",
        },
      );
      return;
    }
    if (url.pathname === "/api/me") {
      json(response, 200, {
        user: "fixture-reader",
        name: "Fixture Reader",
        admin: false,
        perms: {},
        forecast: {},
        forecast_reader: {},
      });
      return;
    }
    if (url.pathname === "/api/apps") {
      json(response, 200, { apps: [], builtin_apps: [], order: [] });
      return;
    }
    if (url.pathname === "/api/app-platform/apps") {
      json(response, 200, directory);
      return;
    }
    if (
      url.pathname ===
      `/api/app-platform/installations/${notesInstallation}/launch`
    ) {
      json(response, 200, notesLaunch);
      return;
    }
    if (
      url.pathname ===
      `/api/app-platform/installations/${digestInstallation}/launch`
    ) {
      json(response, 200, digestLaunch);
      return;
    }
    if (
      url.pathname ===
        `/api/app-platform/installations/${notesInstallation}/objects` &&
      request.method === "POST"
    ) {
      try {
        const body = JSON.parse(await readBody(request));
        requests.push({ path: url.pathname, action: body.action });
        if (
          !body ||
          body.binding?.generation !== notesBinding.generation ||
          body.binding?.policyRevision !== notesBinding.policyRevision ||
          body.binding?.releaseSha256 !== notesBinding.releaseSha256
        ) {
          json(response, 409, { error: { code: "stale_binding" } });
          return;
        }
        const result = objectResponse(body);
        if (result) {
          requests.push({ path: url.pathname, action: body.action, ok: true });
          json(response, 200, result);
        }
        else json(response, 409, { error: { code: "conflict" } });
      } catch {
        requests.push({ path: url.pathname, error: true });
        json(response, 422, { error: { code: "invalid_request" } });
      }
      return;
    }
    if (
      url.pathname ===
        `/api/app-platform/installations/${digestInstallation}/backend` &&
      request.method === "POST"
    ) {
      try {
        const body = JSON.parse(await readBody(request));
        requests.push({
          path: url.pathname,
          method: body.method,
          requestPath: body.path,
        });
        if (
          body.binding?.generation !== digestBinding.generation ||
          body.binding?.policyRevision !== digestBinding.policyRevision ||
          body.binding?.releaseSha256 !== digestBinding.releaseSha256 ||
          body.method !== "GET" ||
          body.path !== "/v1/digest"
        ) {
          json(response, 409, { code: "app_platform_invalid_request" });
          return;
        }
        json(response, 200, {
          status: 200,
          body: JSON.stringify(digestState),
        });
      } catch {
        requests.push({ path: url.pathname, error: true });
        json(response, 422, { code: "app_platform_invalid_request" });
      }
      return;
    }
    if (url.pathname === "/") {
      response.writeHead(302, { Location: "/apps" });
      response.end();
      return;
    }
    if (url.pathname.startsWith("/__shell/")) {
      response.writeHead(404);
      response.end("not found");
      return;
    }
    response.writeHead(200, {
      "Content-Type": "text/html; charset=utf-8",
      "Cache-Control": "no-store",
    });
    response.end(shell);
  });
  await new Promise((resolve, reject) => {
    instance.once("error", reject);
    instance.listen(port, "127.0.0.1", resolve);
  });
  return instance;
}

async function openPage(browser, origin, mode, locale, viewport, pathname = "/apps") {
  const context = await browser.newContext({ viewport, colorScheme: mode });
  const page = await context.newPage();
  const errors = [];
  page.on("console", (message) => {
    if (message.type() === "error") errors.push(message.text());
  });
  page.on("pageerror", (error) => errors.push(String(error)));
  await page.goto(origin + "/__blank", { waitUntil: "domcontentloaded" });
  await page.evaluate(
    ({ theme, language }) => {
      localStorage.setItem("rp_theme", theme);
      localStorage.setItem("rp_locale", language);
    },
    { theme: mode, language: locale },
  );
  await page.goto(origin + pathname, {
    waitUntil: "domcontentloaded",
  });
  return { context, page, errors };
}

async function axeViolations(page) {
  await page.addScriptTag({ path: axePath });
  return page.evaluate(async () => {
    const result = await window.axe.run(document, {
      runOnly: { type: "tag", values: ["wcag2a", "wcag2aa"] },
      rules: { "color-contrast": { enabled: false } },
    });
    return result.violations
      .filter(
        (violation) =>
          violation.impact === "serious" || violation.impact === "critical",
      )
      .map((violation) => violation.id);
  });
}

async function frameAxeViolations(page) {
  const frame = page.frames().find((candidate) => candidate !== page.mainFrame());
  if (!frame) throw new Error("application frame was not available for axe");
  const source = await page.locator("iframe").getAttribute("src");
  if (!source) throw new Error("application source was not available for axe");
  // Serve the test-only bundle beside the document so application CSP can keep
  // script-src 'self' without admitting inline script.
  await frame.addScriptTag({
    url: new URL("./__axe.js", new URL(source, page.url())).pathname,
  });
  return frame.evaluate(async () => {
    const result = await window.axe.run(document, {
      runOnly: { type: "tag", values: ["wcag2a", "wcag2aa"] },
      rules: { "color-contrast": { enabled: false } },
    });
    return result.violations
      .filter(
        (violation) =>
          violation.impact === "serious" || violation.impact === "critical",
      )
      .map((violation) => violation.id);
  });
}

async function backToApps(page, label) {
  await page
    .getByRole("button")
    .filter({ hasText: label })
    .first()
    .click();
}

function applicationConsoleErrors(errors) {
  return errors.filter(
    (message) =>
      !(
        message.includes("style.css") &&
        message.includes("connect-src 'none'")
      ),
  );
}

async function shellContrast(page) {
  return page.evaluate(() => {
    function channels(value) {
      const match = value.match(/rgba?\((\d+), (\d+), (\d+)/);
      if (!match) throw new Error("unresolved CSS colour");
      return match.slice(1, 4).map(Number);
    }
    function luminance(value) {
      return channels(value)
        .map((channel) => channel / 255)
        .map((channel) =>
          channel <= 0.04045
            ? channel / 12.92
            : ((channel + 0.055) / 1.055) ** 2.4,
        )
        .reduce(
          (sum, channel, index) =>
            sum + channel * [0.2126, 0.7152, 0.0722][index],
          0,
        );
    }
    function ratio(foreground, background) {
      const first = luminance(foreground);
      const second = luminance(background);
      return (Math.max(first, second) + 0.05) /
        (Math.min(first, second) + 0.05);
    }
    function effectiveBackground(element) {
      for (let current = element; current; current = current.parentElement) {
        const value = getComputedStyle(current).backgroundColor;
        const alpha = value.match(/rgba\([^)]*, ([0-9.]+)\)/)?.[1];
        if (value !== "transparent" && alpha !== "0") return value;
      }
      return "rgb(255, 255, 255)";
    }
    const primary = document.querySelector(".ant-btn-primary");
    const secondary = document.querySelector(".ant-typography-secondary");
    return {
      primary: primary
        ? ratio(getComputedStyle(primary).color, effectiveBackground(primary))
        : 0,
      primaryForeground: primary ? getComputedStyle(primary).color : "",
      primaryBackground: primary ? effectiveBackground(primary) : "",
      secondary: secondary
        ? ratio(
            getComputedStyle(secondary).color,
            effectiveBackground(secondary),
          )
        : 0,
    };
  });
}

async function main() {
  const assertions = {};
  const screenshots = [];
  const output = {
    format: "portal.reference-apps-shell-browser-acceptance/1-dev.1",
    packageDigests,
    assertions,
    screenshots,
  };
  const service = await server();
  const origin = `http://127.0.0.1:${port}`;
  let browser;
  let currentPage;
  let currentErrors = [];
  try {
    browser = await chromium.launch({ headless: true, timeout: 10000 });
    const desktop = await openPage(
      browser,
      origin,
      "light",
      "en-US",
      { width: 1440, height: 1000 },
    );
    const page = desktop.page;
    currentPage = page;
    currentErrors = desktop.errors;
    await page.getByRole("button", { name: "Notes", exact: true }).waitFor();
    await page.getByText("Setup required", { exact: true }).waitFor();
    await page
      .getByText(
        "An administrator must finish the application setup before it can be opened.",
        { exact: true },
      )
      .waitFor();
    if (
      (await page
        .getByRole("button", { name: "Scheduled Digest", exact: true })
        .count()) !== 0
    ) {
      throw new Error("setup-required application exposed an open action");
    }
    assertions.ordinaryUserSeesTruthfulSetupRequired = true;
    assertions.setupRequiredApplicationNotOpenable = true;
    const setupShot = path.join(
      evidenceDir,
      "reference-apps-1440-en-light-setup-required.png",
    );
    await page.screenshot({ path: setupShot, fullPage: true });
    screenshots.push(path.basename(setupShot));
    directory.items[1].state = "installed";
    directory.items[1].openable = true;
    await page.reload({ waitUntil: "domcontentloaded" });
    await page
      .getByRole("button", { name: "Scheduled Digest", exact: true })
      .waitFor();
    if (await page.locator("iframe").count()) {
      throw new Error("My Apps mounted an application before the reader opened one");
    }
    assertions.myAppsListsBothReferenceApps = true;
    assertions.installedDirectoryEntriesOpenable =
      directory.items.every((item) => item.state === "installed" && item.openable);
    await page.getByRole("button", { name: "Notes", exact: true }).click();
    await page.waitForURL(origin + `/apps/v2/${notesInstallation}`);
    const frame = page.frameLocator("iframe");
    await page.getByRole("heading", { name: "Notes", exact: true }).waitFor();
    await frame.getByText("No notes yet", { exact: true }).waitFor();
    const titles = await page
      .locator("h1,h2,h3,h4,h5,h6")
      .allInnerTexts();
    if (titles.filter((title) => title.trim() === "Notes").length !== 1) {
      throw new Error("Notes shell did not expose exactly one application title");
    }
    if (/\nv0\.\d/.test(await page.locator("body").innerText())) {
      throw new Error("Notes shell exposed a duplicate version row");
    }
    const iframeBorder = await page
      .locator("iframe")
      .evaluate((element) => getComputedStyle(element).borderTopWidth);
    if (iframeBorder !== "0px") {
      throw new Error("Notes iframe retained an outer application frame");
    }
    assertions.notesSinglePlatformTitle = true;
    assertions.noDuplicateVersionRow = true;
    assertions.noOuterApplicationFrame = true;

    await page.getByRole("button", { name: "New note", exact: true }).click();
    await page.waitForURL(origin + `/apps/v2/${notesInstallation}/editor`);
    const editor = frame.getByRole("textbox", { name: /Note/ });
    await editor.fill(
      Array.from({ length: 40 }, (_, index) => `Synthetic note line ${index + 1}`).join(
        "\n",
      ),
    );
    const documentGeometry = await frame
      .locator("html")
      .evaluate((element) => ({
        scroll: element.scrollHeight,
        client: element.clientHeight,
      }));
    if (documentGeometry.scroll > documentGeometry.client + 2) {
      throw new Error("Notes viewport editor scrolled its application document");
    }
    const editorGeometry = await editor.evaluate((element) => ({
      scroll: element.scrollHeight,
      client: element.clientHeight,
    }));
    if (editorGeometry.scroll <= editorGeometry.client + 2) {
      throw new Error("Notes editor did not provide intentional internal scroll");
    }
    await editor.focus();
    await page.keyboard.press("Control+End");
    if (await editor.evaluate((element) => element.scrollTop <= 0)) {
      throw new Error("Notes editor was not keyboard scrollable");
    }
    assertions.viewportEditorInternalScroll = true;
    assertions.keyboardScrollableEditor = true;

    await page.getByRole("button", { name: "Save", exact: true }).click();
    await page.waitForURL(origin + `/apps/v2/${notesInstallation}`);
    await frame.getByText("Synthetic note line 1", { exact: true }).waitFor();
    await page.reload({ waitUntil: "domcontentloaded" });
    await page.getByRole("heading", { name: "Notes", exact: true }).waitFor();
    await frame.getByText("Synthetic note line 1", { exact: true }).waitFor();
    await page.waitForFunction(() => {
      const button = [...document.querySelectorAll("button")].find(
        (candidate) => candidate.textContent?.trim() === "New note",
      );
      return button && !button.disabled;
    });
    await page.waitForTimeout(500);
    assertions.savedObjectSurvivesReload = true;
    const contrast = await shellContrast(page);
    if (contrast.primary < 4.5 || contrast.secondary < 4.5) {
      throw new Error("Notes shell contrast fell below AA: " + JSON.stringify(contrast));
    }
    assertions.shellContrastAA = true;
    const desktopHostAxe = await axeViolations(page);
    const desktopNotesAxe = await frameAxeViolations(page);
    if (desktopHostAxe.length || desktopNotesAxe.length) {
      throw new Error(
        "reference-app shell axe violations: " +
          [...desktopHostAxe, ...desktopNotesAxe].join(", "),
      );
    }
    assertions.desktopAxeSeriousCritical = 0;
    const notesShot = path.join(evidenceDir, "reference-apps-1440-en-light-notes.png");
    await page.screenshot({ path: notesShot, fullPage: true });
    screenshots.push(path.basename(notesShot));

    await backToApps(page, "Back to apps");
    await page.waitForURL(origin + "/apps");
    await page
      .getByRole("button", { name: "Scheduled Digest", exact: true })
      .click();
    await page.waitForURL(origin + `/apps/v2/${digestInstallation}`);
    const digestFrame = page.frameLocator("iframe");
    await page
      .getByRole("heading", { name: "Scheduled Digest", exact: true })
      .waitFor();
    await digestFrame.getByText("Digest items", { exact: true }).waitFor();
    await digestFrame.getByText("daily-digest", { exact: true }).waitFor();
    const digestFrames = page.locator("iframe");
    if ((await digestFrames.count()) !== 1) {
      throw new Error("application switch retained more than one executable frame");
    }
    const digestSource = await digestFrames.first().getAttribute("src");
    if (!digestSource?.includes(digestInstallation)) {
      throw new Error("Scheduled Digest did not replace the Notes frame");
    }
    const digestTitles = await page
      .locator("h1,h2,h3,h4,h5,h6")
      .allInnerTexts();
    if (
      digestTitles.filter((title) => title.trim() === "Scheduled Digest").length !==
      1
    ) {
      throw new Error(
        "Scheduled Digest shell did not expose exactly one application title",
      );
    }
    if (/\nv0\.\d/.test(await page.locator("body").innerText())) {
      throw new Error("Scheduled Digest shell exposed a duplicate version row");
    }
    const digestIframeBorder = await digestFrames
      .first()
      .evaluate((element) => getComputedStyle(element).borderTopWidth);
    if (digestIframeBorder !== "0px") {
      throw new Error("Scheduled Digest iframe retained an outer application frame");
    }
    if ((await page.getByRole("navigation", { name: "Platform" }).count()) !== 1) {
      throw new Error("application shell did not expose exactly one platform navigation");
    }
    if (!requests.some((entry) => entry.requestPath === "/v1/digest")) {
      throw new Error("Scheduled Digest did not use the independent backend bridge");
    }
    const digestHostAxe = await axeViolations(page);
    const digestFrameAxe = await frameAxeViolations(page);
    if (digestHostAxe.length || digestFrameAxe.length) {
      throw new Error(
        "Scheduled Digest axe violations: " +
          [...digestHostAxe, ...digestFrameAxe].join(", "),
      );
    }
    assertions.digestSinglePlatformTitle = true;
    assertions.singlePlatformNavigation = true;
    assertions.digestNoDuplicateVersionRow = true;
    assertions.digestNoOuterApplicationFrame = true;
    assertions.digestIndependentBackendBridge = true;
    assertions.appSwitchUnmountsPreviousFrame = true;
    const digestShot = path.join(
      evidenceDir,
      "reference-apps-1440-en-light-scheduled-digest.png",
    );
    await page.screenshot({ path: digestShot, fullPage: true });
    screenshots.push(path.basename(digestShot));

    await backToApps(page, "Back to apps");
    await page.waitForURL(origin + "/apps");
    await page.getByRole("button", { name: "Notes", exact: true }).click();
    await page.waitForURL(origin + `/apps/v2/${notesInstallation}`);
    await page.getByRole("heading", { name: "Notes", exact: true }).waitFor();
    const returnedSource = await page.locator("iframe").getAttribute("src");
    if (!returnedSource?.includes(notesInstallation)) {
      throw new Error("switching back did not mount the exact Notes document");
    }
    assertions.appSwitchBackMountedExactNotesDocument = true;
    await desktop.context.close();
    const desktopErrors = applicationConsoleErrors(desktop.errors);
    if (desktopErrors.length) {
      throw new Error("reference-app desktop console errors: " + desktopErrors.join(" | "));
    }

    const narrow = await openPage(
      browser,
      origin,
      "dark",
      "zh-TW",
      { width: 320, height: 800 },
    );
    const narrowPage = narrow.page;
    currentPage = narrowPage;
    currentErrors = narrow.errors;
    await narrowPage.getByRole("button", { name: "Notes", exact: true }).waitFor();
    await narrowPage
      .getByRole("button", { name: "Scheduled Digest", exact: true })
      .waitFor();
    const directoryGeometry = await narrowPage.evaluate(() => ({
      scroll: document.documentElement.scrollWidth,
      width: window.innerWidth,
    }));
    if (directoryGeometry.scroll > directoryGeometry.width + 1) {
      throw new Error("My Apps overflowed a 320px viewport");
    }
    await narrowPage
      .getByRole("button", { name: "Scheduled Digest", exact: true })
      .click();
    await narrowPage.waitForURL(origin + `/apps/v2/${digestInstallation}`);
    const narrowFrame = narrowPage.frameLocator("iframe");
    await narrowPage
      .getByRole("heading", { name: "定時摘要", exact: true })
      .waitFor();
    await narrowFrame.getByText("摘要項目", { exact: true }).waitFor();
    const narrowDigestGeometry = await narrowPage.evaluate(() => ({
      scroll: document.documentElement.scrollWidth,
      width: window.innerWidth,
    }));
    const narrowDigestFrameGeometry = await narrowFrame
      .locator("html")
      .evaluate((element) => ({
        scroll: element.scrollWidth,
        width: element.clientWidth,
      }));
    if (
      narrowDigestGeometry.scroll > narrowDigestGeometry.width + 1 ||
      narrowDigestFrameGeometry.scroll > narrowDigestFrameGeometry.width + 1
    ) {
      throw new Error("Scheduled Digest overflowed a 320px viewport");
    }
    assertions.narrowDarkTraditionalChineseDigest = true;
    const narrowDigestShot = path.join(
      evidenceDir,
      "reference-apps-320-zh-tw-dark-scheduled-digest.png",
    );
    await narrowPage.screenshot({ path: narrowDigestShot, fullPage: true });
    screenshots.push(path.basename(narrowDigestShot));

    await backToApps(narrowPage, "返回應用");
    await narrowPage.waitForURL(origin + "/apps");
    await narrowPage.getByRole("button", { name: "Notes", exact: true }).click();
    await narrowPage.waitForURL(origin + `/apps/v2/${notesInstallation}`);
    await narrowPage
      .getByRole("heading", { name: "筆記", exact: true })
      .waitFor();
    await narrowFrame.getByText("Synthetic note line 1", { exact: true }).waitFor();
    const hostGeometry = await narrowPage.evaluate(() => ({
      scroll: document.documentElement.scrollWidth,
      width: window.innerWidth,
    }));
    const frameGeometry = await narrowFrame
      .locator("html")
      .evaluate((element) => ({
        scroll: element.scrollWidth,
        width: element.clientWidth,
      }));
    if (
      hostGeometry.scroll > hostGeometry.width + 1 ||
      frameGeometry.scroll > frameGeometry.width + 1
    ) {
      throw new Error("Notes shell overflowed a 320px viewport");
    }
    await narrowPage
      .getByRole("button", { name: "新增筆記", exact: true })
      .click();
    await narrowPage.waitForURL(
      origin + `/apps/v2/${notesInstallation}/editor`,
    );
    await narrowFrame
      .locator('.portal-v2-root[data-app-layout="viewport"]')
      .waitFor();
    await narrowPage.waitForFunction(
      () =>
        (document.querySelector("iframe")?.getBoundingClientRect().height ||
          0) >= 500,
    );
    let editorFits = false;
    let finalGeometry;
    for (let attempt = 0; attempt < 20 && !editorFits; attempt++) {
      const iframeBox = await narrowPage.locator("iframe").boundingBox();
      const editorBox = await narrowFrame
        .getByRole("textbox", { name: /筆記/ })
        .boundingBox();
      finalGeometry = { iframeBox, editorBox };
      editorFits =
        !!iframeBox &&
        iframeBox.height >= 500 &&
        !!editorBox &&
        editorBox.y + editorBox.height <= iframeBox.y + iframeBox.height + 1;
      if (!editorFits) await narrowPage.waitForTimeout(100);
    }
    if (!editorFits) {
      throw new Error(
        "Notes viewport editor was clipped by its host frame: " +
          JSON.stringify(finalGeometry),
      );
    }
    assertions.viewportEditorUsesHostHeight = true;
    assertions.narrowDarkTraditionalChineseNotes = true;
    const narrowShot = path.join(
      evidenceDir,
      "reference-apps-320-zh-tw-dark-notes-editor.png",
    );
    await narrowPage.screenshot({ path: narrowShot, fullPage: true });
    screenshots.push(path.basename(narrowShot));
    await narrow.context.close();
    const narrowErrors = applicationConsoleErrors(narrow.errors);
    if (narrowErrors.length) {
      throw new Error("reference-app narrow console errors: " + narrowErrors.join(" | "));
    }
    output.limitations = [
    "The fixture preloads installation-state projections so it isolates My Apps setup, opening, switching, title and frame behavior; it does not replace the managed lifecycle install transaction.",
      "Scheduled Digest presentation runs against a contract-compatible fixture backend, not the exact service image.",
      "Automated axe and keyboard checks do not constitute the required human screen-reader audit.",
    ];
    output.passed = true;
  } catch (error) {
    output.passed = false;
    output.failure = String(error?.stack || error);
    output.consoleErrors = currentErrors;
    output.requests = requests;
    if (currentPage) {
      output.body = (await currentPage.locator("body").innerText()).slice(0, 4096);
      await currentPage.screenshot({
        path: path.join(evidenceDir, "failure.png"),
        fullPage: true,
      });
    }
    throw error;
  } finally {
    writeFileSync(
      path.join(evidenceDir, "acceptance.json"),
      JSON.stringify(output, null, 2) + "\n",
    );
    if (browser) await browser.close();
    await new Promise((resolve) => service.close(resolve));
  }
}

main().catch((error) => {
  console.error(error?.stack || error);
  process.exitCode = 1;
});
