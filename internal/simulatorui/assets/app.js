"use strict";
(() => {
  const $ = (id) => document.getElementById(id);
  const terminal = (phase) =>
    [
      "completed",
      "rejected",
      "expired",
      "failed",
      "cancelled",
      "aborted",
    ].includes(phase);
  let token = "",
    generation = 0,
    presets = [],
    selected = "",
    phase = "",
    cursor = 0,
    report = null;
  let view = "scenario",
    pollTimer = null,
    pollEpoch = 0,
    pollRequests = new Set(),
    requests = new Set(),
    busy = false;
  function message(text, error = false) {
    $("message").hidden = !text;
    $("message").textContent = text;
    $("message").classList.toggle("error", error);
  }
  function stopPolling() {
    clearTimeout(pollTimer);
    pollTimer = null;
    pollEpoch++;
    for (const c of pollRequests) c.abort();
    pollRequests.clear();
  }
  function disconnect(reason = "Disconnected. Control token cleared.") {
    generation++;
    token = "";
    stopPolling();
    for (const c of requests) c.abort();
    requests.clear();
    presets = [];
    selected = "";
    phase = "";
    report = null;
    $("token").value = "";
    $("workspace").disabled = true;
    $("disconnect").disabled = true;
    $("connect").disabled = false;
    $("token").disabled = false;
    $("connection-dot").classList.remove("online");
    $("connection-status").textContent = "Disconnected";
    $("gateway-status").textContent = "Gateway readiness has not been checked.";
    $("events").replaceChildren();
    $("run-info").replaceChildren();
    $("active-settings").replaceChildren();
    $("run-select").replaceChildren(new Option("Select a run…", ""));
    $("phase").textContent = "NO RUN SELECTED";
    $("monitor-status").textContent = "No monitoring active.";
    $("event-warning").textContent = "";
    $("report").replaceChildren();
    $("import-json").value = "";
    $("import-file").value = "";
    buttons();
    message(reason);
  }
  async function api(path, options = {}, polling = false) {
    const c = new AbortController(),
      current = generation;
    requests.add(c);
    if (polling) pollRequests.add(c);
    let timedOut = false;
    const timeout = setTimeout(() => {
      timedOut = true;
      c.abort();
    }, 10000);
    try {
      const response = await fetch("/sim/v1/" + path, {
        ...options,
        cache: "no-store",
        credentials: "omit",
        redirect: "error",
        signal: c.signal,
        headers: {
          Authorization: "Bearer " + token,
          ...(options.body ? { "Content-Type": "application/json" } : {}),
          ...options.headers,
        },
      });
      if (current !== generation)
        throw new DOMException("Disconnected", "AbortError");
      if (response.status === 401 || response.status === 403) {
        disconnect(
          "Authentication failed. Reconnect using this simulator’s control token.",
        );
        throw new Error("Authentication failed");
      }
      if (!response.ok)
        throw new Error(
          (await response.text()).slice(0, 1600) || "HTTP " + response.status,
        );
      return await response.json();
    } catch (err) {
      if (timedOut)
        throw new Error(
          "Control request timed out. Refresh or reconnect to retry.",
        );
      throw err;
    } finally {
      clearTimeout(timeout);
      requests.delete(c);
      pollRequests.delete(c);
    }
  }
  async function task(action) {
    if (busy) return;
    busy = true;
    $("workspace").disabled = true;
    try {
      await action();
    } catch (err) {
      if (err.name !== "AbortError") message(err.message, true);
    } finally {
      busy = false;
      $("workspace").disabled = !token;
      buttons();
    }
  }
  function showView(next) {
    stopPolling();
    view = next;
    for (const element of document.querySelectorAll("[data-view]"))
      element.setAttribute(
        "aria-pressed",
        String(element.dataset.view === next),
      );
    for (const name of ["scenario", "providers", "runs"])
      $(name + "-view").hidden = name !== next;
    if (next === "runs" && token && selected) monitor();
  }
  for (const element of document.querySelectorAll("[data-view]"))
    element.addEventListener("click", () => showView(element.dataset.view));
  function fillScenario(s) {
    const fields = {
      "scenario-id": "id",
      language: "language",
      persona: "persona",
      goal: "goal",
      greeting: "greeting",
      "answer-delay": "answer_delay_ms",
      duration: "max_duration_ms",
      idle: "idle_timeout_ms",
      turns: "max_remote_turns",
    };
    for (const [id, key] of Object.entries(fields)) $(id).value = s[key] ?? "";
    for (const key of ["facts", "actions", "expectations", "behavior"])
      $(key).value = JSON.stringify(
        s[key] ?? (key === "facts" ? {} : []),
        null,
        2,
      );
    $("outcome").value = s.expected_outcome || "conversation";
    $("hangup-after-goal").checked = s.hangup_after_goal !== false;
    $("retain").checked = !!s.retain_transcript;
  }
  // Keep JSON editor fragments raw until the strict backend decoder has checked them.
  // JSON.parse before validation would silently lose duplicate nested keys.
  function scenarioRaw() {
    if (!$("scenario-form").reportValidity())
      throw new Error("Complete the required scenario fields.");
    const scenario = {
      schema_version: 1,
      id: $("scenario-id").value,
      language: $("language").value,
      persona: $("persona").value,
      greeting: $("greeting").value,
      answer_delay_ms: Number($("answer-delay").value),
      max_duration_ms: Number($("duration").value),
      idle_timeout_ms: Number($("idle").value),
      max_remote_turns: Number($("turns").value),
      hangup_after_goal: $("hangup-after-goal").checked,
      retain_transcript: $("retain").checked,
      retain_audio: false,
      expected_outcome: $("outcome").value,
    };
    if ($("goal").value.trim()) scenario.goal = $("goal").value;
    let raw = JSON.stringify(scenario).slice(0, -1);
    for (const key of ["facts", "actions", "expectations", "behavior"])
      raw += "," + JSON.stringify(key) + ":" + $(key).value;
    return raw + "}";
  }
  const validate = (raw) => api("validate", { method: "POST", body: raw });
  $("preset").addEventListener("change", () => {
    const preset =
      $("preset").value === "" ? null : presets[Number($("preset").value)];
    if (preset) {
      fillScenario(preset);
      $("retain").checked = false;
      message("Preset loaded. Edit the scenario before preparing.");
    }
  });
  $("connect-form").addEventListener("submit", (event) => {
    event.preventDefault();
    if (busy || token) return;
    token = $("token").value;
    $("token").value = "";
    $("connect").disabled = true;
    $("token").disabled = true;
    task(async () => {
      try {
        const cfg = await api("config");
        presets = (await api("scenarios")).scenarios;
        $("preset").replaceChildren(new Option("Select a preset…", ""));
        presets.forEach((preset, index) =>
          $("preset").add(new Option(preset.id, String(index))),
        );
        if (presets[0]) {
          $("preset").value = "0";
          fillScenario(presets[0]);
          $("retain").checked = false;
        }
        activeSettings(cfg.active);
        fillDraft(cfg.active);
        $("disconnect").disabled = false;
        $("connection-dot").classList.add("online");
        $("connection-status").textContent = "HTTP connected";
        message("Connected to this isolated simulator.");
        // Readiness is an independent probe; HTTP success is not gateway readiness.
        try {
          const readiness = await api("readiness");
          $("gateway-status").textContent = readiness.gateway_ready
            ? "Gateway ready · HTTP control connected"
            : "Gateway unavailable · HTTP control connected";
        } catch (err) {
          if (!token) throw err;
          $("gateway-status").textContent =
            "Gateway readiness probe failed · HTTP control connected";
        }
        await listRuns();
      } catch (err) {
        if (token) disconnect("Connection failed: " + err.message);
      }
    });
  });
  $("disconnect").addEventListener("click", () => disconnect());
  $("import-file").addEventListener("change", () =>
    task(async () => {
      const file = $("import-file").files[0];
      if (!file) return;
      if (file.size > 65536) throw new Error("Scenario file exceeds 64 KiB.");
      const raw = await file.text();
      const result = await validate(raw);
      fillScenario(result.scenario);
      $("import-json").value = raw;
      message("Scenario imported and strictly validated.");
    }),
  );
  $("import").addEventListener("click", () =>
    task(async () => {
      const result = await validate($("import-json").value);
      fillScenario(result.scenario);
      message("Scenario imported and strictly validated.");
    }),
  );
  $("validate").addEventListener("click", () =>
    task(async () => {
      const result = await validate(scenarioRaw());
      message(
        "Scenario valid. Scenario hash: " +
          result.scenario_hash +
          "\nPrompt hash: " +
          result.prompt_hash,
      );
    }),
  );
  function download(name, value) {
    const url = URL.createObjectURL(
      new Blob([JSON.stringify(value, null, 2) + "\n"], {
        type: "application/json",
      }),
    );
    const a = document.createElement("a");
    a.href = url;
    a.download = name;
    a.click();
    setTimeout(() => URL.revokeObjectURL(url), 1000);
  }
  $("export-scenario").addEventListener("click", () =>
    task(async () => {
      const result = await validate(scenarioRaw());
      download(result.scenario.id + ".json", result.scenario);
      message("Validated scenario exported.");
    }),
  );
  $("scenario-form").addEventListener("submit", (event) => {
    event.preventDefault();
    task(async () => {
      const raw = scenarioRaw();
      await validate(raw);
      const run = await api("runs", {
        method: "POST",
        body: raw,
        headers: { "Idempotency-Key": crypto.randomUUID() },
      });
      selectRun(run);
      showView("runs");
      const preparedMessage =
        "Run prepared. Call the exact SIP target below from your separate caller.";
      message(preparedMessage);
      try {
        await listRuns();
      } catch (err) {
        // Reservation success must survive a subsequent list failure. Authentication
        // failures still disconnect and clear sensitive state through api().
        if (token && selected === run.run_id)
          message(
            preparedMessage +
              " Recent runs could not be refreshed: " +
              err.message,
            true,
          );
      }
    });
  });
  function definitionList(id, pairs) {
    const nodes = [];
    for (const [label, value] of pairs) {
      const dt = document.createElement("dt"),
        dd = document.createElement("dd");
      dt.textContent = label;
      dd.textContent = String(value ?? "—");
      nodes.push(dt, dd);
    }
    $(id).replaceChildren(...nodes);
  }
  function activeSettings(cfg) {
    definitionList("active-settings", [
      ["ASR", cfg.asr],
      ["LLM", cfg.llm],
      ["TTS", cfg.tts],
      ["Backend model", cfg.backend_model],
      ["Voice", cfg.voice],
      ["Language", cfg.language],
      ["Speed", cfg.speed],
      ["Sample rate", cfg.client_rate + " Hz"],
      ["Barge-in", cfg.allow_barge_in ? "Enabled" : "Disabled"],
      ["On new query", cfg.on_new_query],
    ]);
  }
  function fillDraft(cfg) {
    const fields = {
      asr: "asr",
      llm: "llm",
      tts: "tts",
      model: "backend_model",
      voice: "voice",
      language: "language",
      speed: "speed",
      query: "on_new_query",
    };
    for (const [id, key] of Object.entries(fields))
      $("draft-" + id).value = cfg[key];
    $("draft-barge").checked = cfg.allow_barge_in;
  }
  $("profile-form").addEventListener("submit", (event) => {
    event.preventDefault();
    if (!$("profile-form").reportValidity()) return;
    const profile = {
      asr: $("draft-asr").value,
      llm: $("draft-llm").value,
      tts: $("draft-tts").value,
      model: "golive-1",
      backend_model: $("draft-model").value,
      voice: $("draft-voice").value,
      language: $("draft-language").value,
      speed: Number($("draft-speed").value),
      client_rate: 16000,
      greeting: "",
      allow_provider_override: false,
      web_root: "",
      log_level: "warn",
      enable_pprof: false,
      realtime: { store_calls: false },
      duplex: {
        backchannel: false,
        speculative: false,
        holding_filler: false,
        allow_barge_in: $("draft-barge").checked,
        on_new_query: $("draft-query").value,
        playback_paced: true,
        session_max_seconds: 330,
        idle_timeout_seconds: 60,
        max_audio_kbps: 512,
        max_events_per_second: 30,
      },
      llm_hedge: 1,
      tts_hedge: 1,
    };
    download("simulator-profile-draft.json", profile);
    message(
      "Profile draft exported. Restart of this isolated simulator required; active settings are unchanged.",
    );
  });
  async function listRuns() {
    const data = await api("runs");
    $("run-select").replaceChildren(new Option("Select a run…", ""));
    for (const run of data.runs || [])
      $("run-select").add(
        new Option(run.run_id + " · " + run.phase, run.run_id),
      );
    if (
      selected &&
      !Array.from($("run-select").options).some((o) => o.value === selected)
    )
      $("run-select").add(new Option(selected, selected));
    $("run-select").value = selected;
  }
  $("refresh-runs").addEventListener("click", () =>
    task(async () => {
      await listRuns();
      message("Recent runs refreshed.");
    }),
  );
  function selectRun(run) {
    stopPolling();
    selected = run.run_id;
    phase = run.phase;
    cursor = 0;
    report = null;
    $("events").replaceChildren();
    $("event-warning").textContent = "";
    $("report").replaceChildren();
    $("transcript-consent").checked = false;
    if (!Array.from($("run-select").options).some((o) => o.value === selected))
      $("run-select").add(new Option(selected, selected));
    $("run-select").value = selected;
    renderRun(run);
    buttons();
  }
  function renderRun(run) {
    phase = run.phase;
    $("phase").textContent = phase.toUpperCase();
    definitionList("run-info", [
      ["Run ID", run.run_id],
      [
        "Exact SIP target",
        String(run.dial_target_reference ?? "—").replaceAll(
          "<run_id>",
          run.run_id,
        ),
      ],
      ["Reservation expires", run.reservation_expires_at],
      ["Call ID", run.binding?.call_id],
      ["Reason", run.reason],
    ]);
    buttons();
  }
  function buttons() {
    const done = !!selected && terminal(phase);
    $("stop").disabled = !selected || !phase || done || busy;
    $("load-report").disabled = !done || busy;
    $("resume").disabled = !selected || busy;
    $("download-report").disabled = !report || busy;
    $("transcript-consent").disabled =
      !report?.transcript_retained || !done || busy;
    $("download-transcript").disabled =
      !report?.transcript_retained ||
      !done ||
      !$("transcript-consent").checked ||
      busy;
  }
  $("run-select").addEventListener("change", () => {
    const id = $("run-select").value;
    stopPolling();
    selected = id;
    report = null;
    cursor = 0;
    phase = "";
    $("events").replaceChildren();
    $("report").replaceChildren();
    $("run-info").replaceChildren();
    $("event-warning").textContent = "";
    $("transcript-consent").checked = false;
    $("phase").textContent = id ? "LOADING SELECTED RUN" : "NO RUN SELECTED";
    $("monitor-status").textContent = id
      ? "Loading selected run…"
      : "No monitoring active.";
    buttons();
    if (id) monitor();
  });
  async function monitor() {
    stopPolling();
    if (!token || !selected || view !== "runs") return;
    const epoch = pollEpoch,
      id = selected;
    let count = 0;
    async function tick() {
      if (epoch !== pollEpoch || !token || selected !== id || view !== "runs")
        return;
      try {
        const run = await api("runs/" + encodeURIComponent(id), {}, true);
        const page = await api(
          "runs/" + encodeURIComponent(id) + "/events?after=" + cursor,
          {},
          true,
        );
        if (epoch !== pollEpoch || selected !== id) return;
        renderRun(run);
        if (page.truncated)
          $("event-warning").textContent =
            "Earlier events were truncated by the server. This timeline is incomplete.";
        for (const event of page.events || []) {
          const li = document.createElement("li");
          li.textContent =
            "#" +
            event.sequence +
            " · " +
            event.at_ms +
            " ms · " +
            event.kind +
            (event.reason ? " · " + event.reason : "");
          $("events").append(li);
          if ($("events").children.length > 300) {
            $("events").firstChild.remove();
            $("event-warning").textContent =
              "Showing the latest 300 events. Earlier events are omitted.";
          }
        }
        cursor = Math.max(cursor, Number(page.next) || 0);
        if (terminal(run.phase)) {
          $("monitor-status").textContent =
            "Terminal state reached. Monitoring stopped; report is available.";
          stopPolling();
          return;
        }
        if (++count >= 200) {
          $("monitor-status").textContent =
            "Five-minute monitoring limit reached. Refresh & monitor to continue.";
          stopPolling();
          return;
        }
        $("monitor-status").textContent =
          "Monitoring selected run · every 1.5 seconds";
        pollTimer = setTimeout(tick, 1500);
      } catch (err) {
        if (epoch === pollEpoch && err.name !== "AbortError") {
          $("monitor-status").textContent =
            "Monitoring stopped after an error.";
          message(err.message, true);
          stopPolling();
        }
      }
    }
    await tick();
  }
  $("resume").addEventListener("click", () => monitor());
  $("stop").addEventListener("click", () =>
    task(async () => {
      const id = selected;
      if (!id || terminal(phase)) return;
      stopPolling();
      const run = await api("runs/" + encodeURIComponent(id) + "/stop", {
        method: "POST",
      });
      if (selected !== id) return;
      renderRun(run);
      message("Stop requested for run " + id + ".");
      if (!terminal(run.phase)) monitor();
    }),
  );
  function renderReport(rep) {
    const root = $("report");
    root.replaceChildren();
    function section(title, rows) {
      const container = document.createElement("section"),
        heading = document.createElement("h4");
      container.className = "result-section";
      heading.textContent = title;
      container.append(heading);
      if (!rows.length) {
        const p = document.createElement("p");
        p.className = "hint";
        p.textContent = "No entries.";
        container.append(p);
      }
      for (const row of rows) {
        const div = document.createElement("div"),
          badge = document.createElement("span"),
          text = document.createElement("span"),
          detail = document.createElement("small");
        div.className = "result-item";
        badge.className =
          "badge " +
          (["fail", "indeterminate"].includes(row.status) ? row.status : "");
        badge.textContent = row.status;
        text.textContent = row.label;
        detail.textContent = row.detail || "";
        div.append(badge, text, detail);
        container.append(div);
      }
      root.append(container);
    }
    section(
      "Lifecycle & semantic checks",
      (rep.checks || []).map((c) => ({
        status: c.result,
        label: c.name,
        detail: c.evidence,
      })),
    );
    section(
      "Scheduled actions",
      (rep.action_outcomes || []).map((a) => ({
        status: a.result,
        label: a.id,
        detail: (a.at_ms ?? 0) + " ms · " + (a.reason || ""),
      })),
    );
    for (const [key, title] of [
      ["assistant_response_gaps", "Assistant response gaps"],
      ["simulator_response_gaps", "Customer response gaps"],
    ]) {
      section(
        title,
        (rep[key] || []).map((g) => ({
          status: g.valid ? "valid" : "invalid",
          label: g.valid ? g.value_ms + " ms" : "Excluded sample",
          detail: g.reason || g.evidence,
        })),
      );
    }
    section(
      "Failures",
      (rep.failures || []).map((f) => ({ status: "fail", label: f })),
    );
    section("Evidence", [
      {
        status: rep.evidence || "unknown",
        label: rep.termination_reason || rep.phase,
        detail:
          "Invalid observations: " +
          rep.invalid_observations +
          " · Events truncated: " +
          !!rep.events_truncated +
          " · Transcript truncated: " +
          !!rep.transcript_truncated,
      },
    ]);
  }
  $("load-report").addEventListener("click", () =>
    task(async () => {
      const id = selected;
      const rep = await api("runs/" + encodeURIComponent(id) + "/report");
      if (selected !== id) return;
      report = rep;
      renderReport(rep);
      message("Report loaded for " + id + ".");
    }),
  );
  $("download-report").addEventListener("click", () => {
    if (report) download(selected + "-report.json", report);
  });
  $("transcript-consent").addEventListener("change", buttons);
  $("download-transcript").addEventListener("click", () =>
    task(async () => {
      if (
        !report?.transcript_retained ||
        !$("transcript-consent").checked ||
        !terminal(phase)
      )
        return;
      const id = selected;
      const transcript = await api(
        "runs/" + encodeURIComponent(id) + "/transcript",
      );
      if (id === selected) {
        download(id + "-transcript.json", transcript);
        message("Transcript envelope exported for independent evaluation.");
      }
    }),
  );
  buttons();
})();
