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

// Browser page zoom at 200% keeps the physical display at 1440x900 while
// halving the CSS viewport and doubling rendered pixels.
const desktopZoom200 = {
  viewport: { width: 720, height: 450 },
  deviceScaleFactor: 2,
  zoom: 200,
};

function startFixture(scenario = "install", market = false) {
  const fixture = spawn(
    "python3",
    [
      "-B",
      "scripts/app-install-browser-fixture.py",
      "--scenario",
      scenario,
      ...(market ? ["--market"] : []),
    ],
    {
      cwd: root,
      env: {
        ...process.env,
        GOWORK: "off",
        GOTOOLCHAIN: "local",
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
      const match = buffered.match(/^PROBE_URL=(http:\/\/127\.0\.0\.1:\d+\/manage\/apps(?:\/[^\s?]+)?(?:\?[^\s]+)?)$/m);
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

async function auditPage(page) {
  await page.addScriptTag({ path: axePath });
  return page.evaluate(async () => {
    const result = await window.axe.run(document, {
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

async function waitForReviewDialogStyles(page) {
  await page.waitForFunction(() => {
    const dialog = document.querySelector('[role="dialog"]');
    const primary = dialog?.querySelector(".ant-btn-primary");
    if (!dialog || !primary || getComputedStyle(dialog).opacity !== "1") {
      return false;
    }
    const background = getComputedStyle(primary).backgroundColor;
    return primary.classList.contains("ant-btn-dangerous")
      ? background === "rgb(168, 7, 26)"
      : background === "rgb(9, 88, 217)";
  });
}

async function assertDialogViewport(page, label) {
  const geometry = await page.evaluate(() => {
    const viewportWidth = window.innerWidth;
    const viewportHeight = window.innerHeight;
    const dialog = document.querySelector('[role="dialog"]');
    const content =
      dialog?.querySelector(".ant-modal-container, .ant-modal-content") ??
      (dialog?.classList.contains("ant-modal-content") ? dialog : null);
    const body = content?.querySelector(".ant-modal-body");
    const footerButtons = dialog
      ? [...dialog.querySelectorAll(".ant-modal-footer button")].map(
          (button) => {
            const rect = button.getBoundingClientRect();
            return {
              left: rect.left,
              top: rect.top,
              right: rect.right,
              bottom: rect.bottom,
              width: rect.width,
              height: rect.height,
            };
          },
        )
      : [];
    return {
      scrollWidth: document.documentElement.scrollWidth,
      viewportWidth,
      viewportHeight,
      activeInside: !!document.activeElement?.closest('[role="dialog"]'),
      footerButtons,
      dialogTag: dialog?.tagName,
      dialogClass: dialog?.className,
      contentClass: content?.className,
      dialogPosition: dialog ? getComputedStyle(dialog).position : "",
      dialogTop: dialog ? getComputedStyle(dialog).top : "",
      contentDisplay: content ? getComputedStyle(content).display : "",
      contentMaxHeight: content ? getComputedStyle(content).maxHeight : "",
      bodyMaxHeight: body ? getComputedStyle(body).maxHeight : "",
      bodyOverflowY: body ? getComputedStyle(body).overflowY : "",
    };
  });
  const box = await page.getByRole("dialog").boundingBox();
  const footerInside =
    geometry.footerButtons.length > 0 &&
    geometry.footerButtons.every(
      (button) =>
        button.width > 0 &&
        button.height > 0 &&
        button.left >= -1 &&
        button.top >= -1 &&
        button.right <= geometry.viewportWidth + 1 &&
        button.bottom <= geometry.viewportHeight + 1,
    );
  if (
    geometry.scrollWidth > geometry.viewportWidth + 1 ||
    !box ||
    box.x < -1 ||
    box.y < -1 ||
    box.x + box.width > geometry.viewportWidth + 1 ||
    box.y + box.height > geometry.viewportHeight + 1 ||
    !geometry.activeInside ||
    !footerInside
  ) {
    throw new Error(
      `unsafe ${label} geometry, focus or footer reachability: ${JSON.stringify({ box, ...geometry })}`,
    );
  }
}

async function contrastStates(page, violations) {
  const targets = violations.flatMap((violation) =>
    violation.targets.map((target) => target.split(" ").at(-1)),
  );
  return page.evaluate((selectors) =>
    selectors.map((selector) => {
      const element = document.querySelector(selector);
      if (!element) return { selector, missing: true };
      let background = element;
      let backgroundStyle = getComputedStyle(background);
      while (
        background.parentElement &&
        (backgroundStyle.backgroundColor === "rgba(0, 0, 0, 0)" ||
          backgroundStyle.backgroundColor === "transparent")
      ) {
        background = background.parentElement;
        backgroundStyle = getComputedStyle(background);
      }
      return {
        selector,
        className: element.className,
        color: getComputedStyle(element).color,
        background: backgroundStyle.backgroundColor,
        backgroundClass: background.className,
        theme: document.documentElement.dataset.theme,
        inSettings: !!element.closest(".rp-platform-settings"),
      };
    }), targets);
}

// An installation's page has one URL per tab; this moves to a tab of the installation the page is on.
async function gotoSettingsTab(page, tab) {
  await page.evaluate((name) => {
    const base = window.location.pathname.replace(/\/(overview|permissions|access|data|admin)(\/.*)?$/, "");
    window.history.pushState({}, "", `${base}/${name}`);
    window.dispatchEvent(new PopStateEvent("popstate"));
  }, tab);
  await page.waitForFunction((name) => window.location.pathname.endsWith(`/${name}`), tab);
  await page.getByRole("tab").first().waitFor();
}

// Client-side navigation keeps the language, theme and session the page already has.
async function navigateClient(page, pathname) {
  await page.evaluate((target) => {
    window.history.pushState({}, "", target);
    window.dispatchEvent(new PopStateEvent("popstate"));
  }, pathname);
}

// An installed platform app's card: the one that carries the more-actions menu (built-in apps have none).
function installedCard(page) {
  return page
    .locator(".rp-app-card")
    .filter({ has: page.locator(".rp-app-controls button:has(.anticon-ellipsis)") })
    .first();
}

// Opens the lifecycle dialog of one kind from the console and waits for its review. Resolves to the dialog and
// the control that opened it, which must get focus back when the dialog closes.
//   install: the catalogue page's Install button, then "Open to" -> Next (nothing selected keeps the app closed)
//   update:  the installed card's update button (the catalogue offers a newer version)
//   disable / uninstall / enable: the installed card's more-actions menu
async function openReview(page, options) {
  const kind = options.kind;
  let trigger;
  if (kind === "install") {
    await navigateClient(page, "/manage/apps/install");
    trigger = page.locator(".rp-market-card .rp-app-controls .ant-btn-primary").first();
    await trigger.waitFor({ timeout: 20_000 });
    await page.waitForFunction(
      () => !document.querySelector(".rp-market-card .rp-app-controls .ant-btn-primary")?.disabled,
    );
    await trigger.click();
    const dialog = page.getByRole("dialog");
    await dialog.waitFor({ timeout: 30_000 });
    // Step one: who the app is open to. Closed to everyone is the default, so Next goes on.
    await page.waitForTimeout(400);
    await assertDialogViewport(page, `${options.name} audience step`);
    await dialog.locator(".ant-modal-footer .ant-btn-primary").click();
    await dialog.locator(".rp-platform-review").waitFor({ timeout: 20_000 });
    return { dialog, trigger };
  }
  await navigateClient(page, "/manage/apps");
  const card = installedCard(page);
  await card.waitFor({ timeout: 20_000 });
  if (kind === "update") {
    // Switch version loads the versions when pressed; the version offered as the update is preselected.
    trigger = card.getByRole("button", { name: "Switch version", exact: true });
    await trigger.waitFor({ timeout: 20_000 });
    await trigger.click();
    const picker = page.getByRole("dialog");
    await picker.getByRole("radio").first().waitFor({ timeout: 30_000 });
    await picker.locator(".ant-modal-footer .ant-btn-primary").click();
  } else {
    trigger = card.locator(".rp-app-controls button:has(.anticon-ellipsis)");
    await trigger.click();
    const label = { disable: "Disable", enable: "Enable", uninstall: "Uninstall" }[kind];
    await page.getByRole("menuitem", { name: label, exact: true }).click();
  }
  const dialog = page.getByRole("dialog");
  await dialog.waitFor({ timeout: 30_000 });
  await dialog.locator(".rp-platform-review").waitFor({ timeout: 20_000 });
  return { dialog, trigger };
}

async function seriousViolations(audit) {
  return (await audit).filter(
    (violation) => violation.impact === "critical" || violation.impact === "serious",
  );
}

async function scenario(browser, url, options) {
  const context = await browser.newContext({
    viewport: options.viewport,
    colorScheme: options.dark ? "dark" : "light",
    deviceScaleFactor: options.deviceScaleFactor ?? 1,
  });
  const page = await context.newPage();
  page.setDefaultTimeout(15_000);
  const errors = [];
  page.on("pageerror", (error) => errors.push(error.message));
  page.on("console", (message) => {
    if (message.type() === "error") errors.push(`${message.text()} ${message.location().url}`);
  });
  try {
    await page.goto(url, { waitUntil: "networkidle" });
    await page.getByRole("button", { name: options.language, exact: true }).click();
    if (options.dark) {
      await page.getByRole("button", { name: "Toggle theme", exact: true }).click();
    }
    await page.getByRole("tab").first().waitFor();
    const { dialog, trigger } = await openReview(page, options);
    await page.waitForTimeout(400);
    await assertDialogViewport(page, options.name);
    const violations = await auditDialog(page);
    const serious = violations.filter(
      (violation) => violation.impact === "critical" || violation.impact === "serious",
    );
    if (serious.length) {
      const footerStyles = await page.evaluate(() =>
        [...document.querySelectorAll('[role="dialog"] .ant-modal-footer button')].map(
          (button) => ({
            text: button.textContent,
            color: getComputedStyle(button).color,
            textColor: getComputedStyle(button.querySelector("span") ?? button).color,
            background: getComputedStyle(button).backgroundColor,
          }),
        ),
      );
      throw new Error(
        `axe violations in ${options.name}: ${JSON.stringify(serious)} ${JSON.stringify(footerStyles)}`,
      );
    }
    const text = (await dialog.innerText()).toLowerCase().replace(/\s+/g, " ");
    for (const expected of options.reviewText ?? []) {
      if (!text.includes(expected.toLowerCase())) {
        throw new Error(`${options.name} review is missing "${expected}": ${text}`);
      }
    }
    const base = path.join(output, options.name);
    await page.screenshot({ path: `${base}-review.png`, fullPage: false });
    await dialog.locator(".ant-modal-footer button").first().click();
    await dialog.waitFor({ state: "hidden" });
    if (!(await trigger.evaluate((element) => document.activeElement === element))) {
      const active = await page.evaluate(() => document.activeElement?.outerHTML.slice(0, 200));
      throw new Error(`${options.name} did not restore trigger focus (active: ${active})`);
    }
    if (errors.length) {
      throw new Error(`${options.name} console/page errors: ${errors.join(" | ")}`);
    }
    return {
      name: options.name,
      violations,
      viewport: page.viewportSize(),
      zoom: options.zoom ?? 100,
    };
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
    // The way in is the console's own Install button, which opens the catalogue page.
    await page.getByRole("button", { name: "Install app", exact: true }).click();
    await page.waitForURL(/\/manage\/apps\/install$/);
    const install = page.locator(".rp-market-card .rp-app-controls .ant-btn-primary").first();
    await install.waitFor();
    await page.waitForFunction(
      () => !document.querySelector(".rp-market-card .rp-app-controls .ant-btn-primary")?.disabled,
    );
    await install.click();
    const dialog = page.getByRole("dialog");
    await dialog.waitFor({ timeout: 30_000 });
    await dialog.getByRole("button", { name: "Next", exact: true }).click();
    await dialog.locator(".rp-platform-review").waitFor();
    await dialog.getByRole("button", { name: "Confirm installation", exact: true }).click();
    await page.waitForURL(/[?&]operation=[0-9a-f-]{36}(?:&|$)/);
    // The step list repeats the stage title, so read the status line.
    await dialog.getByRole("status").getByText("Operation complete", { exact: true }).waitFor({
      timeout: 60_000,
    });
    await dialog.getByRole("button", { name: "Done", exact: true }).click();
    await dialog.waitFor({ state: "hidden" });
    // Installed now: the catalogue no longer offers it, and its card on the console offers disable, not install.
    // The empty-catalogue notice says the grid has loaded and holds nothing left to install.
    await page.getByText("This catalogue has no new apps to install.", { exact: true }).waitFor();
    if (await page.locator(".rp-market-card").count()) {
      throw new Error("completed operation still offered installation");
    }
    await navigateClient(page, "/manage/apps");
    const card = installedCard(page);
    await card.waitFor();
    if (await card.getByRole("button", { name: "Install", exact: true }).count()) {
      throw new Error("installed card still offered installation");
    }
    await card.locator(".rp-app-controls button:has(.anticon-ellipsis)").click();
    await page.getByRole("menuitem", { name: "Disable", exact: true }).waitFor();
    if (await page.getByRole("menuitem", { name: "Install", exact: true }).count()) {
      throw new Error("installed app's menu offered installation");
    }
    await page.keyboard.press("Escape");
    await page.screenshot({
      path: path.join(output, "install-complete-1440-en-light.png"),
      fullPage: true,
    });
    return { installed: true };
  } catch (error) {
    await page.screenshot({
      path: path.join(output, "install-complete-1440-en-light-failure.png"),
      fullPage: true,
    });
    throw new Error(
      `install scenario failed: ${(await page.locator("body").innerText()).slice(0, 2000)}`,
      { cause: error },
    );
  } finally {
    await context.close();
  }
}

// Disable and uninstall state their data boundary in the review; this reads those words from the real dialog.
async function boundaryScenario(browser, url, { kind, name, expected }) {
  const context = await browser.newContext({
    viewport: { width: 1440, height: 900 },
  });
  const page = await context.newPage();
  page.setDefaultTimeout(20_000);
  try {
    await page.goto(url, { waitUntil: "networkidle" });
    const { dialog, trigger } = await openReview(page, { kind, name });
    await page.waitForTimeout(400);
    const text = (await dialog.innerText()).toLowerCase().replace(/\s+/g, " ");
    for (const phrase of expected) {
      if (!text.includes(phrase)) {
        throw new Error(`${name} did not state its boundary "${phrase}": ${text}`);
      }
    }
    await assertDialogViewport(page, name);
    const violations = await seriousViolations(auditDialog(page));
    if (violations.length) {
      throw new Error(`${name} axe: ${JSON.stringify(violations)}`);
    }
    await page.screenshot({
      path: path.join(output, `${name}-1440-en-light.png`),
      fullPage: false,
    });
    await dialog.locator(".ant-modal-footer button").first().click();
    await dialog.waitFor({ state: "hidden" });
    if (!(await trigger.evaluate((element) => document.activeElement === element))) {
      throw new Error(`${name} did not restore trigger focus`);
    }
    return { violations };
  } finally {
    await context.close();
  }
}

async function progressScenario(browser, url, expected, name, options = {}) {
  const viewport = options.viewport ?? { width: 1440, height: 900 };
  const context = await browser.newContext({
    viewport,
    deviceScaleFactor: options.deviceScaleFactor ?? 1,
  });
  const page = await context.newPage();
  page.setDefaultTimeout(20_000);
  try {
    await page.goto(url, { waitUntil: "networkidle" });
    // Progress is the third step of the lifecycle dialog, resumed from ?operation=.
    const dialog = page.getByRole("dialog");
    await dialog.waitFor();
    const status = dialog.locator('div[role="status"]');
    await status.waitFor();
    // The status tag and the step list can both carry the stage name ("Queued"), so wait for the first match.
    await page.getByText(expected, { exact: true }).first().waitFor();
    const text = await status.innerText();
    if (!text.includes(expected)) {
      throw new Error(`progress state ${expected} missing: ${text}`);
    }
    if (await dialog.getByRole("link").count()) {
      throw new Error(`nonterminal progress exposed a completion link: ${expected}`);
    }
    await page.waitForTimeout(400);
    await assertDialogViewport(page, name);
    const geometry = await page.evaluate(() => ({
      scrollWidth: document.documentElement.scrollWidth,
      width: window.innerWidth,
    }));
    if (geometry.scrollWidth > geometry.width + 1) {
      throw new Error(`progress state overflowed: ${expected}`);
    }
    const violations = (await auditDialog(page)).filter(
      (violation) =>
        violation.impact === "critical" || violation.impact === "serious",
    );
    if (violations.length) {
      const placeholder = page.locator(".ant-select-placeholder");
      const placeholderState = (await placeholder.count())
        ? await placeholder.evaluate((element) => {
            const style = getComputedStyle(element);
            return {
              color: style.color,
              opacity: style.opacity,
              selector: element.closest(".ant-select")?.className ?? "",
            };
          })
        : null;
      throw new Error(
        `progress axe for ${expected}: ${JSON.stringify(violations)} ${JSON.stringify(placeholderState)}`,
      );
    }
    await page.screenshot({
      path: path.join(output, `${name}.png`),
      fullPage: true,
    });
    return {
      name,
      violations,
      viewport: page.viewportSize(),
      zoom: options.zoom ?? 100,
    };
  } finally {
    await context.close();
  }
}

async function settingsScenario(browser, url, options) {
  const context = await browser.newContext({
    viewport: options.viewport,
    deviceScaleFactor: options.deviceScaleFactor ?? 1,
  });
  const page = await context.newPage();
  page.setDefaultTimeout(20_000);
  try {
    await page.goto(url, { waitUntil: "networkidle" });
    // The bare installation URL lands on the Overview; the permissions and credentials sit on their own tab.
    await page.getByRole("tab").first().waitFor();
    await gotoSettingsTab(page, "permissions");
    if (options.language !== "English") {
      await page.getByRole("button", { name: options.language }).click();
    }
    if (options.dark) {
      await page.getByRole("button", { name: "Toggle theme" }).click();
      await page.waitForFunction(() => {
        const tag = document.querySelector(".rp-platform-success-tag");
        return (
          document.documentElement.dataset.theme === "dark" &&
          tag &&
          getComputedStyle(tag).color === "rgb(115, 209, 61)"
        );
      });
    }
    try {
      await page.locator(".rp-platform-settings").waitFor();
    } catch (error) {
      throw new Error(`settings page did not render: ${(await page.locator("body").innerText()).slice(0, 2000)}`, { cause: error });
    }
    if (options.capability) {
      await page.getByText(options.capability, { exact: false }).waitFor();
    }
    if (options.ready) {
      await gotoSettingsTab(page, "overview");
      await page.getByText(options.ready, { exact: true }).waitFor();
      await gotoSettingsTab(page, "permissions");
    }
    // Restore, repair and the retained data's deletion are on the Data tab.
    if (options.restorePausedText || options.repairButton || options.restoreButton || options.deleteButton) {
      await gotoSettingsTab(page, "data");
    }
    if (options.restorePausedText) {
      await page.getByText(options.restorePausedText, {
        exact: false,
      }).waitFor();
      const restore = page.getByRole("button", {
        name: options.pausedRestoreButton,
      });
      if ((await restore.count()) !== 1 || !(await restore.isDisabled())) {
        throw new Error("restore remained enabled while control was paused");
      }
    }
    const geometry = await page.evaluate(() => ({
      scrollWidth: document.documentElement.scrollWidth,
      width: window.innerWidth,
    }));
    if (geometry.scrollWidth > geometry.width + 1) {
      throw new Error(`settings overflowed at ${options.viewport.width}px`);
    }
    const clippedTags = await page.locator(".rp-platform-settings .ant-tag").evaluateAll(
      (tags) => tags.filter((tag) => tag.scrollWidth > tag.clientWidth + 1).length,
    );
    if (clippedTags) {
      throw new Error(`settings clipped ${clippedTags} tag(s) at ${options.viewport.width}px`);
    }
    if (options.repairButton) {
      await page.getByRole("button", { name: options.repairButton }).click();
      const dialog = page.getByRole("dialog");
      await dialog.waitFor();
      await waitForReviewDialogStyles(page);
      await assertDialogViewport(page, "repair review");
      const text = await dialog.innerText();
      if (!text.includes(options.repairBoundary)) {
        throw new Error(`repair review boundary missing: ${text}`);
      }
      const dialogViolations = (await auditDialog(page)).filter(
        (violation) =>
          violation.impact === "critical" || violation.impact === "serious",
      );
      if (dialogViolations.length) {
        throw new Error(`repair review axe at ${options.viewport.width}px: ${JSON.stringify(dialogViolations)}`);
      }
      await page.screenshot({
        path: path.join(output, `${options.name}-review.png`),
        fullPage: false,
      });
      await dialog.locator(".ant-modal-footer button").first().click();
      await dialog.waitFor({ state: "hidden" });
    }
    if (options.restoreButton) {
      await page.getByRole("button", { name: options.restoreButton }).click();
      const dialog = page.getByRole("dialog");
      await dialog.waitFor();
      await waitForReviewDialogStyles(page);
      await assertDialogViewport(page, "restore review");
      const text = await dialog.innerText();
      if (!text.includes(options.restoreBoundary)) {
        throw new Error(`restore review boundary missing: ${text}`);
      }
      const dialogViolations = (await auditDialog(page)).filter(
        (violation) =>
          violation.impact === "critical" || violation.impact === "serious",
      );
      if (dialogViolations.length) {
        throw new Error(`restore review axe at ${options.viewport.width}px: ${JSON.stringify(dialogViolations)}`);
      }
      await page.screenshot({
        path: path.join(output, `${options.name}-review.png`),
        fullPage: false,
      });
      await dialog.locator(".ant-modal-footer button").first().click();
      await dialog.waitFor({ state: "hidden" });
    }
    if (options.deleteButton) {
      await page.getByRole("button", { name: options.deleteButton }).click();
      const dialog = page.getByRole("dialog");
      await dialog.waitFor();
      await waitForReviewDialogStyles(page);
      await assertDialogViewport(page, "delete-data review");
      const text = await dialog.innerText();
      if (!text.includes("exact namespace and recorded recovery sets")) {
        throw new Error(`delete-data bound scope missing: ${text}`);
      }
      const dialogViolations = (await auditDialog(page)).filter(
        (violation) =>
          violation.impact === "critical" || violation.impact === "serious",
      );
      if (dialogViolations.length) {
        throw new Error(`delete-data axe at ${options.viewport.width}px: ${JSON.stringify(dialogViolations)}`);
      }
      await page.screenshot({
        path: path.join(output, `${options.name}-review.png`),
        fullPage: false,
      });
      await dialog.locator(".ant-modal-footer button").first().click();
      await dialog.waitFor({ state: "hidden" });
    }
    // Colour transitions (the theme switch fades the tab bar too) must finish before contrast is measured.
    await page.waitForTimeout(600);
    const violations = (await auditPage(page)).filter(
      (violation) =>
        violation.impact === "critical" || violation.impact === "serious",
    );
    if (violations.length) {
      throw new Error(`settings axe at ${options.viewport.width}px: ${JSON.stringify(violations)} ${JSON.stringify(await contrastStates(page, violations))}`);
    }
    await page.screenshot({
      path: path.join(output, `${options.name}.png`),
      fullPage: true,
    });
    return {
      name: options.name,
      violations,
      viewport: page.viewportSize(),
      zoom: options.zoom ?? 100,
    };
  } finally {
    await context.close();
  }
}

async function main() {
  let browser;
  try {
    browser = await chromium.launch({
      headless: true,
      ...(process.env.APP_INSTALL_BROWSER_EXECUTABLE
        ? { executablePath: process.env.APP_INSTALL_BROWSER_EXECUTABLE }
        : {}),
    });
    const report = {
      gitCommit: execFileSync("git", ["rev-parse", "HEAD"], {
        cwd: root,
        encoding: "utf8",
      }).trim(),
      scenarios: [],
      install: null,
      disable: null,
      uninstall: null,
    };
    // `market` serves the owned synthetic catalogue: the install scenario offers a new app, the updateable
    // scenario offers the newer version of the installed one.
    async function withFixture(mode, run, market = false) {
      const { fixture, outputLog } = startFixture(mode, market);
      try {
        return await run(await fixtureUrl(fixture, outputLog));
      } finally {
        await closeFixture(fixture);
      }
    }
    await withFixture(
      "install",
      async (url) => {
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
          report.scenarios.push(
            await scenario(browser, url, { kind: "install", ...options }),
          );
        }
        report.scenarios.push(
          await scenario(browser, url, {
            kind: "install",
            name: "install-review-1440-en-light-zoom-200",
            language: "English",
            dark: false,
            ...desktopZoom200,
          }),
        );
        report.install = await installScenario(browser, url);
        report.scenarios.push(
          await scenario(browser, url, {
            kind: "disable",
            name: "disable-review-1440-en-light-zoom-200",
            language: "English",
            dark: false,
            ...desktopZoom200,
          }),
          await scenario(browser, url, {
            kind: "uninstall",
            name: "uninstall-review-1440-en-light-zoom-200",
            language: "English",
            dark: false,
            ...desktopZoom200,
          }),
        );
        report.disable = await boundaryScenario(browser, url, {
          kind: "disable",
          name: "disable-review",
          expected: ["data is retained"],
        });
        report.uninstall = await boundaryScenario(browser, url, {
          kind: "uninstall",
          name: "uninstall-review",
          expected: [
            "other plugins are unaffected",
            "no other installation uses it",
          ],
        });
      },
      true,
    );
    await withFixture(
      "updateable",
      async (url) => {
        report.scenarios.push(
          await scenario(browser, url, {
            kind: "update",
            name: "update-review-1440-en-light",
            // The card's update button must open an update review, for the staged version.
            reviewText: ["Confirm update plan", "1.0.0-beta.2"],
            language: "English",
            dark: false,
            viewport: { width: 1440, height: 900 },
          }),
          await scenario(browser, url, {
            kind: "update",
            name: "update-review-1440-en-light-zoom-200",
            // The card's update button must open an update review, for the staged version.
            reviewText: ["Confirm update plan", "1.0.0-beta.2"],
            language: "English",
            dark: false,
            ...desktopZoom200,
          }),
        );
      },
      true,
    );
    await withFixture("disabled", async (url) => {
      report.scenarios.push(
        await scenario(browser, url, {
          kind: "enable",
          name: "enable-review-1440-en-light",
          language: "English",
          dark: false,
          viewport: { width: 1440, height: 900 },
        }),
        await scenario(browser, url, {
          kind: "enable",
          name: "enable-review-1440-en-light-zoom-200",
          language: "English",
          dark: false,
          ...desktopZoom200,
        }),
      );
    });
    for (const [mode, expected, name] of [
      ["progress", "Queued", "progress-queued"],
      ["paused", "Application control is paused pending recovery review.", "progress-paused"],
      ["recovery", "Recovery review required", "progress-recovery"],
    ]) {
      await withFixture(mode, async (url) => {
        report.scenarios.push(
          await progressScenario(browser, url, expected, name),
          await progressScenario(
            browser,
            url,
            expected,
            `${name}-zoom-200`,
            desktopZoom200,
          ),
        );
      });
    }
    await withFixture("settings", async (url) => {
      report.scenarios.push(
        await settingsScenario(browser, url, {
          name: "settings-1440-en-light",
          language: "English",
          dark: false,
          viewport: { width: 1440, height: 900 },
          title: "Application settings",
          capability: "Store application files",
          ready: "No configuration gap detected",
        }),
        await settingsScenario(browser, url, {
          name: "settings-320-zh-cn-dark",
          language: "Simplified Chinese",
          dark: true,
          viewport: { width: 320, height: 800 },
          title: "应用设置",
          capability: "保存应用文件",
          ready: "未发现配置缺口",
        }),
        await settingsScenario(browser, url, {
          name: "settings-1440-en-light-zoom-200",
          language: "English",
          dark: false,
          title: "Application settings",
          capability: "Store application files",
          ready: "No configuration gap detected",
          ...desktopZoom200,
        }),
      );
    });
    await withFixture("delete-data", async (url) => {
      report.scenarios.push(
        await settingsScenario(browser, url, {
          name: "settings-delete-data-review-1440-en-light",
          language: "English",
          dark: false,
          viewport: { width: 1440, height: 900 },
          title: "Application settings",
          deleteButton: "Delete retained data",
        }),
        await settingsScenario(browser, url, {
          name: "settings-delete-data-review-1440-en-light-zoom-200",
          language: "English",
          dark: false,
          title: "Application settings",
          deleteButton: "Delete retained data",
          ...desktopZoom200,
        }),
      );
    });
    await withFixture("repair", async (url) => {
      report.scenarios.push(
        await settingsScenario(browser, url, {
          name: "settings-repair-review-1440-en-light",
          language: "English",
          dark: false,
          viewport: { width: 1440, height: 900 },
          title: "Application settings",
          repairButton: "Review repair",
          repairBoundary: "creates a fresh checkpoint",
        }),
        await settingsScenario(browser, url, {
          name: "settings-repair-review-320-zh-cn-light",
          language: "Simplified Chinese",
          dark: false,
          viewport: { width: 320, height: 800 },
          title: "应用设置",
          repairButton: "审核修复",
          repairBoundary: "创建新的恢复点",
        }),
        await settingsScenario(browser, url, {
          name: "settings-repair-review-1440-en-light-zoom-200",
          language: "English",
          dark: false,
          title: "Application settings",
          repairButton: "Review repair",
          repairBoundary: "creates a fresh checkpoint",
          ...desktopZoom200,
        }),
      );
    });
    await withFixture("restore", async (url) => {
      report.scenarios.push(
        await settingsScenario(browser, url, {
          name: "settings-restore-review-1440-en-light",
          language: "English",
          dark: false,
          viewport: { width: 1440, height: 900 },
          title: "Application settings",
          restoreButton: "Review restore",
          restoreBoundary: "Writes after that checkpoint may be lost",
        }),
        await settingsScenario(browser, url, {
          name: "settings-restore-review-320-zh-cn-light",
          language: "Simplified Chinese",
          dark: false,
          viewport: { width: 320, height: 800 },
          title: "应用设置",
          restoreButton: "审核恢复",
          restoreBoundary: "检查点之后的写入可能丢失",
        }),
        await settingsScenario(browser, url, {
          name: "settings-restore-review-1440-en-light-zoom-200",
          language: "English",
          dark: false,
          title: "Application settings",
          restoreButton: "Review restore",
          restoreBoundary: "Writes after that checkpoint may be lost",
          ...desktopZoom200,
        }),
      );
    });
    await withFixture("restore-paused", async (url) => {
      report.scenarios.push(
        await settingsScenario(browser, url, {
          name: "settings-restore-paused-1440-en-light",
          language: "English",
          dark: false,
          viewport: { width: 1440, height: 900 },
          title: "Application settings",
          restorePausedText:
            "Application control is paused. Restore planning and admission remain disabled",
          pausedRestoreButton: "Review restore",
        }),
        await settingsScenario(browser, url, {
          name: "settings-restore-paused-320-zh-cn-light",
          language: "Simplified Chinese",
          dark: false,
          viewport: { width: 320, height: 800 },
          title: "应用设置",
          restorePausedText:
            "应用控制已暂停。管理员完成恢复审核前，不能规划或确认恢复。",
          pausedRestoreButton: "审核恢复",
        }),
        await settingsScenario(browser, url, {
          name: "settings-restore-paused-1440-en-light-zoom-200",
          language: "English",
          dark: false,
          title: "Application settings",
          restorePausedText:
            "Application control is paused. Restore planning and admission remain disabled",
          pausedRestoreButton: "Review restore",
          ...desktopZoom200,
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
