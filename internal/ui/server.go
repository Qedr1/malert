package ui

import (
	"context"
	"encoding/json"
	"html/template"
	"net/http"
	"strconv"
	"strings"

	"alerting/internal/config"
)

type snapshotProvider interface {
	Snapshot(ctx context.Context) (Snapshot, error)
}

// Server exposes HTTP handlers for the built-in dashboard.
// Params: UI config and snapshot provider.
// Returns: handlers for HTML, JSON snapshot, and health routes.
type Server struct {
	cfg      config.UIConfig
	provider snapshotProvider
	page     *template.Template
}

// NewServer creates one HTTP UI server helper.
// Params: UI config and snapshot provider.
// Returns: initialized UI server.
func NewServer(cfg config.UIConfig, provider snapshotProvider) *Server {
	return &Server{
		cfg:      cfg,
		provider: provider,
		page:     template.Must(template.New("ui").Parse(dashboardHTML)),
	}
}

// Handler builds an HTTP handler with auth-protected UI routes.
// Params: none.
// Returns: mux containing UI endpoints.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	basePath := s.basePath()
	mux.Handle(basePath, s.wrapAuth(http.HandlerFunc(s.handleDashboard)))
	mux.Handle(basePath+"/", s.wrapAuth(http.HandlerFunc(s.handleDashboard)))
	mux.Handle(basePath+"/api/dashboard", s.wrapAuth(http.HandlerFunc(s.handleSnapshot)))
	mux.Handle(basePath+"/healthz", s.wrapAuth(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte("ok"))
	})))
	return mux
}

func (s *Server) wrapAuth(next http.Handler) http.Handler {
	if !s.cfg.Auth.Basic.Enabled {
		return next
	}
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		username, password, ok := request.BasicAuth()
		if !ok || username != s.cfg.Auth.Basic.Username || password != s.cfg.Auth.Basic.Password {
			writer.Header().Set("WWW-Authenticate", `Basic realm="alerting-ui"`)
			http.Error(writer, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(writer, request)
	})
}

func (s *Server) handleDashboard(writer http.ResponseWriter, request *http.Request) {
	snapshot, err := s.provider.Snapshot(request.Context())
	if err != nil {
		http.Error(writer, err.Error(), http.StatusInternalServerError)
		return
	}
	initialJSON, err := json.Marshal(snapshot)
	if err != nil {
		http.Error(writer, err.Error(), http.StatusInternalServerError)
		return
	}
	pageData := struct {
		Title       string
		BasePathJS  template.JS
		InitialJSON template.JS
		RefreshMS   int
	}{
		Title:       "Alerting Dashboard",
		BasePathJS:  template.JS(strconv.Quote(s.basePath())),
		InitialJSON: template.JS(initialJSON),
		RefreshMS:   s.cfg.RefreshSec * 1000,
	}
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = s.page.Execute(writer, pageData)
}

func (s *Server) handleSnapshot(writer http.ResponseWriter, request *http.Request) {
	snapshot, err := s.provider.Snapshot(request.Context())
	if err != nil {
		http.Error(writer, err.Error(), http.StatusInternalServerError)
		return
	}
	writeSnapshotJSON(writer, snapshot)
}

func (s *Server) basePath() string {
	basePath := strings.TrimSpace(s.cfg.BasePath)
	if basePath == "" {
		return "/ui"
	}
	return basePath
}

func writeSnapshotJSON(writer http.ResponseWriter, snapshot Snapshot) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(writer).Encode(snapshot)
}

const dashboardHTML = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>{{ .Title }}</title>
  <style>
    :root { color-scheme: dark; --accent:#22d3ee; --accent-light:#67e8f9; --accent-rgb:34,211,238; --bg:#040d12; --surface:rgba(255,255,255,.025); --surface-strong:rgba(255,255,255,.045); --panel:rgba(255,255,255,.035); --panel-hover:rgba(255,255,255,.055); --ink:#ffffff; --muted:rgba(255,255,255,.58); --dim:rgba(255,255,255,.38); --faint:rgba(255,255,255,.2); --line:rgba(34,211,238,.18); --line-strong:rgba(34,211,238,.46); --warn:#fbbf24; --bad:#f87171; --good:#34d399; --shadow:0 18px 44px rgba(0,0,0,.28); --radius-sm:2px; --radius-md:4px; }
    * { box-sizing:border-box; }
    html { background:var(--bg); }
    body { margin:0; font-family:Inter,system-ui,-apple-system,"Segoe UI",sans-serif; color:var(--ink); background-color:var(--bg); background-image:repeating-linear-gradient(0deg,transparent,transparent 39px,rgba(var(--accent-rgb),.05) 39px,rgba(var(--accent-rgb),.05) 40px),repeating-linear-gradient(90deg,transparent,transparent 39px,rgba(var(--accent-rgb),.05) 39px,rgba(var(--accent-rgb),.05) 40px); }
    .shell { min-height:100vh; background:radial-gradient(ellipse at 25% 50%,rgba(var(--accent-rgb),.07) 0%,transparent 60%),radial-gradient(ellipse at 80% 15%,rgba(var(--accent-rgb),.04) 0%,transparent 50%); }
    .topbar { border-bottom:1px solid rgba(var(--accent-rgb),.15); }
    .topbar-inner { max-width:1380px; margin:0 auto; padding:12px 18px; display:flex; justify-content:space-between; gap:12px; align-items:center; font-family:ui-monospace,"JetBrains Mono",Consolas,monospace; font-size:12px; letter-spacing:.12em; text-transform:uppercase; color:rgba(var(--accent-rgb),.42); }
    .topbar-status { color:var(--accent); opacity:.72; }
    main { max-width:1380px; margin:0 auto; padding:22px 18px 42px; }
    .hero { margin-bottom:12px; }
    .eyebrow, .label, th { font-family:ui-monospace,"JetBrains Mono",Consolas,monospace; font-size:12px; letter-spacing:.16em; text-transform:uppercase; color:var(--muted); }
    .hero h1 { margin:0; font-family:"Bebas Neue","Oswald","Arial Narrow Bold",Impact,sans-serif; font-size:clamp(28px,4vw,55px); line-height:.92; letter-spacing:.04em; font-weight:400; text-transform:uppercase; white-space:nowrap; }
    section { margin-top:12px; overflow:auto; padding:0; }
    .section-head { display:flex; align-items:flex-end; justify-content:space-between; gap:10px; padding:10px 12px 8px; border-bottom:1px solid var(--line); background:rgba(255,255,255,.01); }
    h2 { margin:0; font-family:"Bebas Neue","Oswald","Arial Narrow Bold",Impact,sans-serif; font-size:24px; line-height:.95; letter-spacing:.04em; font-weight:400; text-transform:uppercase; }
    .section-note { color:var(--dim); font-size:12px; }
    table { width:100%; border-collapse:collapse; font-size:13px; min-width:980px; }
    th, td { text-align:left; padding:6px 8px; border-bottom:1px solid var(--line); vertical-align:top; }
    th { color:var(--muted); background:rgba(4,13,18,.92); position:sticky; top:0; z-index:1; }
    tbody tr:hover { background:var(--panel-hover); }
    .mono { font-family:ui-monospace,"JetBrains Mono",Consolas,monospace; }
    .meta-line { color:var(--dim); font-size:11px; margin-top:1px; }
    .status-online { color:var(--good); font-weight:700; }
    .status-degraded, .state-pending { color:var(--warn); font-weight:700; }
    .status-offline, .state-firing { color:var(--bad); font-weight:700; }
    .chip { display:inline-block; padding:2px 6px; border:1px solid currentColor; border-radius:var(--radius-sm); font-size:10px; letter-spacing:.08em; text-transform:uppercase; background:rgba(255,255,255,.02); }
    .empty { color:var(--muted); padding:10px 12px; }
    .dense { line-height:1.15; }
    .tooltip { cursor:default; border-bottom:1px dotted rgba(var(--accent-rgb),.35); }
    .headline-accent { color:var(--accent); }
    .offline-modal { position:fixed; inset:0; display:flex; align-items:center; justify-content:center; padding:24px; background:rgba(4,13,18,.84); backdrop-filter:blur(6px); z-index:99; }
    .offline-modal[hidden] { display:none; }
    .offline-panel { width:min(100%,560px); border:1px solid rgba(248,113,113,.55); background:linear-gradient(180deg,rgba(248,113,113,.10),rgba(4,13,18,.98)); box-shadow:0 0 0 1px rgba(248,113,113,.15),0 24px 80px rgba(0,0,0,.48); padding:20px 22px 18px; position:relative; overflow:hidden; }
    .offline-panel::before { content:""; position:absolute; top:0; left:0; width:4px; height:100%; background:var(--bad); box-shadow:0 0 18px rgba(248,113,113,.65); }
    .offline-kicker { font-family:ui-monospace,"JetBrains Mono",Consolas,monospace; color:var(--bad); font-size:12px; letter-spacing:.18em; text-transform:uppercase; }
    .offline-title { margin:8px 0 10px; font-family:"Bebas Neue","Oswald","Arial Narrow Bold",Impact,sans-serif; font-size:42px; line-height:.92; letter-spacing:.04em; color:var(--bad); text-transform:uppercase; }
    .offline-copy { color:rgba(255,255,255,.82); font-size:14px; line-height:1.35; max-width:48ch; }
    .offline-meta { margin-top:12px; color:rgba(255,255,255,.5); font-size:11px; letter-spacing:.12em; text-transform:uppercase; font-family:ui-monospace,"JetBrains Mono",Consolas,monospace; }
    @keyframes scanline { 0% { transform:translate(-100%); } 100% { transform:translate(400%); } }
    @media (max-width: 960px) {
      main { padding:16px 12px 34px; }
      .topbar-inner { padding:10px 12px; }
    }
    @media (max-width: 720px) {
      h2 { font-size:17px; }
      .section-head { display:block; }
      .topbar-inner { flex-direction:column; align-items:flex-start; }
      .offline-title { font-size:34px; }
    }
  </style>
</head>
<body>
<div class="shell">
  <div id="offline-modal" class="offline-modal" hidden>
    <div class="offline-panel" role="alertdialog" aria-live="assertive" aria-labelledby="offline-title">
      <div class="offline-kicker">Firing</div>
      <div id="offline-title" class="offline-title">Backend Snapshot Feed Lost</div>
      <div class="offline-copy">The dashboard can no longer reach the alerting backend. Live status, service topology, and active alert rows may be stale until the feed recovers.</div>
      <div id="offline-meta" class="offline-meta">Retrying automatically.</div>
    </div>
  </div>
  <div class="topbar">
    <div class="topbar-inner">
      <span>ALERTING // CONTROL SURFACE</span>
      <span class="topbar-status">STATUS: LIVE SNAPSHOT FEED</span>
    </div>
  </div>
<main>
  <div class="hero">
    <h1>Live <span class="headline-accent">Alerting</span> Dashboard</h1>
  </div>
  <section>
    <div class="section-head">
      <div>
        <h2>Services</h2>
      </div>
      <div class="section-note">Discovery comes from the NATS registry; status comes from live readyz / healthz probes.</div>
    </div>
    <div id="services-empty" class="empty" hidden>No services discovered.</div>
    <table id="services-table">
      <thead><tr><th>Service</th><th>Status</th><th>Mode</th><th>Version</th><th>Last Seen</th></tr></thead>
      <tbody id="services-body"></tbody>
    </table>
  </section>
  <section>
    <div class="section-head">
      <div>
        <h2>Alerts</h2>
      </div>
      <div class="section-note">Active lifecycle rows show latest value, threshold and accumulated event count.</div>
    </div>
    <div id="alerts-empty" class="empty" hidden>No active alerts.</div>
    <table id="alerts-table">
      <thead><tr><th>Rule</th><th>Type</th><th>State</th><th>Host</th><th>Alert ID</th><th>Var</th><th>Latest</th><th>Threshold</th><th>Events</th><th>Started</th><th>Last Seen</th></tr></thead>
      <tbody id="alerts-body"></tbody>
    </table>
  </section>
</main>
<script>
const initial = {{ .InitialJSON }};
const basePath = {{ .BasePathJS }};
const refreshMs = {{ .RefreshMS }};
function esc(value) {
  return String(value ?? "").replaceAll("&","&amp;").replaceAll("<","&lt;").replaceAll(">","&gt;");
}
function display(value) {
  const text = String(value ?? "").trim();
  return text === "" ? "—" : text;
}
function formatTimestamp(value) {
  if (!value) {
    return "—";
  }
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) {
    return esc(value);
  }
  const pad = (part) => String(part).padStart(2, "0");
  return date.getUTCFullYear() + "-" +
    pad(date.getUTCMonth() + 1) + "-" +
    pad(date.getUTCDate()) + " " +
    pad(date.getUTCHours()) + ":" +
    pad(date.getUTCMinutes()) + ":" +
    pad(date.getUTCSeconds());
}
function formatAge(value) {
  if (!value) {
    return "—";
  }
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) {
    return display(value);
  }
  const diffMs = Date.now() - date.getTime();
  if (diffMs < 0) {
    return "0s ago";
  }
  const seconds = Math.floor(diffMs / 1000);
  if (seconds < 60) {
    return seconds + "s ago";
  }
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) {
    return minutes + "m ago";
  }
  const hours = Math.floor(minutes / 60);
  if (hours < 24) {
    return hours + "h ago";
  }
  const days = Math.floor(hours / 24);
  return days + "d ago";
}
function shortAlertID(value) {
  const text = String(value ?? "");
  if (text.length <= 20) {
    return text;
  }
  return text.slice(0, 20) + "…";
}
function renderServices(items) {
  document.getElementById("services-empty").hidden = items.length !== 0;
  document.getElementById("services-body").innerHTML = items.map((item) =>
    "<tr>" +
      "<td class=\"dense\"><span class=\"tooltip\" title=\"" + esc(item.instance_id) + "\">" + esc(item.service) + "</span><div class=\"meta-line\">" + esc(display(item.host)) + "</div><div class=\"meta-line mono\">" + esc(display(item.host_ip)) + "</div></td>" +
      "<td><span class=\"chip status-" + esc(item.status) + "\">" + esc(item.status) + "</span></td>" +
      "<td class=\"mono\">" + esc(display(item.mode)) + "</td>" +
      "<td class=\"mono\">" + esc(display(item.version)) + "</td>" +
      "<td class=\"mono\">" + esc(formatTimestamp(item.last_seen)) + "</td>" +
    "</tr>"
  ).join("");
}
function renderAlerts(items) {
  document.getElementById("alerts-empty").hidden = items.length !== 0;
  document.getElementById("alerts-body").innerHTML = items.map((item) =>
    "<tr>" +
      "<td class=\"dense\">" + esc(display(item.rule_name)) + "</td>" +
      "<td class=\"mono\">" + esc(display(item.alert_type)) + "</td>" +
      "<td><span class=\"chip state-" + esc(item.state) + "\">" + esc(item.state) + "</span></td>" +
      "<td>" + esc(display(item.host)) + "</td>" +
      "<td class=\"mono\"><span class=\"tooltip\" title=\"" + esc(item.alert_id) + "\">" + esc(shortAlertID(item.alert_id)) + "</span></td>" +
      "<td class=\"mono\">" + esc(display(item.var)) + "</td>" +
      "<td class=\"mono\">" + esc(display(item.metric_value)) + "</td>" +
      "<td class=\"mono\">" + esc(display(item.threshold)) + "</td>" +
      "<td class=\"mono\">" + esc(display(item.event_count)) + "</td>" +
      "<td class=\"mono\">" + esc(formatTimestamp(item.started_at)) + "</td>" +
      "<td class=\"mono\" data-last-seen=\"" + esc(item.last_seen_at) + "\">" + esc(formatAge(item.last_seen_at)) + "</td>" +
    "</tr>"
  ).join("");
}
function refreshLastSeenCells() {
  document.querySelectorAll("[data-last-seen]").forEach((cell) => {
    cell.textContent = formatAge(cell.getAttribute("data-last-seen"));
  });
}
function render(snapshot) {
  renderServices(snapshot.services);
  renderAlerts(snapshot.alerts);
  refreshLastSeenCells();
}
render(initial);
const offlineModal = document.getElementById("offline-modal");
const offlineMeta = document.getElementById("offline-meta");
let refreshInFlight = false;
function showOfflineModal(message) {
  offlineMeta.textContent = message || "Retrying automatically.";
  offlineModal.hidden = false;
}
function hideOfflineModal() {
  offlineModal.hidden = true;
  offlineMeta.textContent = "Retrying automatically.";
}
async function refreshSnapshot() {
  if (refreshInFlight) {
    return;
  }
  refreshInFlight = true;
  try {
    const response = await fetch(basePath + "/api/dashboard", {
      method: "GET",
      headers: { "Accept": "application/json" },
      credentials: "same-origin",
      cache: "no-store"
    });
    if (!response.ok) {
      throw new Error("snapshot " + response.status);
    }
    render(await response.json());
    hideOfflineModal();
  } catch (error) {
    showOfflineModal("Last fetch failed: " + (error && error.message ? error.message : "backend unavailable"));
  } finally {
    refreshInFlight = false;
  }
}
setInterval(refreshSnapshot, refreshMs);
setInterval(refreshLastSeenCells, 1000);
document.addEventListener("visibilitychange", () => {
  if (document.visibilityState === "visible") {
    refreshSnapshot();
  }
});
</script>
</main>
</div>
</body>
</html>`
