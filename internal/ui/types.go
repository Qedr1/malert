package ui

import (
	"time"

	"alerting/internal/domain"
)

const registryKeyPrefix = "alerting/"

// Snapshot contains one dashboard payload sent to HTML clients.
// Params: generated timestamp, alert rows, and service rows.
// Returns: complete dashboard state.
type Snapshot struct {
	GeneratedAt time.Time    `json:"generated_at"`
	Alerts      []AlertRow   `json:"alerts"`
	Services    []ServiceRow `json:"services"`
}

// AlertRow contains one active-alert row rendered by the dashboard.
// Params: persisted card fields mapped to UI-friendly values.
// Returns: one active alert row.
type AlertRow struct {
	AlertID     string            `json:"alert_id"`
	RuleName    string            `json:"rule_name"`
	AlertType   string            `json:"alert_type"`
	State       domain.AlertState `json:"state"`
	Host        string            `json:"host"`
	Var         string            `json:"var"`
	MetricValue string            `json:"metric_value"`
	Threshold   string            `json:"threshold"`
	EventCount  string            `json:"event_count"`
	StartedAt   time.Time         `json:"started_at"`
	LastSeenAt  time.Time         `json:"last_seen_at"`
}

// ServiceRow contains one discovered alerting service row.
// Params: registry metadata, freshness, and probe result.
// Returns: one service-status row.
type ServiceRow struct {
	InstanceID string    `json:"instance_id"`
	Host       string    `json:"host"`
	HostIP     string    `json:"host_ip"`
	Service    string    `json:"service"`
	Mode       string    `json:"mode"`
	Version    string    `json:"version"`
	HealthURL  string    `json:"health_url"`
	ReadyURL   string    `json:"ready_url"`
	LastSeen   time.Time `json:"last_seen"`
	Status     string    `json:"status"`
}

// RegistryRecord stores one discovery entry in the NATS KV registry.
// Params: identity, probe endpoints, mode, version, and heartbeat time.
// Returns: one registry record payload.
type RegistryRecord struct {
	InstanceID string    `json:"instance_id"`
	Host       string    `json:"host"`
	Service    string    `json:"service_name"`
	Mode       string    `json:"mode"`
	Version    string    `json:"version"`
	HealthURL  string    `json:"health_url"`
	ReadyURL   string    `json:"ready_url"`
	LastSeen   time.Time `json:"last_seen"`
}
