package ui

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"alerting/internal/config"
	"alerting/internal/domain"
	"alerting/internal/engine"
	"alerting/internal/state"
)

type fakeRegistry struct {
	records []RegistryRecord
}

func (f fakeRegistry) List(context.Context) ([]RegistryRecord, error) { return f.records, nil }
func (f fakeRegistry) Close() error                                   { return nil }

type fakeProvider struct {
	snapshot Snapshot
}

func (f fakeProvider) Snapshot(context.Context) (Snapshot, error) { return f.snapshot, nil }

type fakeRuntime struct {
	states map[string]engine.RuntimeState
}

func (f fakeRuntime) GetStateSnapshot(alertID string) (engine.RuntimeState, bool) {
	state, ok := f.states[alertID]
	return state, ok
}

func TestDashboardSnapshot(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0).UTC()
	store := state.NewMemoryStore(func() time.Time { return now })
	_, _ = store.PutCard(context.Background(), "rule/ct/errors/a", domain.AlertCard{
		AlertID:    "rule/ct/errors/a",
		RuleName:   "ct",
		State:      domain.AlertStateFiring,
		Var:        "errors",
		Tags:       map[string]string{"host": "app-1"},
		RaisedAt:   now.Add(-time.Minute),
		LastSeenAt: now,
	})
	_, _ = store.PutCard(context.Background(), "rule/ct/errors/b", domain.AlertCard{
		AlertID:      "rule/ct/errors/b",
		RuleName:     "ct",
		State:        domain.AlertStatePending,
		Var:          "errors",
		Tags:         map[string]string{"host": "app-2"},
		PendingSince: ptrTime(now.Add(-30 * time.Second)),
		LastSeenAt:   now,
	})

	healthServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/readyz" || request.URL.Path == "/healthz" {
			writer.WriteHeader(http.StatusOK)
			return
		}
		writer.WriteHeader(http.StatusNotFound)
	}))
	defer healthServer.Close()

	cfg := config.Config{
		UI: config.UIConfig{
			NATS: config.UINATSConfig{
				StaleAfterSec:   10,
				OfflineAfterSec: 30,
			},
			Health: config.UIHealthConfig{TimeoutSec: 1},
		},
		Rule: []config.RuleConfig{{
			Name:      "ct",
			AlertType: "count_total",
			Raise:     config.RuleRaise{N: 3},
		}},
	}
	dashboard := NewDashboard(store, func() config.Config { return cfg }, fakeRegistry{
		records: []RegistryRecord{
			{
				InstanceID: "node-a",
				Host:       "host-a",
				Service:    "alerting",
				Mode:       "nats",
				Version:    "dev",
				HealthURL:  healthServer.URL + "/healthz",
				ReadyURL:   healthServer.URL + "/readyz",
				LastSeen:   now,
			},
			{
				InstanceID: "node-b",
				Host:       "host-b",
				Service:    "alerting",
				Mode:       "nats",
				Version:    "dev",
				HealthURL:  "http://127.0.0.1:1/healthz",
				ReadyURL:   "http://127.0.0.1:1/readyz",
				LastSeen:   now.Add(-time.Minute),
			},
		},
	}, fakeRuntime{
		states: map[string]engine.RuntimeState{
			"rule/ct/errors/a": {
				AlertID:     "rule/ct/errors/a",
				RuleName:    "ct",
				MetricValue: "7",
				CountTotal:  7,
				ActiveCount: 7,
			},
			"rule/ct/errors/b": {
				AlertID:     "rule/ct/errors/b",
				RuleName:    "ct",
				MetricValue: "3",
				CountTotal:  3,
				ActiveCount: 3,
			},
		},
	}, func() time.Time { return now })

	snapshot, err := dashboard.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(snapshot.Alerts) != 2 {
		t.Fatalf("unexpected alert rows: %+v", snapshot.Alerts)
	}
	if len(snapshot.Services) != 2 {
		t.Fatalf("unexpected service rows: %+v", snapshot.Services)
	}
	if snapshot.Alerts[0].AlertType != "count_total" || snapshot.Alerts[0].Threshold != "3" {
		t.Fatalf("unexpected alert metadata: %+v", snapshot.Alerts[0])
	}
	if snapshot.Alerts[0].EventCount == "" || snapshot.Alerts[0].MetricValue == "" {
		t.Fatalf("missing alert counters: %+v", snapshot.Alerts[0])
	}
	if snapshot.Services[0].HostIP == "" {
		t.Fatalf("missing host ip: %+v", snapshot.Services[0])
	}
}

func TestServerBasicAuthDashboardAPI(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(NewServer(config.UIConfig{
		BasePath:   "/ui",
		RefreshSec: 1,
		Auth: config.UIAuthConfig{
			Basic: config.UIBasicAuthConfig{
				Enabled:  true,
				Username: "ops",
				Password: "secret",
			},
		},
	}, fakeProvider{snapshot: Snapshot{
		GeneratedAt: time.Unix(1_700_000_000, 0).UTC(),
	}}).Handler())
	defer server.Close()

	response, err := http.Get(server.URL + "/ui")
	if err != nil {
		t.Fatalf("get ui: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", response.StatusCode)
	}

	request, err := http.NewRequest(http.MethodGet, server.URL+"/ui", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	request.SetBasicAuth("ops", "secret")
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("authorized get ui: %v", err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), "Alerting Dashboard") {
		t.Fatalf("unexpected ui response: code=%d body=%s", response.StatusCode, string(body))
	}
	if !strings.Contains(string(body), `fetch(basePath + "/api/dashboard"`) {
		t.Fatalf("dashboard page does not use snapshot api: %s", string(body))
	}
	if !strings.Contains(string(body), "Backend Snapshot Feed Lost") || !strings.Contains(string(body), "showOfflineModal(") {
		t.Fatalf("dashboard page missing backend-offline overlay: %s", string(body))
	}

	request, err = http.NewRequest(http.MethodGet, server.URL+"/ui/", nil)
	if err != nil {
		t.Fatalf("new slash request: %v", err)
	}
	request.SetBasicAuth("ops", "secret")
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("authorized get ui slash: %v", err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("unexpected ui slash code %d", response.StatusCode)
	}

	response, err = http.Get(server.URL + "/ui/api/dashboard")
	if err != nil {
		t.Fatalf("get dashboard api: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 for dashboard api, got %d", response.StatusCode)
	}

	request, err = http.NewRequest(http.MethodGet, server.URL+"/ui/api/dashboard", nil)
	if err != nil {
		t.Fatalf("new dashboard api request: %v", err)
	}
	request.SetBasicAuth("ops", "secret")
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("authorized get dashboard api: %v", err)
	}
	body, _ = io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("unexpected dashboard api code %d body=%s", response.StatusCode, string(body))
	}
	if !strings.Contains(string(body), `"generated_at":"`) {
		t.Fatalf("unexpected dashboard api body: %s", string(body))
	}
}

func ptrTime(value time.Time) *time.Time {
	return &value
}
