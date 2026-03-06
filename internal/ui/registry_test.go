package ui

import (
	"context"
	"testing"
	"time"

	"alerting/internal/config"
	"alerting/test/testutil"
)

func TestRegistryClientPublishAndList(t *testing.T) {
	t.Parallel()

	natsURL, stop := testutil.StartLocalNATSServer(t)
	defer stop()

	client, err := NewRegistryClient(config.UIConfig{
		NATS: config.UINATSConfig{
			URL:            []string{natsURL},
			RegistryBucket: "ui_registry_test",
			RegistryTTLSec: 30,
		},
		Service: config.UIServiceConfig{
			PublicBaseURL: "http://127.0.0.1:8080",
		},
		RefreshSec: 2,
	}, config.ServiceConfig{Name: "alerting", Mode: config.ServiceModeNATS}, "node:a:1", func() time.Time {
		return time.Unix(100, 0).UTC()
	})
	if err != nil {
		t.Fatalf("new registry client: %v", err)
	}
	defer client.Close()

	if err := client.Publish(context.Background(), "/healthz", "/readyz"); err != nil {
		t.Fatalf("publish: %v", err)
	}
	records, err := client.List(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}
	if records[0].HealthURL != "http://127.0.0.1:8080/healthz" {
		t.Fatalf("unexpected health url %q", records[0].HealthURL)
	}
	if records[0].InstanceID != "node:a:1" {
		t.Fatalf("unexpected instance id %q", records[0].InstanceID)
	}
	if records[0].ReadyURL != "http://127.0.0.1:8080/readyz" {
		t.Fatalf("unexpected ready url %q", records[0].ReadyURL)
	}

	if err := client.Delete(); err != nil {
		t.Fatalf("delete: %v", err)
	}
	records, err = client.List(context.Background())
	if err != nil {
		t.Fatalf("list after delete: %v", err)
	}
	if len(records) != 0 {
		t.Fatalf("expected empty registry after delete, got %d", len(records))
	}
}
