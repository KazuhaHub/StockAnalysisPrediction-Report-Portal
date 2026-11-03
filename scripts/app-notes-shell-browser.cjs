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
const fixtureRoot = process.env.APP_NOTES_BROWSER_ROOT;
const evidenceDir = process.env.APP_NOTES_BROWSER_EVIDENCE;
const packagePath = process.env.SDK_BROWSER_PACKAGE;
if (!fixtureRoot || !path.isAbsolute(fixtureRoot)) {
  throw new Error("APP_NOTES_BROWSER_ROOT must be absolute");
}
if (!evidenceDir || !path.isAbsolute(evidenceDir)) {
  throw new Error("APP_NOTES_BROWSER_EVIDENCE must be absolute");
}
if (!packagePath || !path.isAbsolute(packagePath)) {
  throw new Error("SDK_BROWSER_PACKAGE must name the Playwright package.json");
}
const requirePlaywright = createRequire(packagePath);
const { chromium } = requirePlaywright("playwright");
const axePath = requirePlaywright.resolve("axe-core/axe.min.js");
mkdirSync(evidenceDir, { recursive: true });

const installation = "00000000-0000-4000-8000-000000000042";
const port = 31935;
const release = "a".repeat(64);
const binding = {
  installationId: installation,
  generation: "1",
  policyRevision: "1",
  releaseSha256: release,
};
const frontend = "b".repeat(64);
const documentRoot = `/app-documents/${installation}/1/1/${release}/${frontend}/`;
const launch = {
  source: documentRoot + "index.html",
  binding,
  capabilities: ["storage.files"],
  backendAvailable: false,
  applicationId: "notes-sample",
  display: {
    icon: "file-text",
    texts: [
      { locale: "en-US", name: "Notes", description: "Personal notes" },
      { locale: "zh-CN", name: "Notes", description: "个人笔记" },
      { locale: "zh-TW", name: "Notes", description: "個人筆記" },
    ],
  },
  routes: [
    { id: "home", path: "/", layout: "document" },
    { id: "editor", path: "/editor", layout: "viewport" },
  ],
  layout: "document",
  timeZone: "UTC",
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
  const notesRoot = path.join(fixtureRoot, "notes");
  const instance = createServer(async (request, response) => {
    const url = new URL(request.url, "http://127.0.0.1");
    if (url.pathname === "/__blank") {
      response.writeHead(200, { "Content-Type": "text/html; charset=utf-8" });
      response.end("<!doctype html><title>Preferences</title>");
      return;
    }
    if (url.pathname === "/__notes/index.html") {
      response.writeHead(200, {
        "Content-Type": "text/html; charset=utf-8",
        "Cache-Control": "no-store",
        "Content-Security-Policy":
          "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self' data:; connect-src 'none'; object-src 'none'; base-uri 'none'; form-action 'none'",
      });
      response.end(
        '<!doctype html><meta charset="utf-8"><link rel="stylesheet" href="/__notes/style.css"><div id="host"></div><script src="/__notes/host.js"></script>',
      );
      return;
    }
    if (url.pathname === "/__notes/host.js" || url.pathname === "/__notes/style.css") {
      sendFile(response, hostRoot, path.basename(url.pathname));
      return;
    }
    if (url.pathname.startsWith(documentRoot)) {
      sendFile(
        response,
        notesRoot,
        url.pathname.slice(documentRoot.length),
        {
          "Content-Security-Policy":
            "default-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self' data:; connect-src 'none'; base-uri 'none'; form-action 'none'",
        },
      );
      return;
    }
    if (
      url.pathname ===
      `/api/app-platform/installations/${installation}/launch`
    ) {
      json(response, 200, launch);
      return;
    }
    if (
      url.pathname ===
        `/api/app-platform/installations/${installation}/objects` &&
      request.method === "POST"
    ) {
      try {
        const body = JSON.parse(await readBody(request));
        requests.push({ path: url.pathname, action: body.action });
        if (
          !body ||
          body.binding?.generation !== binding.generation ||
          body.binding?.policyRevision !== binding.policyRevision ||
          body.binding?.releaseSha256 !== binding.releaseSha256
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
    if (url.pathname.startsWith("/__notes/") || url.pathname === "/") {
      if (url.pathname === "/") {
        response.writeHead(302, { Location: "/__notes/index.html" });
        response.end();
      } else {
        response.writeHead(404);
        response.end("not found");
      }
      return;
    }
    response.writeHead(200, {
      "Content-Type": "text/html; charset=utf-8",
      "Cache-Control": "no-store",
    });
    response.end(
      '<!doctype html><meta charset="utf-8"><link rel="stylesheet" href="/__notes/style.css"><div id="host"></div><script src="/__notes/host.js"></script>',
    );
  });
  await new Promise((resolve, reject) => {
    instance.once("error", reject);
    instance.listen(port, "127.0.0.1", resolve);
  });
  return instance;
}

async function openPage(browser, origin, mode, locale, viewport) {
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
  await page.goto(origin + `/apps/v2/${installation}`, {
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
  const output = { format: "portal.notes-shell-browser-acceptance/1-dev.1", assertions, screenshots };
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
    assertions.singlePlatformTitle = true;
    assertions.noDuplicateVersionRow = true;
    assertions.noOuterApplicationFrame = true;

    await page.getByRole("button", { name: "New note", exact: true }).click();
    await page.waitForURL(origin + `/apps/v2/${installation}/editor`);
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
    await page.waitForURL(origin + `/apps/v2/${installation}`);
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
    const desktopAxe = await axeViolations(page);
    if (desktopAxe.length) {
      throw new Error(
        "Notes shell axe violations: " + desktopAxe.join(", "),
      );
    }
    assertions.desktopAxeSeriousCritical = 0;
    const desktopShot = path.join(evidenceDir, "notes-shell-1440-en-light.png");
    await page.screenshot({ path: desktopShot, fullPage: true });
    screenshots.push(path.basename(desktopShot));
    await desktop.context.close();
    if (desktop.errors.length) {
      throw new Error("Notes desktop console errors: " + desktop.errors.join(" | "));
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
    const narrowFrame = narrowPage.frameLocator("iframe");
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
      origin + `/apps/v2/${installation}/editor`,
    );
    await narrowFrame
      .locator('.portal-v2-root[data-app-layout="viewport"]')
      .waitFor();
    await narrowPage.waitForFunction(
      () =>
        (document.querySelector("iframe")?.getBoundingClientRect().height ||
          0) >= 650,
    );
    const iframeBox = await narrowPage.locator("iframe").boundingBox();
    const editorBox = await narrowFrame
      .getByRole("textbox", { name: /筆記/ })
      .boundingBox();
    if (
      !iframeBox ||
      iframeBox.height < 650 ||
      !editorBox ||
      editorBox.y + editorBox.height > iframeBox.y + iframeBox.height + 1
    ) {
      throw new Error("Notes viewport editor was clipped by its host frame");
    }
    assertions.viewportEditorUsesHostHeight = true;
    assertions.narrowDarkTraditionalChinese = true;
    const narrowShot = path.join(evidenceDir, "notes-shell-320-zh-tw-dark-editor.png");
    await narrowPage.screenshot({ path: narrowShot, fullPage: true });
    screenshots.push(path.basename(narrowShot));
    await narrow.context.close();
    if (narrow.errors.length) {
      throw new Error("Notes narrow console errors: " + narrow.errors.join(" | "));
    }
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
