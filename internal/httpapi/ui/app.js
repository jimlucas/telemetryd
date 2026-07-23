(() => {
  "use strict";

  const REFRESH_MS = 5000;
  const numberFormat = new Intl.NumberFormat(undefined, { maximumFractionDigits: 1 });
  const integerFormat = new Intl.NumberFormat(undefined, { maximumFractionDigits: 0 });
  const clockFormat = new Intl.DateTimeFormat(undefined, {
    month: "short", day: "numeric", hour: "2-digit", minute: "2-digit", second: "2-digit"
  });

  const statusMeta = {
    online_observed: { label: "Online observed", className: "online" },
    offline_reported: { label: "Offline reported", className: "offline" },
    stale_unconfirmed: { label: "Stale unconfirmed", className: "stale" },
    unknown_upstream: { label: "Unknown upstream", className: "unknown_upstream" },
    conflict: { label: "Parent conflict", className: "conflict" },
    disconnected: { label: "Disconnected", className: "disconnected" },
    unknown: { label: "Unknown", className: "unknown" }
  };
  const qualityMeta = {
    source: { label: "Source time", className: "source" },
    missing: { label: "Missing", className: "missing" },
    invalid_past: { label: "Invalid past", className: "invalid_past" },
    future: { label: "Future", className: "future" },
    regressed: { label: "Regressed", className: "regressed" }
  };
  const statusOrder = [
    "online_observed", "offline_reported", "stale_unconfirmed",
    "unknown_upstream", "conflict", "disconnected", "unknown"
  ];
  const attentionRank = {
    conflict: 0, offline_reported: 1, unknown_upstream: 2, stale_unconfirmed: 3,
    disconnected: 4, unknown: 5, online_observed: 6
  };

  const app = {
    token: readSessionToken(),
    previousSummary: null,
    previousSummaryAt: 0,
    records: [],
    refreshRunning: false,
    recentRunning: false,
    timer: null,
    searchTimer: null,
    authenticatedFailureShown: false
  };

  const dom = {
    connectionPill: byId("connection-pill"),
    connectionLabel: byId("connection-label"),
    lastRefresh: byId("last-refresh"),
    refreshButton: byId("refresh-button"),
    tokenButton: byId("token-button"),
    notice: byId("notice"),
    noticeTitle: byId("notice-title"),
    noticeCopy: byId("notice-copy"),
    noticeAction: byId("notice-action"),
    kpiStreams: byId("kpi-streams"),
    kpiStreamsNote: byId("kpi-streams-note"),
    kpiRate: byId("kpi-rate"),
    kpiRateNote: byId("kpi-rate-note"),
    kpiDevices: byId("kpi-devices"),
    kpiDevicesNote: byId("kpi-devices-note"),
    kpiTimeDefects: byId("kpi-time-defects"),
    kpiTimeNote: byId("kpi-time-note"),
    kpiDuplicates: byId("kpi-duplicates"),
    kpiDuplicatesNote: byId("kpi-duplicates-note"),
    rnTotalLabel: byId("rn-total-label"),
    orbitOnlinePercent: byId("orbit-online-percent"),
    orbitProgress: byId("orbit-progress"),
    rnStatusLegend: byId("rn-status-legend"),
    qualityList: byId("quality-list"),
    captureReturned: byId("capture-returned"),
    captureCountLabel: byId("capture-count-label"),
    captureRows: byId("capture-rows"),
    captureScanNote: byId("capture-scan-note"),
    filterForm: byId("capture-filter"),
    filterSearch: byId("filter-search"),
    filterKind: byId("filter-kind"),
    filterQuality: byId("filter-quality"),
    filterSince: byId("filter-since"),
    filterLimit: byId("filter-limit"),
    rnRows: byId("rn-rows"),
    rnListNote: byId("rn-list-note"),
    streamList: byId("stream-list"),
    recordDialog: byId("record-dialog"),
    recordTitle: byId("record-title"),
    recordSummary: byId("record-summary"),
    recordDetails: byId("record-details"),
    recordJSON: byId("record-json"),
    tokenDialog: byId("token-dialog"),
    tokenForm: byId("token-form"),
    tokenInput: byId("token-input"),
    tokenStatus: byId("token-status"),
    tokenClear: byId("token-clear")
  };

  class APIError extends Error {
    constructor(message, status) {
      super(message);
      this.name = "APIError";
      this.status = status;
    }
  }

  function byId(id) {
    return document.getElementById(id);
  }

  function readSessionToken() {
    try {
      return sessionStorage.getItem("telemetryd-token") || "";
    } catch (_) {
      return "";
    }
  }

  function saveSessionToken(value) {
    app.token = value.trim();
    try {
      if (app.token) sessionStorage.setItem("telemetryd-token", app.token);
      else sessionStorage.removeItem("telemetryd-token");
    } catch (_) {
      // A locked-down browser may disallow storage. The in-memory token still works.
    }
  }

  async function apiResponse(path, accept = "application/json") {
    const headers = { Accept: accept };
    if (app.token) headers.Authorization = `Bearer ${app.token}`;
    let response;
    try {
      response = await fetch(path, { headers, cache: "no-store" });
    } catch (error) {
      throw new APIError(`Collector API is unreachable: ${error.message}`, 0);
    }
    if (!response.ok) {
      let message = `${response.status} ${response.statusText}`;
      try {
        const payload = await response.clone().json();
        if (payload && payload.error) message = payload.error;
      } catch (_) {
        // Preserve the HTTP status when the body is not JSON.
      }
      throw new APIError(message, response.status);
    }
    return response;
  }

  async function apiJSON(path) {
    const response = await apiResponse(path);
    return response.json();
  }

  async function apiDocument(path) {
    const response = await apiResponse(path, "application/json, text/plain;q=0.9, */*;q=0.1");
    const type = response.headers.get("content-type") || "";
    if (type.includes("json")) return JSON.stringify(await response.json(), null, 2);
    return response.text();
  }

  function recentURL() {
    const params = new URLSearchParams();
    params.set("limit", dom.filterLimit.value || "250");
    if (dom.filterSearch.value.trim()) params.set("q", dom.filterSearch.value.trim());
    if (dom.filterKind.value) params.set("kind", dom.filterKind.value);
    if (dom.filterQuality.value) params.set("quality", dom.filterQuality.value);
    if (dom.filterSince.value) params.set("since", dom.filterSince.value);
    return `/v1/recent?${params.toString()}`;
  }

  async function refreshAll({ manual = false } = {}) {
    if (app.refreshRunning) return;
    app.refreshRunning = true;
    setRefreshAnimation(true);
    try {
      const [summary, recent, rns, streams] = await Promise.all([
        apiJSON("/v1/summary"),
        apiJSON(recentURL()),
        apiJSON("/v1/attention/rns?limit=40"),
        apiJSON("/v1/streams")
      ]);
      renderSummary(summary);
      renderRecent(recent);
      renderRNs(rns);
      renderStreams(streams);
      markSuccessfulRefresh(summary);
      hideNotice();
      app.authenticatedFailureShown = false;
    } catch (error) {
      handleRefreshError(error);
    } finally {
      app.refreshRunning = false;
      setRefreshAnimation(false);
      if (manual) restartTimer();
    }
  }

  async function refreshRecent() {
    if (app.recentRunning || app.refreshRunning) return;
    app.recentRunning = true;
    try {
      renderCaptureLoading();
      const recent = await apiJSON(recentURL());
      renderRecent(recent);
    } catch (error) {
      handleRefreshError(error);
    } finally {
      app.recentRunning = false;
    }
  }

  function renderSummary(summary) {
    const stats = summary.stats || {};
    const activeStreams = numeric(summary.active_streams);
    const bnCount = numeric(summary.bn_count);
    const rnCount = numeric(summary.rn_count);

    dom.kpiStreams.textContent = integer(activeStreams);
    dom.kpiStreamsNote.textContent = activeStreams === 1 ? "One BN dial-out session active" : `${integer(activeStreams)} BN dial-out sessions active`;

    const nowMs = Date.parse(summary.generated_at) || Date.now();
    let rate = 0;
    let rateContext = "Average update observations";
    if (app.previousSummary && app.previousSummaryAt > 0 && nowMs > app.previousSummaryAt) {
      const delta = Math.max(0, numeric(stats.updates) - numeric(app.previousSummary.stats?.updates));
      rate = delta / ((nowMs - app.previousSummaryAt) / 1000);
      rateContext = `${integer(delta)} updates since last refresh`;
    } else if (numeric(summary.uptime_seconds) > 0) {
      rate = numeric(stats.updates) / numeric(summary.uptime_seconds);
    }
    dom.kpiRate.textContent = formatRate(rate);
    dom.kpiRateNote.textContent = rateContext;

    dom.kpiDevices.textContent = integer(bnCount + rnCount);
    dom.kpiDevicesNote.textContent = `${integer(bnCount)} BNs · ${integer(rnCount)} RNs`;

    const missing = numeric(stats.missing_source_timestamp);
    const invalid = numeric(stats.invalid_source_timestamp);
    const regressions = numeric(stats.source_regressions);
    dom.kpiTimeDefects.textContent = integer(missing + invalid + regressions);
    dom.kpiTimeNote.textContent = `${integer(missing)} missing · ${integer(regressions)} regressed`;

    const exact = numeric(stats.exact_duplicates);
    const reported = numeric(stats.reported_duplicates);
    const repeated = numeric(stats.repeated_values);
    dom.kpiDuplicates.textContent = integer(exact + reported);
    dom.kpiDuplicatesNote.textContent = `${integer(repeated)} unchanged repeats retained as evidence`;

    renderStatusSummary(summary.rn_statuses || {}, rnCount);
    renderQuality(stats, summary.rn_statuses || {});
    app.previousSummary = summary;
    app.previousSummaryAt = nowMs;
  }

  function renderStatusSummary(counts, total) {
    const online = numeric(counts.online_observed);
    const percent = total > 0 ? Math.round((online / total) * 1000) / 10 : 0;
    dom.rnTotalLabel.textContent = `${integer(total)} RNs`;
    dom.orbitOnlinePercent.textContent = total > 0 ? `${numberFormat.format(percent)}%` : "—";
    dom.orbitProgress.setAttribute("stroke-dasharray", `${Math.max(0, Math.min(100, percent))} 100`);

    dom.rnStatusLegend.replaceChildren();
    for (const key of statusOrder) {
      const count = numeric(counts[key]);
      const meta = statusMeta[key];
      const row = document.createElement("div");
      row.className = `legend-row legend-${meta.className}`;

      const dot = document.createElement("span");
      dot.className = "legend-dot";
      const label = document.createElement("span");
      label.className = "legend-label";
      label.textContent = meta.label;
      const progress = document.createElement("progress");
      progress.className = "legend-progress";
      progress.max = Math.max(total, 1);
      progress.value = count;
      progress.setAttribute("aria-label", `${meta.label}: ${count}`);
      const countNode = document.createElement("span");
      countNode.className = "legend-count";
      countNode.textContent = integer(count);
      row.append(dot, label, progress, countNode);
      dom.rnStatusLegend.append(row);
    }
  }

  function renderQuality(stats, rnStatuses) {
    const rows = [
      {
        className: "quality-amber", icon: "T?", title: "Missing source timestamps",
        note: "Receive time is preserved separately; no timestamp is invented.",
        value: numeric(stats.missing_source_timestamp)
      },
      {
        className: "quality-rose", icon: "T↓", title: "Invalid or regressed source time",
        note: "Evidence remains visible even though receipt order owns current state.",
        value: numeric(stats.invalid_source_timestamp) + numeric(stats.source_regressions)
      },
      {
        className: "quality-violet", icon: "×2", title: "Duplicate and repeat evidence",
        note: "Exact duplicates, sender coalescing, and unchanged values are counted.",
        value: numeric(stats.exact_duplicates) + numeric(stats.reported_duplicates) + numeric(stats.repeated_values)
      },
      {
        className: "quality-cyan", icon: "!", title: "Protocol and decode errors",
        note: "Malformed submissions are diagnosed without erasing known state.",
        value: numeric(stats.decode_errors) + numeric(stats.protocol_errors)
      }
    ];
    dom.qualityList.replaceChildren();
    for (const item of rows) {
      const row = document.createElement("div");
      row.className = `quality-row ${item.className}`;
      const icon = document.createElement("span");
      icon.className = "quality-icon";
      icon.textContent = item.icon;
      const copy = document.createElement("div");
      copy.className = "quality-copy";
      const title = document.createElement("strong");
      title.textContent = item.title;
      const note = document.createElement("small");
      note.textContent = item.note;
      copy.append(title, note);
      const value = document.createElement("span");
      value.className = "quality-value";
      value.textContent = integer(item.value);
      row.append(icon, copy, value);
      dom.qualityList.append(row);
    }
    const conflicts = numeric(rnStatuses.conflict);
    if (conflicts > 0) {
      dom.qualityList.title = `${integer(conflicts)} RNs currently have contradictory healthy parent claims.`;
    } else {
      dom.qualityList.removeAttribute("title");
    }
  }

  function renderRecent(payload) {
    app.records = Array.isArray(payload.items) ? payload.items : [];
    dom.captureReturned.textContent = integer(payload.returned || app.records.length);
    dom.captureCountLabel.textContent = payload.truncated ? "newest records shown" : "records shown";
    dom.captureScanNote.textContent = `${integer(payload.scanned)} current paths scanned · ${integer(payload.matched)} matched${payload.truncated ? " · result truncated" : ""}`;

    if (app.records.length === 0) {
      dom.captureRows.innerHTML = '<tr class="empty-row"><td colspan="7">No retained records match these filters.</td></tr>';
      return;
    }

    dom.captureRows.innerHTML = app.records.map((record, index) => captureRow(record, index)).join("");
  }

  function captureRow(record, index) {
    const received = parseDate(record.received_at);
    const source = record.source_timestamp ? parseDate(record.source_timestamp) : null;
    const status = statusMeta[record.status] || statusMeta.unknown;
    const quality = qualityMeta[record.timestamp_quality] || qualityMeta.missing;
    const kind = record.kind === "rn" ? "rn" : "bn";
    const parent = kind === "rn" ? (record.parent_bn_id || record.source_bn_id || "No parent") : (record.source_bn_id || "Base node");
    const hostname = record.hostname ? record.hostname : parent;
    const value = record.value_text === "" ? "∅" : record.value_text;
    const sourcePrimary = source ? shortTime(source) : quality.label;
    const sourceSecondary = source ? `${formatAgeSeconds(record.source_age_seconds)} old` : "collector receipt only";

    return `<tr data-record-index="${index}" tabindex="0" aria-label="Open ${escapeHTML(kind.toUpperCase())} ${escapeHTML(record.device_id)} ${escapeHTML(record.path)}">
      <td title="${escapeHTML(exactTime(received))}"><span class="cell-primary mono">${escapeHTML(formatAgeSeconds(record.age_seconds))} ago</span><span class="cell-secondary">${escapeHTML(shortTime(received))}</span></td>
      <td title="${escapeHTML(record.device_id)}"><span class="cell-primary"><span class="kind-tag ${kind}">${kind.toUpperCase()}</span>${escapeHTML(record.device_id)}</span><span class="cell-secondary">${escapeHTML(hostname)}</span></td>
      <td><span class="status-badge status-${status.className}" title="${escapeHTML(record.status_reason || status.label)}">${escapeHTML(status.label)}</span></td>
      <td class="path-cell" title="${escapeHTML(record.path)}"><span class="cell-primary">${escapeHTML(record.path)}</span><span class="cell-secondary">${escapeHTML(parent)}</span></td>
      <td title="${escapeHTML(value)}"><span class="value-chip">${escapeHTML(value)}</span><span class="cell-secondary">${escapeHTML(record.value_type || "unknown")}</span></td>
      <td title="${escapeHTML(source ? exactTime(source) : "No source timestamp was supplied")}"><span class="quality-badge quality-${quality.className}">${escapeHTML(sourcePrimary)}</span><span class="cell-secondary">${escapeHTML(sourceSecondary)}</span></td>
      <td><div class="evidence">${evidenceTags(record)}</div></td>
    </tr>`;
  }

  function evidenceTags(record) {
    const tags = [];
    const add = (label, value, className = "") => {
      if (numeric(value) > 0) tags.push(`<span class="evidence-tag ${className}" title="${escapeHTML(label)}">${escapeHTML(shortEvidence(label))} ${escapeHTML(integer(value))}</span>`);
    };
    if (numeric(record.samples) > 1) add("samples", record.samples);
    add("value changes", record.value_changes);
    add("exact duplicates", record.exact_duplicates, "warn");
    add("unchanged repeats", record.repeated_values);
    add("source regressions", record.source_regressions, "bad");
    add("sender-reported duplicates", record.reported_duplicates, "warn");
    return tags.length ? tags.join("") : '<span class="evidence-tag">first</span>';
  }

  function shortEvidence(label) {
    return ({
      "samples": "S", "value changes": "Δ", "exact duplicates": "D",
      "unchanged repeats": "R", "source regressions": "T↓", "sender-reported duplicates": "SD"
    })[label] || label;
  }

  function renderCaptureLoading() {
    dom.captureRows.innerHTML = '<tr class="empty-row"><td colspan="7"><span class="spinner"></span>Refreshing current records…</td></tr>';
  }

  function renderRNs(payload) {
    const items = Array.isArray(payload.items) ? payload.items.slice() : [];
    items.sort((left, right) => {
      const rank = (attentionRank[left.status] ?? 99) - (attentionRank[right.status] ?? 99);
      if (rank !== 0) return rank;
      const age = numeric(right.age_seconds) - numeric(left.age_seconds);
      if (age !== 0) return age;
      return String(left.id).localeCompare(String(right.id));
    });
    const display = items.slice(0, 40);
    if (display.length === 0) {
      dom.rnRows.innerHTML = '<tr class="empty-row"><td colspan="4">No RNs have been observed yet.</td></tr>';
    } else {
      dom.rnRows.innerHTML = display.map((rn) => {
        const status = statusMeta[rn.status] || statusMeta.unknown;
        return `<tr title="${escapeHTML(rn.status_reason || status.label)}">
          <td><span class="cell-primary">${escapeHTML(rn.id)}</span><span class="cell-secondary">${escapeHTML(rn.hostname || "No hostname")}</span></td>
          <td class="mono">${escapeHTML(rn.parent_bn_id || "—")}</td>
          <td><span class="status-badge status-${status.className}">${escapeHTML(status.label)}</span></td>
          <td class="mono">${escapeHTML(formatAgeSeconds(rn.age_seconds))}</td>
        </tr>`;
      }).join("");
    }
    const total = numeric(payload.total ?? payload.count ?? items.length);
    const returned = numeric(payload.returned ?? payload.count ?? items.length);
    const problems = numeric(payload.problem_count);
    const notes = [`${integer(total)} known RNs`];
    if (problems > 0) notes.push(`${integer(problems)} need attention`);
    if (returned < total) notes.push(`showing ${integer(returned)} highest-attention rows`);
    if (display.length < returned) notes.push(`rendered ${integer(display.length)}`);
    dom.rnListNote.textContent = notes.join(" · ");
  }

  function renderStreams(payload) {
    const items = Array.isArray(payload.items) ? payload.items : [];
    if (items.length === 0) {
      dom.streamList.innerHTML = '<div class="stream-empty">No dial-out stream has connected during this process lifetime.</div>';
      return;
    }
    dom.streamList.innerHTML = items.slice(0, 40).map((stream) => {
      const peer = stream.meta?.peer_host || stream.meta?.peer || "Unknown peer";
      const subscription = stream.meta?.metadata?.["subscription-name"] || stream.meta?.metadata?.subscription_name || "unnamed subscription";
      const identity = stream.bn_id || peer;
      const last = stream.last_message_at ? `${formatAge(stream.last_message_at)} ago` : "no messages";
      const errors = numeric(stream.decode_errors) + numeric(stream.protocol_errors);
      const detail = stream.active ? `${last} · ${subscription}` : `${stream.close_reason || "stream closed"} · ${last}`;
      return `<div class="stream-item ${stream.active ? "active" : "closed"}" title="${escapeHTML(stream.id)}">
        <span class="stream-state" aria-hidden="true"></span>
        <div class="stream-copy"><strong>${escapeHTML(identity)}</strong><small>${escapeHTML(peer)} · ${escapeHTML(detail)}</small></div>
        <div class="stream-stats"><strong>${escapeHTML(integer(stream.message_sequence))}</strong><small>${errors ? `${integer(errors)} errors` : "messages"}</small></div>
      </div>`;
    }).join("");
  }

  function openRecord(index) {
    const record = app.records[index];
    if (!record) return;
    const status = statusMeta[record.status] || statusMeta.unknown;
    const quality = qualityMeta[record.timestamp_quality] || qualityMeta.missing;
    dom.recordTitle.textContent = `${String(record.kind || "").toUpperCase()} ${record.device_id}`;
    dom.recordSummary.innerHTML = [
      `<span class="status-badge status-${status.className}">${escapeHTML(status.label)}</span>`,
      `<span class="quality-badge quality-${quality.className}">${escapeHTML(quality.label)}</span>`,
      `<span class="value-chip">${escapeHTML(record.value_text === "" ? "∅" : record.value_text)}</span>`
    ].join("");

    const details = [
      ["Canonical path", record.path],
      ["Base path", record.base_path],
      ["Path keys", objectText(record.keys)],
      ["Value type", record.value_type],
      ["Collector received", exactTime(parseDate(record.received_at))],
      ["Source timestamp", record.source_timestamp ? exactTime(parseDate(record.source_timestamp)) : "Missing — no device time was invented"],
      ["Timestamp quality", record.timestamp_quality],
      ["First retained", exactTime(parseDate(record.first_seen_at))],
      ["Value last changed", exactTime(parseDate(record.changed_at))],
      ["Device status", `${record.status}: ${record.status_reason || ""}`],
      ["Parent / source BN", record.parent_bn_id || record.source_bn_id || "—"],
      ["Stream ID", record.stream_id],
      ["Subscription scope", record.scope_id],
      ["Message sequence", record.message_sequence],
      ["Observation order", record.observation_order],
      ["Samples / changes", `${integer(record.samples)} / ${integer(record.value_changes)}`],
      ["Exact duplicates", record.exact_duplicates],
      ["Unchanged repeats", record.repeated_values],
      ["Source regressions", record.source_regressions],
      ["Sender duplicates", record.reported_duplicates]
    ];
    dom.recordDetails.replaceChildren();
    for (const [label, value] of details) {
      const term = document.createElement("dt");
      term.textContent = label;
      const definition = document.createElement("dd");
      definition.textContent = value === null || value === undefined || value === "" ? "—" : String(value);
      dom.recordDetails.append(term, definition);
    }
    dom.recordJSON.textContent = JSON.stringify(record, null, 2);
    showDialog(dom.recordDialog);
  }


  async function openAPIView(path) {
    dom.recordTitle.textContent = `GET ${path}`;
    dom.recordSummary.innerHTML = '<span class="quality-badge quality-source">Live API response</span>';
    dom.recordDetails.replaceChildren();
    for (const [label, value] of [["Endpoint", path], ["Authentication", app.token ? "Bearer token attached" : "No token configured"]]) {
      const term = document.createElement("dt");
      term.textContent = label;
      const definition = document.createElement("dd");
      definition.textContent = value;
      dom.recordDetails.append(term, definition);
    }
    dom.recordJSON.textContent = "Loading…";
    showDialog(dom.recordDialog);
    try {
      dom.recordJSON.textContent = await apiDocument(path);
    } catch (error) {
      dom.recordJSON.textContent = error instanceof Error ? error.message : String(error);
      handleRefreshError(error);
    }
  }

  function markSuccessfulRefresh(summary) {
    const active = numeric(summary.active_streams);
    dom.connectionPill.dataset.state = active > 0 ? "live" : "stale";
    dom.connectionLabel.textContent = active > 0 ? "Capture live" : "No BN streams";
    dom.lastRefresh.textContent = `Updated ${new Date().toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" })}`;
  }

  function handleRefreshError(error) {
    const authFailure = error instanceof APIError && error.status === 401;
    if (authFailure) {
      dom.connectionPill.dataset.state = "auth";
      dom.connectionLabel.textContent = "Token required";
      showNotice("API authentication required", "Enter the collector bearer token. The console stores it only for this browser tab.", () => openTokenDialog("Authentication required"));
      if (!app.authenticatedFailureShown) {
        app.authenticatedFailureShown = true;
        openTokenDialog(app.token ? "The saved token was rejected." : "Authentication required");
      }
      return;
    }
    dom.connectionPill.dataset.state = "error";
    dom.connectionLabel.textContent = "API unavailable";
    const message = error instanceof Error ? error.message : String(error);
    showNotice("Unable to refresh capture state", message, () => refreshAll({ manual: true }));
  }

  function showNotice(title, copy, action) {
    dom.noticeTitle.textContent = title;
    dom.noticeCopy.textContent = copy;
    dom.notice.classList.remove("hidden");
    dom.noticeAction.onclick = action;
  }

  function hideNotice() {
    dom.notice.classList.add("hidden");
  }

  function openTokenDialog(message = "") {
    dom.tokenInput.value = app.token;
    dom.tokenStatus.textContent = message;
    showDialog(dom.tokenDialog);
    window.setTimeout(() => dom.tokenInput.focus(), 30);
  }

  function showDialog(dialog) {
    if (!dialog.open) dialog.showModal();
  }

  function setRefreshAnimation(active) {
    dom.refreshButton.classList.toggle("is-spinning", active);
    dom.refreshButton.disabled = active;
  }

  function restartTimer() {
    window.clearInterval(app.timer);
    app.timer = window.setInterval(() => refreshAll(), REFRESH_MS);
  }

  function bindEvents() {
    dom.refreshButton.addEventListener("click", () => refreshAll({ manual: true }));
    dom.tokenButton.addEventListener("click", () => openTokenDialog());
    dom.filterForm.addEventListener("submit", (event) => {
      event.preventDefault();
      refreshRecent();
    });
    dom.filterSearch.addEventListener("input", () => {
      window.clearTimeout(app.searchTimer);
      app.searchTimer = window.setTimeout(refreshRecent, 320);
    });
    for (const select of [dom.filterKind, dom.filterQuality, dom.filterSince, dom.filterLimit]) {
      select.addEventListener("change", refreshRecent);
    }
    dom.captureRows.addEventListener("click", (event) => {
      const row = event.target.closest("tr[data-record-index]");
      if (row) openRecord(Number(row.dataset.recordIndex));
    });
    dom.captureRows.addEventListener("keydown", (event) => {
      if (event.key !== "Enter" && event.key !== " ") return;
      const row = event.target.closest("tr[data-record-index]");
      if (!row) return;
      event.preventDefault();
      openRecord(Number(row.dataset.recordIndex));
    });
    dom.tokenForm.addEventListener("submit", (event) => {
      event.preventDefault();
      saveSessionToken(dom.tokenInput.value);
      dom.tokenStatus.textContent = "";
      dom.tokenDialog.close();
      app.authenticatedFailureShown = false;
      app.previousSummary = null;
      refreshAll({ manual: true });
    });
    dom.tokenClear.addEventListener("click", () => {
      saveSessionToken("");
      dom.tokenInput.value = "";
      dom.tokenStatus.textContent = "Token cleared for this tab.";
    });
    document.querySelectorAll("[data-api-link]").forEach((link) => {
      link.addEventListener("click", (event) => {
        event.preventDefault();
        openAPIView(link.getAttribute("href"));
      });
    });
    document.querySelectorAll("[data-close-dialog]").forEach((button) => {
      button.addEventListener("click", () => button.closest("dialog")?.close());
    });
    for (const dialog of [dom.recordDialog, dom.tokenDialog]) {
      dialog.addEventListener("click", (event) => {
        if (event.target === dialog) dialog.close();
      });
    }
    document.addEventListener("visibilitychange", () => {
      if (!document.hidden) refreshAll({ manual: true });
    });
  }

  function parseDate(value) {
    const date = new Date(value);
    return Number.isNaN(date.getTime()) ? new Date(0) : date;
  }

  function shortTime(date) {
    return date.getTime() === 0 ? "—" : clockFormat.format(date);
  }

  function exactTime(date) {
    return date.getTime() === 0 ? "—" : `${date.toLocaleString()} · ${date.toISOString()}`;
  }

  function formatAge(iso) {
    const date = parseDate(iso);
    return formatAgeSeconds(Math.max(0, (Date.now() - date.getTime()) / 1000));
  }

  function formatAgeSeconds(value) {
    const seconds = numeric(value);
    if (!Number.isFinite(seconds)) return "—";
    if (seconds < 1) return "now";
    if (seconds < 60) return `${Math.floor(seconds)}s`;
    if (seconds < 3600) return `${Math.floor(seconds / 60)}m ${Math.floor(seconds % 60)}s`;
    if (seconds < 86400) return `${Math.floor(seconds / 3600)}h ${Math.floor((seconds % 3600) / 60)}m`;
    return `${Math.floor(seconds / 86400)}d ${Math.floor((seconds % 86400) / 3600)}h`;
  }

  function integer(value) {
    return integerFormat.format(numeric(value));
  }

  function formatRate(value) {
    if (!Number.isFinite(value)) return "0";
    if (value >= 100) return integerFormat.format(value);
    if (value >= 10) return value.toFixed(1);
    return value.toFixed(2);
  }

  function numeric(value) {
    const result = Number(value);
    return Number.isFinite(result) ? result : 0;
  }

  function objectText(value) {
    if (!value || typeof value !== "object" || Object.keys(value).length === 0) return "—";
    return Object.entries(value).map(([key, item]) => `${key}=${item}`).join(", ");
  }

  function escapeHTML(value) {
    return String(value ?? "").replace(/[&<>'"]/g, (character) => ({
      "&": "&amp;", "<": "&lt;", ">": "&gt;", "'": "&#39;", '"': "&quot;"
    })[character]);
  }

  bindEvents();
  restartTimer();
  refreshAll();
})();
