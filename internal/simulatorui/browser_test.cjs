// Run with NODE_PATH pointing at the bundled Playwright dependencies.
// This loopback fake API never opens provider, SIP, carrier or media connections.
const { chromium } = require("playwright");
const http = require("node:http");
const fs = require("node:fs");
const path = require("node:path");
const assert = require("node:assert/strict");
const assets = path.join(__dirname, "assets");
const scenario = JSON.parse(
  fs.readFileSync(
    path.join(__dirname, "../../scenarios/simulator/cooperative.json"),
    "utf8",
  ),
);
const calls = [],
  external = [],
  consoleErrors = [];
let runs = [],
  stopped = "",
  transcriptFetches = 0,
  failNextRunList = false,
  prepareCount = 0;
const active = {
  asr: "mock",
  llm: "mock",
  tts: "mock",
  backend_model: "mock",
  voice: "default",
  language: "en",
  speed: 1,
  client_rate: 16000,
  allow_barge_in: true,
  on_new_query: "cut",
};
const server = http.createServer(async (req, res) => {
  calls.push(req.url);
  res.setHeader("Cache-Control", "no-store");
  res.setHeader(
    "Content-Security-Policy",
    "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'",
  );
  if (!req.url.startsWith("/sim/v1/")) {
    const names = {
      "/sim/": "index.html",
      "/sim/app.js": "app.js",
      "/sim/style.css": "style.css",
    };
    const file = names[req.url];
    if (!file) {
      res.writeHead(404);
      res.end();
      return;
    }
    res.setHeader(
      "Content-Type",
      file.endsWith(".js")
        ? "text/javascript"
        : file.endsWith(".css")
          ? "text/css"
          : "text/html",
    );
    res.end(fs.readFileSync(path.join(assets, file)));
    return;
  }
  if (req.headers.authorization !== "Bearer fake-test-token") {
    res.writeHead(401);
    res.end("unauthorized");
    return;
  }
  let body = "";
  for await (const chunk of req) body += chunk;
  const send = (value) => {
    res.setHeader("Content-Type", "application/json");
    res.end(JSON.stringify(value));
  };
  const fail = (text) => {
    res.writeHead(400);
    res.end(text);
  };
  if (req.url === "/sim/v1/config")
    return send({ active, constraints: { receiving_only: true } });
  if (req.url === "/sim/v1/scenarios") return send({ scenarios: [scenario] });
  if (req.url === "/sim/v1/readiness") return send({ gateway_ready: false });
  if (req.url === "/sim/v1/validate") {
    if (body.includes('"unknown_field"'))
      return fail('unknown field "unknown_field"');
    if ((body.match(/"schema_version"/g) || []).length > 1)
      return fail('duplicate key "schema_version"');
    if ((body.match(/"nested_duplicate"/g) || []).length > 1)
      return fail('duplicate key "nested_duplicate"');
    try {
      return send({
        scenario: JSON.parse(body),
        scenario_hash: "test-scenario-hash",
        prompt_hash: "test-prompt-hash",
      });
    } catch {
      return fail("invalid JSON");
    }
  }
  if (req.url === "/sim/v1/runs" && req.method === "GET") {
    if (failNextRunList) {
      failNextRunList = false;
      res.writeHead(503);
      res.end("test recent-run list unavailable");
      return;
    }
    return send({ runs });
  }
  if (req.url === "/sim/v1/runs" && req.method === "POST") {
    prepareCount++;
    failNextRunList = true;
    assert(req.headers["idempotency-key"]);
    const s = JSON.parse(body);
    assert.equal(s.goal, "Confirm Thursday afternoon");
    assert.equal(s.retain_transcript, true);
    const run = {
      run_id: "run-exact-123",
      phase: "waiting",
      dial_target_reference: "sip:run-exact-123@simulator.invalid",
      reservation_expires_at: "2026-10-16T12:00:00Z",
      binding: {},
    };
    runs = [run, { run_id: "other-run", phase: "completed", binding: {} }];
    return send(run);
  }
  const match = req.url.match(/^\/sim\/v1\/runs\/([^/?]+)(?:\/([^?]+))?/);
  if (match) {
    const id = match[1],
      op = match[2],
      run = runs.find((r) => r.run_id === id);
    if (!run) {
      res.writeHead(404);
      res.end("run not found");
      return;
    }
    if (!op) return send(run);
    if (op === "events")
      return send({
        events: [
          {
            sequence: 1,
            kind: "prepared",
            at_ms: 0,
            reason: "<img src=x onerror=alert(1)>",
          },
        ],
        next: 1,
        truncated: true,
      });
    if (op === "stop") {
      stopped = id;
      run.phase = "completed";
      return send(run);
    }
    if (op === "report")
      return send({
        run_id: id,
        phase: "completed",
        checks: [
          {
            name: "goal",
            result: "indeterminate",
            evidence: "semantic evaluator absent",
          },
        ],
        action_outcomes: [{ id: "interrupt", result: "executed", at_ms: 500 }],
        assistant_response_gaps: [
          {
            value_ms: 380,
            valid: true,
            evidence: "application_audio_boundary",
          },
          { valid: false, reason: "overlapping speech" },
        ],
        simulator_response_gaps: [],
        failures: ["<script>alert(1)</script>"],
        transcript_retained: true,
        invalid_observations: 1,
        evidence: "application_audio_boundary",
      });
    if (op === "transcript") {
      transcriptFetches++;
      return send({
        schema_version: 1,
        run_id: id,
        scenario_hash: "s",
        prompt_hash: "p",
        truncated: false,
        entries: [{ role: "customer", text: "Synthetic hello" }],
      });
    }
  }
  res.writeHead(404);
  res.end("missing");
});
(async () => {
  let browser;
  try {
    await new Promise((resolve, reject) => {
      server.once("error", reject);
      server.listen(0, "127.0.0.1", resolve);
    });
    const base = "http://127.0.0.1:" + server.address().port;
    browser = await chromium.launch({ headless: true });
    const page = await browser.newPage({
      viewport: { width: 1360, height: 1000 },
    });
    page.setDefaultTimeout(10000);
    page.on("pageerror", (err) => consoleErrors.push(err.message));
    page.on("console", (msg) => {
      if (
        msg.type() === "error" &&
        !msg.text().includes("400") &&
        !msg.text().includes("401") &&
        !msg.text().includes("503")
      )
        consoleErrors.push(msg.text());
    });
    await page.route("**/*", async (route) => {
      if (
        !route
          .request()
          .url()
          .startsWith(base + "/")
      ) {
        external.push(route.request().url());
        return route.abort();
      }
      return route.continue();
    });
    await page.goto(base + "/sim/");
    assert(await page.locator("#workspace").evaluate((el) => el.disabled));
    await page.fill("#token", "fake-test-token");
    await page.click("#connect");
    await page.waitForFunction(
      () => !document.getElementById("workspace").disabled,
    );
    assert.equal(await page.inputValue("#token"), "");
    assert.match(
      await page.textContent("#gateway-status"),
      /Gateway unavailable/,
    );
    await page.screenshot({
      path: "/private/tmp/simulator-workbench-desktop.png",
      fullPage: true,
    });
    await page.fill("#goal", "Confirm Thursday afternoon");
    await page.check("#retain");
    await page
      .getByText("Import & export scenario JSON", { exact: true })
      .click();
    await page.fill(
      "#import-json",
      JSON.stringify({ ...scenario, unknown_field: true }),
    );
    await page.click("#import");
    await page.waitForFunction(() =>
      document.getElementById("message").textContent.includes("unknown field"),
    );
    await page.fill("#import-json", '{"schema_version":1,"schema_version":1}');
    await page.click("#import");
    await page.waitForFunction(() =>
      document.getElementById("message").textContent.includes("duplicate key"),
    );
    await page.fill(
      "#import-json",
      JSON.stringify({
        ...scenario,
        goal: "Confirm Thursday afternoon",
        retain_transcript: true,
        behavior: ["Keep this instruction\nacross two lines"],
      }),
    );
    await page.click("#import");
    await page.waitForFunction(() =>
      document
        .getElementById("message")
        .textContent.includes("Scenario imported"),
    );
    assert.equal(
      JSON.parse(await page.inputValue("#behavior"))[0],
      "Keep this instruction\nacross two lines",
    );
    await page.fill(
      "#facts",
      '{"nested_duplicate":"one","nested_duplicate":"two"}',
    );
    await page.click("#validate");
    await page.waitForFunction(() =>
      document
        .getElementById("message")
        .textContent.includes("nested_duplicate"),
    );
    await page.fill("#facts", '{"available_day":"Thursday"}');
    const scenarioDownload = page.waitForEvent("download");
    await page.click("#export-scenario");
    const sd = await scenarioDownload;
    const exported = JSON.parse(fs.readFileSync(await sd.path(), "utf8"));
    assert.equal(exported.goal, "Confirm Thursday afternoon");
    assert.equal(exported.retain_transcript, true);
    assert.equal(
      exported.behavior[0],
      "Keep this instruction\nacross two lines",
    );
    await page.click('[data-view="providers"]');
    assert.equal(
      await page.locator("#draft-rate").getAttribute("readonly"),
      "",
    );
    assert.equal(await page.inputValue("#draft-rate"), "16000");
    assert.equal(await page.locator("select#draft-rate").count(), 0);
    await page.selectOption("#draft-tts", "minimax");
    await page.fill("#draft-voice", "test-voice");
    const profileDownload = page.waitForEvent("download");
    await page.click("#profile-form button");
    const pd = await profileDownload;
    const profile = JSON.parse(fs.readFileSync(await pd.path(), "utf8"));
    assert.equal(profile.tts, "minimax");
    assert.equal(profile.client_rate, 16000);
    assert.equal(profile.allow_provider_override, false);
    assert.equal(profile.realtime.store_calls, false);
    assert(!JSON.stringify(profile).includes("fake-test-token"));
    assert.match(await page.textContent("#active-settings"), /mock/);
    await page.click('[data-view="scenario"]');
    await page.click("#prepare");
    await page.waitForFunction(() =>
      document
        .getElementById("run-info")
        .textContent.includes("sip:run-exact-123@simulator.invalid"),
    );
    await page.waitForFunction(() =>
      document
        .getElementById("message")
        .textContent.includes("Recent runs could not be refreshed"),
    );
    assert.equal(prepareCount, 1);
    assert.match(await page.textContent("#run-info"), /run-exact-123/);
    assert.equal(await page.inputValue("#run-select"), "run-exact-123");
    assert.match(await page.textContent("#run-info"), /2026-10-16T12:00:00Z/);
    await page.click("#refresh-runs");
    await page.waitForFunction(() =>
      document
        .getElementById("message")
        .textContent.includes("Recent runs refreshed"),
    );
    assert.equal(await page.inputValue("#run-select"), "run-exact-123");
    assert.equal(prepareCount, 1);
    await page.waitForFunction(
      () => document.getElementById("events").children.length > 0,
    );
    assert.match(await page.textContent("#event-warning"), /truncated/);
    assert.equal(await page.locator("#events img").count(), 0);
    assert(await page.locator("#download-transcript").isDisabled());
    await page.click("#stop");
    await page.waitForFunction(
      () => !document.getElementById("load-report").disabled,
    );
    assert.equal(stopped, "run-exact-123");
    await page.click("#load-report");
    await page.waitForFunction(() =>
      document.getElementById("report").textContent.includes("indeterminate"),
    );
    assert.match(await page.textContent("#report"), /overlapping speech/);
    assert.equal(await page.locator("#report script").count(), 0);
    assert.equal(transcriptFetches, 0);
    assert(await page.locator("#download-transcript").isDisabled());
    const reportDownload = page.waitForEvent("download");
    await page.click("#download-report");
    const rd = await reportDownload;
    assert.equal(
      JSON.parse(fs.readFileSync(await rd.path(), "utf8")).run_id,
      "run-exact-123",
    );
    await page.check("#transcript-consent");
    const transcriptDownload = page.waitForEvent("download");
    await page.click("#download-transcript");
    const td = await transcriptDownload;
    assert.equal(
      JSON.parse(fs.readFileSync(await td.path(), "utf8")).schema_version,
      1,
    );
    assert.equal(transcriptFetches, 1);
    await page.setViewportSize({ width: 375, height: 812 });
    for (const name of ["scenario", "providers", "runs"]) {
      await page.click('[data-view="' + name + '"]');
      assert(
        await page.evaluate(
          () => document.documentElement.scrollWidth <= innerWidth,
        ),
        "mobile overflow " + name,
      );
      await page.screenshot({
        path: "/private/tmp/simulator-workbench-mobile-" + name + ".png",
        fullPage: true,
      });
    }
    await page.screenshot({
      path: "/private/tmp/simulator-workbench-mobile.png",
      fullPage: true,
    });
    const requestCount = calls.length;
    await page.click("#disconnect");
    await page.waitForTimeout(1800);
    assert(await page.locator("#workspace").evaluate((el) => el.disabled));
    assert.equal(await page.inputValue("#token"), "");
    assert.equal(calls.length, requestCount);
    assert.equal(
      await page.evaluate(() => localStorage.length + sessionStorage.length),
      0,
    );
    await page.fill("#token", "wrong-token");
    await page.click("#connect");
    await page.waitForFunction(() =>
      document
        .getElementById("message")
        .textContent.includes("Authentication failed"),
    );
    assert(await page.locator("#workspace").evaluate((el) => el.disabled));
    assert.equal(await page.inputValue("#token"), "");
    assert.deepEqual(external, []);
    assert.deepEqual(consoleErrors, []);
    console.log(
      "PASS: connect, readiness, raw import errors, edited duplicate fields, scenario/profile/report/transcript exports, exact prepare/stop target, inert text, event truncation, disconnect, 375px panels, no external network or console errors",
    );
  } finally {
    if (browser) await browser.close();
    await new Promise((resolve) => server.close(resolve));
  }
})().catch((err) => {
  console.error(err);
  process.exitCode = 1;
});
