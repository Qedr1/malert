package ui

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"alerting/internal/config"
	"alerting/internal/domain"
	"alerting/internal/engine"
	"alerting/internal/state"
)

type registryLister interface {
	List(ctx context.Context) ([]RegistryRecord, error)
	Close() error
}

type runtimeStateProvider interface {
	GetStateSnapshot(alertID string) (engine.RuntimeState, bool)
}

// Dashboard aggregates active-alert and service-status data for the UI.
// Params: state store, current config getter, registry reader, probe client, and clock source.
// Returns: snapshot builder for HTML and JSON handlers.
type Dashboard struct {
	store         state.Store
	currentConfig func() config.Config
	registry      registryLister
	runtime       runtimeStateProvider
	httpClient    *http.Client
	now           func() time.Time
}

// NewDashboard creates one dashboard snapshot builder.
// Params: state store, current config getter, registry reader, and clock source.
// Returns: initialized dashboard.
func NewDashboard(store state.Store, currentConfig func() config.Config, registry registryLister, runtime runtimeStateProvider, now func() time.Time) *Dashboard {
	if now == nil {
		now = time.Now
	}
	timeout := time.Duration(currentConfig().UI.Health.TimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	return &Dashboard{
		store:         store,
		currentConfig: currentConfig,
		registry:      registry,
		runtime:       runtime,
		httpClient:    &http.Client{Timeout: timeout},
		now:           now,
	}
}

// Snapshot builds one full dashboard payload.
// Params: request context for backend reads and probes.
// Returns: aggregated snapshot or backend error.
func (d *Dashboard) Snapshot(ctx context.Context) (Snapshot, error) {
	cfg := d.currentConfig()
	alerts, err := d.loadActiveAlerts(ctx, cfg)
	if err != nil {
		return Snapshot{}, err
	}
	services, err := d.loadServices(ctx, cfg)
	if err != nil {
		return Snapshot{}, err
	}
	snapshot := Snapshot{
		GeneratedAt: d.now().UTC(),
		Alerts:      alerts,
		Services:    services,
	}
	return snapshot, nil
}

func (d *Dashboard) loadActiveAlerts(ctx context.Context, cfg config.Config) ([]AlertRow, error) {
	rulesByName := make(map[string]config.RuleConfig, len(cfg.Rule))
	for _, rule := range cfg.Rule {
		rulesByName[rule.Name] = rule
	}
	seen := make(map[string]struct{})
	rows := make([]AlertRow, 0)
	for _, rule := range cfg.Rule {
		ids, err := d.store.ListAlertIDsByRule(ctx, rule.Name)
		if err != nil {
			return nil, err
		}
		for _, id := range ids {
			if _, exists := seen[id]; exists {
				continue
			}
			seen[id] = struct{}{}
			card, _, err := d.store.GetCard(ctx, id)
			if err != nil {
				if errors.Is(err, state.ErrNotFound) {
					continue
				}
				return nil, err
			}
			if card.State != domain.AlertStatePending && card.State != domain.AlertStateFiring {
				continue
			}
			runtimeState, runtimeOK := d.runtimeSnapshot(id)
			rows = append(rows, AlertRow{
				AlertID:     card.AlertID,
				RuleName:    card.RuleName,
				AlertType:   rule.AlertType,
				State:       card.State,
				Host:        strings.TrimSpace(card.Tags["host"]),
				Var:         card.Var,
				MetricValue: alertMetricValue(card, runtimeState),
				Threshold:   alertThreshold(rule),
				EventCount:  alertEventCount(rule, card, runtimeState, runtimeOK),
				StartedAt:   alertStartedAt(card),
				LastSeenAt:  card.LastSeenAt,
			})
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].State != rows[j].State {
			return rows[i].State < rows[j].State
		}
		if rows[i].RuleName != rows[j].RuleName {
			return rows[i].RuleName < rows[j].RuleName
		}
		if rows[i].Host != rows[j].Host {
			return rows[i].Host < rows[j].Host
		}
		return rows[i].AlertID < rows[j].AlertID
	})
	for index := range rows {
		if rule, ok := rulesByName[rows[index].RuleName]; ok {
			rows[index].AlertType = rule.AlertType
			if rows[index].Threshold == "" {
				rows[index].Threshold = alertThreshold(rule)
			}
		}
	}
	return rows, nil
}

func (d *Dashboard) loadServices(ctx context.Context, cfg config.Config) ([]ServiceRow, error) {
	if d.registry == nil {
		return nil, nil
	}
	records, err := d.registry.List(ctx)
	if err != nil {
		return nil, err
	}
	rows := make([]ServiceRow, len(records))
	now := d.now().UTC()
	staleAfter := time.Duration(cfg.UI.NATS.StaleAfterSec) * time.Second
	offlineAfter := time.Duration(cfg.UI.NATS.OfflineAfterSec) * time.Second
	var waitGroup sync.WaitGroup
	waitGroup.Add(len(records))
	for index, record := range records {
		index := index
		record := record
		go func() {
			defer waitGroup.Done()
			status := d.probeService(ctx, record, now, staleAfter, offlineAfter)
			rows[index] = ServiceRow{
				InstanceID: record.InstanceID,
				Host:       record.Host,
				HostIP:     serviceHostAddress(record),
				Service:    record.Service,
				Mode:       record.Mode,
				Version:    record.Version,
				HealthURL:  record.HealthURL,
				ReadyURL:   record.ReadyURL,
				LastSeen:   record.LastSeen,
				Status:     status,
			}
		}()
	}
	waitGroup.Wait()
	sort.Slice(rows, func(i, j int) bool { return rows[i].InstanceID < rows[j].InstanceID })
	return rows, nil
}

func (d *Dashboard) probeService(ctx context.Context, record RegistryRecord, now time.Time, staleAfter, offlineAfter time.Duration) string {
	age := now.Sub(record.LastSeen)
	if offlineAfter > 0 && age > offlineAfter {
		return "offline"
	}
	readyOK := d.probeURL(ctx, record.ReadyURL)
	if readyOK {
		if staleAfter > 0 && age > staleAfter {
			return "degraded"
		}
		return "online"
	}
	if d.probeURL(ctx, record.HealthURL) {
		return "degraded"
	}
	return "offline"
}

func (d *Dashboard) probeURL(ctx context.Context, rawURL string) bool {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return false
	}
	response, err := d.httpClient.Do(request)
	if err != nil {
		return false
	}
	defer response.Body.Close()
	return response.StatusCode == http.StatusOK
}

func alertStartedAt(card domain.AlertCard) time.Time {
	if card.PendingSince != nil && !card.PendingSince.IsZero() {
		return *card.PendingSince
	}
	return card.RaisedAt
}

func (d *Dashboard) runtimeSnapshot(alertID string) (engine.RuntimeState, bool) {
	if d.runtime == nil {
		return engine.RuntimeState{}, false
	}
	return d.runtime.GetStateSnapshot(alertID)
}

func alertMetricValue(card domain.AlertCard, runtimeState engine.RuntimeState) string {
	if strings.TrimSpace(runtimeState.MetricValue) != "" {
		return runtimeState.MetricValue
	}
	return card.MetricValue
}

func alertThreshold(rule config.RuleConfig) string {
	switch rule.AlertType {
	case "count_total":
		return strconv.Itoa(rule.Raise.N)
	case "count_window":
		return fmt.Sprintf("%d / %ds", rule.Raise.N, rule.Raise.WindowSec)
	case "missing_heartbeat":
		return fmt.Sprintf("%ds", rule.Raise.MissingSec)
	default:
		return ""
	}
}

func alertEventCount(rule config.RuleConfig, card domain.AlertCard, runtimeState engine.RuntimeState, runtimeOK bool) string {
	if !runtimeOK {
		if card.EventCount > 0 {
			return strconv.FormatInt(card.EventCount, 10)
		}
		if rule.AlertType == "missing_heartbeat" {
			return "0"
		}
		return ""
	}
	switch rule.AlertType {
	case "count_total":
		return strconv.FormatInt(runtimeState.ActiveCount, 10)
	case "count_window":
		return strconv.FormatInt(runtimeState.ActiveCount, 10)
	case "missing_heartbeat":
		return "0"
	default:
		return ""
	}
}

func serviceHostAddress(record RegistryRecord) string {
	for _, rawURL := range []string{record.ReadyURL, record.HealthURL} {
		host := hostFromURL(rawURL)
		if host != "" {
			return host
		}
	}
	return ""
}

func hostFromURL(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	host := parsed.Hostname()
	return strings.TrimSpace(host)
}
