package ui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"runtime/debug"
	"strings"
	"time"

	"alerting/internal/config"

	"github.com/nats-io/nats.go"
)

// RegistryClient provides access to the UI discovery registry in NATS KV.
// Params: NATS connection, JetStream context, and registry bucket handle.
// Returns: registry read/write operations for UI runtime.
type RegistryClient struct {
	nc   *nats.Conn
	kv   nats.KeyValue
	cfg  config.UIConfig
	now  func() time.Time
	key  string
	base RegistryRecord
}

// NewRegistryClient opens or creates the UI registry bucket.
// Params: UI config, service config, instance identity, and clock source.
// Returns: initialized registry client or setup error.
func NewRegistryClient(uiCfg config.UIConfig, serviceCfg config.ServiceConfig, instanceID string, now func() time.Time) (*RegistryClient, error) {
	if now == nil {
		now = time.Now
	}
	nc, err := nats.Connect(strings.Join(uiCfg.NATS.URL, ","))
	if err != nil {
		return nil, fmt.Errorf("connect ui nats: %w", err)
	}
	js, err := nc.JetStream()
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("ui jetstream init: %w", err)
	}
	kv, err := js.KeyValue(uiCfg.NATS.RegistryBucket)
	if err != nil {
		kv, err = js.CreateKeyValue(&nats.KeyValueConfig{
			Bucket: uiCfg.NATS.RegistryBucket,
			TTL:    time.Duration(uiCfg.NATS.RegistryTTLSec) * time.Second,
		})
		if err != nil {
			nc.Close()
			return nil, fmt.Errorf("open ui registry bucket %q: %w", uiCfg.NATS.RegistryBucket, err)
		}
	}
	host, err := os.Hostname()
	if err != nil {
		host = "unknown"
	}
	baseURL := strings.TrimRight(uiCfg.Service.PublicBaseURL, "/")
	return &RegistryClient{
		nc:  nc,
		kv:  kv,
		cfg: uiCfg,
		now: now,
		key: registryKeyPrefix + sanitizeRegistryKey(instanceID),
		base: RegistryRecord{
			InstanceID: instanceID,
			Host:       host,
			Service:    serviceCfg.Name,
			Mode:       config.NormalizeServiceMode(serviceCfg.Mode),
			Version:    currentBuildVersion(),
			HealthURL:  baseURL,
			ReadyURL:   baseURL,
		},
	}, nil
}

// Publish writes the current service registry record into NATS KV.
// Params: context for cancellation.
// Returns: write error.
func (c *RegistryClient) Publish(_ context.Context, healthPath, readyPath string) error {
	record := c.base
	record.HealthURL = strings.TrimRight(c.cfg.Service.PublicBaseURL, "/") + defaultPath(healthPath)
	record.ReadyURL = strings.TrimRight(c.cfg.Service.PublicBaseURL, "/") + defaultPath(readyPath)
	record.LastSeen = c.now().UTC()
	body, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("encode registry record: %w", err)
	}
	if _, err := c.kv.Put(c.key, body); err != nil {
		return fmt.Errorf("put registry record: %w", err)
	}
	return nil
}

// Run publishes the registry record immediately and then refreshes it periodically.
// Params: context for lifecycle and service probe paths.
// Returns: nil on context cancellation or first publish error.
func (c *RegistryClient) Run(ctx context.Context, healthPath, readyPath string) error {
	if err := c.Publish(ctx, healthPath, readyPath); err != nil {
		return err
	}
	interval := registryRefreshInterval(c.cfg)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := c.Publish(ctx, healthPath, readyPath); err != nil {
				return err
			}
		}
	}
}

// List returns all currently visible registry records.
// Params: context for cancellation.
// Returns: decoded registry records.
func (c *RegistryClient) List(_ context.Context) ([]RegistryRecord, error) {
	keys, err := c.kv.Keys()
	if err != nil {
		if errors.Is(err, nats.ErrNoKeysFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("list ui registry keys: %w", err)
	}
	records := make([]RegistryRecord, 0, len(keys))
	for _, key := range keys {
		if !strings.HasPrefix(key, registryKeyPrefix) {
			continue
		}
		entry, err := c.kv.Get(key)
		if err != nil {
			if errors.Is(err, nats.ErrKeyNotFound) {
				continue
			}
			return nil, fmt.Errorf("get ui registry key %q: %w", key, err)
		}
		var record RegistryRecord
		if err := json.Unmarshal(entry.Value(), &record); err != nil {
			return nil, fmt.Errorf("decode ui registry key %q: %w", key, err)
		}
		records = append(records, record)
	}
	return records, nil
}

// Close closes the underlying NATS connection.
// Params: none.
// Returns: nil after connection close.
func (c *RegistryClient) Close() error {
	if c == nil || c.nc == nil {
		return nil
	}
	c.nc.Close()
	return nil
}

// Delete removes this service record from the registry bucket.
// Params: none.
// Returns: delete error except missing-key cases.
func (c *RegistryClient) Delete() error {
	if c == nil || c.kv == nil || strings.TrimSpace(c.key) == "" {
		return nil
	}
	if err := c.kv.Delete(c.key); err != nil && !errors.Is(err, nats.ErrKeyNotFound) {
		return fmt.Errorf("delete ui registry record: %w", err)
	}
	return nil
}

func registryRefreshInterval(cfg config.UIConfig) time.Duration {
	refreshSec := cfg.RefreshSec
	if refreshSec <= 0 {
		refreshSec = 1
	}
	interval := time.Duration(refreshSec) * time.Second
	maxInterval := time.Duration(cfg.NATS.RegistryTTLSec/2) * time.Second
	if maxInterval > 0 && interval > maxInterval {
		return maxInterval
	}
	return interval
}

func defaultPath(path string) string {
	if strings.TrimSpace(path) == "" {
		return "/"
	}
	if !strings.HasPrefix(path, "/") {
		return "/" + path
	}
	return path
}

func sanitizeRegistryKey(value string) string {
	if strings.TrimSpace(value) == "" {
		return "unknown"
	}
	var builder strings.Builder
	builder.Grow(len(value))
	for _, symbol := range value {
		switch {
		case symbol >= 'a' && symbol <= 'z':
			builder.WriteRune(symbol)
		case symbol >= 'A' && symbol <= 'Z':
			builder.WriteRune(symbol)
		case symbol >= '0' && symbol <= '9':
			builder.WriteRune(symbol)
		case symbol == '-' || symbol == '_' || symbol == '.':
			builder.WriteRune(symbol)
		default:
			builder.WriteByte('_')
		}
	}
	return builder.String()
}

func currentBuildVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "dev"
	}
	if version := strings.TrimSpace(info.Main.Version); version != "" && version != "(devel)" {
		return version
	}
	var revision string
	var dirty string
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			revision = strings.TrimSpace(setting.Value)
		case "vcs.modified":
			dirty = strings.TrimSpace(setting.Value)
		}
	}
	if revision == "" {
		return "dev"
	}
	if len(revision) > 12 {
		revision = revision[:12]
	}
	if dirty == "true" {
		return revision + "-dirty"
	}
	return revision
}
