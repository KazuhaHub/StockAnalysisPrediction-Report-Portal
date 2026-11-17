const { createRequire } = require("node:module");
const { createServer } = require("node:http");
const { readFileSync } = require("node:fs");
const path = require("node:path");

const root = path.resolve(__dirname, "..");
const packagePath = process.env.PLAYWRIGHT_PACKAGE_JSON;
if (!packagePath || !path.isAbsolute(packagePath)) {
  throw new Error("PLAYWRIGHT_PACKAGE_JSON must name an absolute package file");
}
const requirePlaywright = createRequire(packagePath);
const { chromium } = requirePlaywright("playwright");

const source = readFileSync(
  path.join(root, "internal/appplatform/frontend_document.go"),
  "utf8",
);
const policy = source.match(/const frontendDocumentCSP = "([^"]+)"/)?.[1];
if (!policy) {
  throw new Error("frontend document CSP was not found");
}
if (
  !policy.includes("sandbox allow-scripts") ||
  policy.includes("allow-same-origin") ||
  !policy.includes("connect-src 'none'")
) {
  throw new Error("frontend document CSP no longer has the reviewed boundary");
}

let privateRequests = 0;
let popupRequests = 0;
const hostileScript = `
(async()=>{
  const result={};
  const denied=async(name,fn)=>{
    try { await fn(); result[name]=false }
    catch { result[name]=true }
  };
  await denied("parentDomDenied",()=>parent.document.body);
  await denied("hostStorageDenied",()=>parent.localStorage.length);
  try { result.cookieDenied=document.cookie==="" }
  catch { result.cookieDenied=true }
  await denied("topNavigationDenied",()=>{top.location.href="https://example.invalid/escaped"});
  try {
    const popup=window.open("/popup","_blank");
    result.popupDenied=popup===null;
    if(popup) popup.close();
  } catch {
    result.popupDenied=true;
  }
  try {
    await fetch("/api/private",{credentials:"include"});
    result.networkDenied=false;
  } catch {
    result.networkDenied=true;
  }
  parent.postMessage({type:"security-result",result},"*");
})();
`;
const child = `<!doctype html><meta charset="utf-8"><title>Hostile app</title>` +
  `<script src="data:text/javascript;base64,${Buffer.from(hostileScript).toString("base64")}"></script>`;

const host = `<!doctype html><meta charset="utf-8"><title>Security host</title>
<script>
document.cookie="host_session=fixture-secret; Path=/; SameSite=Lax";
localStorage.setItem("host-private","fixture-secret");
window.securityResult=new Promise((resolve)=>{
  addEventListener("message",(event)=>{
    if(event.source!==document.querySelector("iframe")?.contentWindow)return;
    if(event.origin!=="null"||event.data?.type!=="security-result")return;
    resolve(event.data.result);
  });
});
</script>
<iframe sandbox="allow-scripts" referrerpolicy="no-referrer" src="/hostile"></iframe>`;

async function main() {
  const server = createServer((request, response) => {
    if (request.url === "/") {
      response.writeHead(200, {
        "Content-Type": "text/html; charset=utf-8",
        "Cache-Control": "no-store",
      });
      response.end(host);
      return;
    }
    if (request.url === "/hostile") {
      response.writeHead(200, {
        "Content-Type": "text/html; charset=utf-8",
        "Cache-Control": "no-store",
        "Content-Security-Policy": policy,
      });
      response.end(child);
      return;
    }
    if (request.url === "/api/private") {
      privateRequests++;
      response.writeHead(200, { "Content-Type": "application/json" });
      response.end('{"secret":"fixture-sensitive"}');
      return;
    }
    if (request.url === "/popup") {
      popupRequests++;
      response.writeHead(200, { "Content-Type": "text/html; charset=utf-8" });
      response.end("<!doctype html><title>Unexpected popup</title>");
      return;
    }
    response.writeHead(404);
    response.end("not found");
  });
  await new Promise((resolve, reject) => {
    server.once("error", reject);
    server.listen(0, "127.0.0.1", resolve);
  });

  let browser;
  try {
    const origin = `http://127.0.0.1:${server.address().port}`;
    browser = await chromium.launch({ headless: true, timeout: 10000 });
    const page = await browser.newPage();
    await page.goto(origin + "/", { waitUntil: "domcontentloaded" });
    const result = await page.evaluate(() =>
      Promise.race([
        window.securityResult,
        new Promise((_, reject) =>
          setTimeout(() => reject(new Error("child result timeout")), 5000),
        ),
      ]),
    );
    await page.waitForTimeout(250);
    const hostState = await page.evaluate(() => ({
      url: location.href,
      cookie: document.cookie,
      storage: localStorage.getItem("host-private"),
    }));
    const assertions = {
      parentDomDenied: result.parentDomDenied === true,
      hostStorageDenied: result.hostStorageDenied === true,
      cookieDenied: result.cookieDenied === true,
      topNavigationDenied:
        result.topNavigationDenied === true &&
        hostState.url === origin + "/",
      popupDenied: result.popupDenied === true && popupRequests === 0,
      networkDenied: result.networkDenied === true && privateRequests === 0,
      hostSecretsUnchanged:
        hostState.cookie.includes("host_session=fixture-secret") &&
        hostState.storage === "fixture-secret",
    };
    const passed = Object.values(assertions).every(Boolean);
    process.stdout.write(
      JSON.stringify(
        {
          format: "portal.app-platform.security-document-browser/1-dev.1",
          passed,
          csp: policy,
          assertions,
          result,
          privateRequests,
          popupRequests,
          hostState,
        },
        null,
        2,
      ) + "\n",
    );
    if (!passed) process.exitCode = 1;
  } finally {
    if (browser) await browser.close();
    await new Promise((resolve) => server.close(resolve));
  }
}

main().catch((error) => {
  console.error(error?.stack || error);
  process.exitCode = 1;
});
