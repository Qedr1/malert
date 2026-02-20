package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"alerting/internal/clock"
	"alerting/internal/config"
	"alerting/internal/domain"
	"alerting/internal/engine"
	"alerting/internal/notify"
	"alerting/internal/notifyqueue"
	"alerting/internal/state"
)

// Manager coordinates rule evaluation, state persistence, and notifications.
// Params: runtime config, rule engine, state backend, notifier, logger, and clock.
// Returns: event sink and periodic worker entrypoint.
type Manager struct {
	mu         sync.RWMutex
	cfg        config.Config
	rules      map[string]config.RuleConfig
	sorted     []config.RuleConfig
	logger     *slog.Logger
	engine     *engine.Engine
	store      state.Store
	dispatcher *notify.Dispatcher
	producer   notifyqueue.Producer
	clock      clock.Clock
}

// NewManager creates manager with initial configuration.
// Params: initial config, logger, state store, notifier dispatcher, and clock.
// Returns: initialized manager.
func NewManager(cfg config.Config, logger *slog.Logger, store state.Store, dispatcher *notify.Dispatcher, clk clock.Clock) *Manager {
	rules, sorted := buildRuleIndex(cfg.Rule)
	return &Manager{
		cfg:        cfg,
		rules:      rules,
		sorted:     sorted,
		logger:     logger,
		engine:     engine.New(),
		store:      store,
		dispatcher: dispatcher,
		clock:      clk,
	}
}

// Push processes one incoming event from ingest interfaces.
// Params: validated incoming event.
// Returns: processing error when backend operation fails.
func (m *Manager) Push(event domain.Event) error {
	return m.ProcessEvent(context.Background(), event)
}

// ProcessEvent runs all rule evaluations for one event.
// Params: context and validated event payload.
// Returns: first backend error on store/notify failure.
func (m *Manager) ProcessEvent(ctx context.Context, event domain.Event) error {
	now := m.clock.Now()

	m.mu.RLock()
	rules := m.sortedRulesLocked()
	m.mu.RUnlock()

	for _, rule := range rules {
		if m.shouldDropOutOfOrder(rule, event, now) {
			m.logger.Warn("event dropped by out_of_order filter", "rule", rule.Name, "dt", event.DT)
			continue
		}
		if !engine.MatchEvent(rule, event) {
			continue
		}
		alertID, err := engine.BuildAlertKey(rule, event)
		if err != nil {
			m.logger.Warn("event ignored due key build error", "rule", rule.Name, "error", err.Error())
			continue
		}

		decision := m.engine.ProcessEvent(rule, event, alertID, now)
		if !decision.Matched {
			continue
		}
		if err := m.applyDecision(ctx, rule, alertID, decision, now); err != nil {
			return err
		}
	}

	return nil
}

// Tick evaluates time-based transitions and repeat notifications.
// Params: context for backend operations.
// Returns: first backend error from resolve/repeat processing.
func (m *Manager) Tick(ctx context.Context) error {
	now := m.clock.Now()

	m.mu.RLock()
	rules := m.sortedRulesLocked()
	m.mu.RUnlock()
	repeatPerChannel := m.repeatPerChannel()

	for _, rule := range rules {
		decisions, firingAlertIDs := m.engine.TickRuleWithFiring(rule, now)
		for _, decision := range decisions {
			if err := m.applyDecision(ctx, rule, decision.AlertID, decision, now); err != nil {
				return err
			}
		}

		repeatEnabled := m.repeatEnabled(rule)
		repeatEvery := m.repeatEvery(rule)
		routes := rule.Notify.Route
		for _, alertID := range firingAlertIDs {
			runtimeState, exists := m.engine.GetStateSnapshot(alertID)
			if !exists {
				continue
			}
			if runtimeState.CurrentState != domain.AlertStateFiring || !repeatEnabled || len(routes) == 0 {
				continue
			}
			notification := domain.Notification{
				AlertID:     runtimeState.AlertID,
				ShortID:     shortAlertID(runtimeState.AlertID),
				RuleName:    runtimeState.RuleName,
				Var:         runtimeState.Var,
				Tags:        runtimeState.Tags,
				State:       domain.AlertStateFiring,
				Message:     "firing repeat",
				MetricValue: runtimeState.MetricValue,
				StartedAt:   startedAt(runtimeState.RaisedAt, now),
				Timestamp:   now,
			}

			if !repeatPerChannel {
				last := runtimeState.LastNotified["global"]
				if !last.IsZero() && now.Sub(last) < repeatEvery {
					continue
				}
				if err := m.dispatchNotification(ctx, rule, notification); err != nil {
					m.logger.Error("repeat notification failed", "rule", rule.Name, "alert_id", runtimeState.AlertID, "error", err.Error())
				}
				continue
			}

			for _, route := range routes {
				notifyKey := route.Channel
				last := runtimeState.LastNotified[notifyKey]
				if !last.IsZero() && now.Sub(last) < repeatEvery {
					continue
				}
				if err := m.dispatchNotification(ctx, rule, notification, route); err != nil {
					m.logger.Error("repeat notification failed", "rule", rule.Name, "alert_id", runtimeState.AlertID, "error", err.Error())
					continue
				}
			}
		}
	}

	idleTTL := m.runtimeStateIdleTTL()
	maxStates := m.runtimeStateMax()
	if idleTTL > 0 || maxStates > 0 {
		evicted := m.engine.CompactStates(now, idleTTL, maxStates)
		if evicted > 0 {
			m.logger.Warn("runtime states compacted", "evicted", evicted, "idle_ttl", idleTTL.String(), "max_states", maxStates)
		}
	}

	return nil
}

// SetDispatcher replaces runtime notification dispatcher.
// Params: fresh dispatcher built from active notify config.
// Returns: dispatcher reference swapped atomically.
func (m *Manager) SetDispatcher(dispatcher *notify.Dispatcher) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.dispatcher = dispatcher
}

// SetQueueProducer replaces runtime notification queue producer.
// Params: queue producer built from active notify.queue config.
// Returns: producer reference swapped atomically.
func (m *Manager) SetQueueProducer(producer notifyqueue.Producer) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.producer = producer
}

// ResolveByTTL resolves one alert by tick expiration callback.
// Params: alert ID and marker reason from KV delete marker.
// Returns: backend error when resolve cannot be persisted.
func (m *Manager) ResolveByTTL(ctx context.Context, alertID, reason string) error {
	exists, err := m.store.HasTick(ctx, alertID)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}

	card, revision, err := m.store.GetCard(ctx, alertID)
	if err != nil {
		if errors.Is(err, state.ErrNotFound) {
			m.logger.Error("ttl resolve card not found", "alert_id", alertID)
			return nil
		}
		return err
	}
	if card.State != domain.AlertStatePending && card.State != domain.AlertStateFiring {
		return nil
	}

	if reason == "" {
		reason = "ttl_expired"
	}
	card.State = domain.AlertStateResolved
	closedAt := m.clock.Now()
	if card.RaisedAt.IsZero() {
		card.RaisedAt = card.LastSeenAt
		if card.RaisedAt.IsZero() {
			card.RaisedAt = closedAt
		}
	}
	card.ClosedAt = &closedAt
	card.ResolveCause = reason
	if _, err := m.store.UpdateCard(ctx, alertID, revision, card); err != nil {
		if errors.Is(err, state.ErrConflict) {
			return nil
		}
		return err
	}

	rule, ok := m.ruleByName(card.RuleName)
	if !ok {
		m.logger.Error("ttl resolve rule not found", "rule", card.RuleName, "alert_id", alertID)
		return nil
	}
	m.seedExternalRefs(alertID, card.ExternalRefs)

	if notifyErr := m.dispatchNotification(ctx, rule, domain.Notification{
		AlertID:     card.AlertID,
		ShortID:     shortAlertID(card.AlertID),
		RuleName:    card.RuleName,
		Var:         card.Var,
		Tags:        card.Tags,
		State:       domain.AlertStateResolved,
		Message:     m.notificationMessage(domain.AlertStateResolved, reason),
		MetricValue: card.MetricValue,
		StartedAt:   startedAt(card.RaisedAt, closedAt),
		Duration:    alertDuration(card.RaisedAt, closedAt),
		Timestamp:   closedAt,
	}); notifyErr != nil {
		m.logger.Error("resolved ttl notification failed", "alert_id", alertID, "error", notifyErr.Error())
	}

	_ = m.store.DeleteCard(ctx, alertID)
	m.engine.RemoveAlertState(alertID)
	return nil
}

// ApplyConfig atomically replaces active config and cleans removed rule states.
// Params: validated new config snapshot.
// Returns: cleanup error if stale states cannot be finalized.
func (m *Manager) ApplyConfig(ctx context.Context, cfg config.Config) error {
	m.mu.Lock()
	oldRules := make(map[string]config.RuleConfig, len(m.rules))
	for key, value := range m.rules {
		oldRules[key] = value
	}

	newRules, newSorted := buildRuleIndex(cfg.Rule)
	m.cfg = cfg
	m.rules = newRules
	m.sorted = newSorted
	m.mu.Unlock()

	for ruleName, oldRule := range oldRules {
		if _, ok := newRules[ruleName]; ok {
			continue
		}
		if err := m.cleanupRemovedRule(ctx, oldRule); err != nil {
			return err
		}
	}

	return nil
}

// cleanupRemovedRule resolves and removes cards of deleted rule namespace.
// Params: removed rule definition.
// Returns: cleanup error.
func (m *Manager) cleanupRemovedRule(ctx context.Context, removedRule config.RuleConfig) error {
	ids, err := m.store.ListAlertIDsByRule(ctx, removedRule.Name)
	if err != nil {
		return err
	}
	now := m.clock.Now()
	for _, alertID := range ids {
		card, _, err := m.store.GetCard(ctx, alertID)
		if err != nil {
			continue
		}
		if card.State == domain.AlertStatePending || card.State == domain.AlertStateFiring {
			m.seedExternalRefs(alertID, card.ExternalRefs)
			if card.RaisedAt.IsZero() {
				card.RaisedAt = card.LastSeenAt
				if card.RaisedAt.IsZero() {
					card.RaisedAt = now
				}
			}
			_ = m.dispatchNotification(ctx, removedRule, domain.Notification{
				AlertID:     card.AlertID,
				ShortID:     shortAlertID(card.AlertID),
				RuleName:    card.RuleName,
				Var:         card.Var,
				Tags:        card.Tags,
				State:       domain.AlertStateResolved,
				Message:     "resolved due rule removal",
				MetricValue: card.MetricValue,
				StartedAt:   startedAt(card.RaisedAt, now),
				Duration:    alertDuration(card.RaisedAt, now),
				Timestamp:   now,
			})
		}
		_ = m.store.DeleteCard(ctx, alertID)
		m.engine.RemoveAlertState(alertID)
	}
	return nil
}

// applyDecision persists state transition and emits notifications.
// Params: rule, alert ID, engine decision, and current time.
// Returns: backend error from store or notifier.
func (m *Manager) applyDecision(ctx context.Context, rule config.RuleConfig, alertID string, decision domain.Decision, now time.Time) error {
	if !decision.ShouldStore && !decision.ShouldNotify {
		return nil
	}

	identity, ok := m.engine.GetAlertIdentity(alertID)
	if !ok {
		return nil
	}
	metricValue := identity.MetricValue
	var raisedAt time.Time
	var cardSnapshot domain.AlertCard
	cardLoaded := false

	if decision.ShouldStore && decision.State != domain.AlertStateResolved {
		ttl := config.ResolveTimeout(rule)
		if err := m.store.RefreshTick(ctx, alertID, now, ttl); err != nil {
			return fmt.Errorf("refresh tick: %w", err)
		}
	}

	shouldNotify := decision.ShouldNotify

	if decision.StateChanged {
		card, revision, err := m.store.GetCard(ctx, alertID)
		if err != nil {
			if !errors.Is(err, state.ErrNotFound) {
				return err
			}
			card = domain.AlertCard{
				AlertID:     alertID,
				RuleName:    rule.Name,
				Var:         identity.Var,
				Tags:        identity.Tags,
				MetricValue: metricValue,
				State:       decision.State,
				RaisedAt:    now,
				LastSeenAt:  now,
			}
			applyCardStateTransition(&card, decision.State, decision.ResolveCause, now)
			if _, putErr := m.store.PutCard(ctx, alertID, card); putErr != nil {
				return putErr
			}
		} else {
			// Redelivery-safe guard: when persisted state already equals the target state,
			// do not emit duplicated transition notifications.
			if card.State == decision.State {
				shouldNotify = false
			}
			card.LastSeenAt = now
			card.State = decision.State
			card.MetricValue = metricValue
			applyCardStateTransition(&card, decision.State, decision.ResolveCause, now)
			if _, updateErr := m.store.UpdateCard(ctx, alertID, revision, card); updateErr != nil && !errors.Is(updateErr, state.ErrConflict) {
				return updateErr
			}
		}
		cardSnapshot = card
		cardLoaded = true
		raisedAt = card.RaisedAt
	}
	if cardLoaded {
		m.seedExternalRefs(alertID, cardSnapshot.ExternalRefs)
	}

	if shouldNotify && !(decision.State == domain.AlertStatePending && !m.notifyOnPending(rule)) {
		notification := domain.Notification{
			AlertID:     alertID,
			ShortID:     shortAlertID(alertID),
			RuleName:    rule.Name,
			Var:         identity.Var,
			Tags:        identity.Tags,
			State:       decision.State,
			Message:     m.notificationMessage(decision.State, decision.ResolveCause),
			MetricValue: metricValue,
			StartedAt:   startedAt(raisedAt, now),
			Timestamp:   now,
		}
		if decision.State == domain.AlertStateResolved {
			notification.Duration = alertDuration(raisedAt, now)
		}
		if err := m.dispatchNotification(ctx, rule, notification); err != nil {
			return err
		}
	}

	if decision.State == domain.AlertStateResolved {
		if err := m.store.DeleteCard(ctx, alertID); err != nil {
			return err
		}
		m.engine.RemoveAlertState(alertID)
	}

	return nil
}

// sortedRulesLocked returns active rules in deterministic order.
// Params: manager read lock must be held by caller.
// Returns: sorted rule slice by name.
func (m *Manager) sortedRulesLocked() []config.RuleConfig {
	return m.sorted
}

// shouldDropOutOfOrder applies rule out-of-order guards.
// Params: rule, event, and current processing time.
// Returns: true when event must be ignored.
func (m *Manager) shouldDropOutOfOrder(rule config.RuleConfig, event domain.Event, now time.Time) bool {
	if rule.OutOfOrder.MaxLateMS > 0 {
		if now.UnixMilli()-event.DT > rule.OutOfOrder.MaxLateMS {
			return true
		}
	}
	if rule.OutOfOrder.MaxFutureSkewMS > 0 {
		if event.DT > now.UnixMilli()+rule.OutOfOrder.MaxFutureSkewMS {
			return true
		}
	}
	return false
}

// repeatEnabled resolves rule/global repeat toggle.
// Params: rule notify overrides.
// Returns: effective repeat enabled flag.
func (m *Manager) repeatEnabled(rule config.RuleConfig) bool {
	if rule.Notify.Repeat != nil {
		return *rule.Notify.Repeat
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cfg.Notify.Repeat
}

// repeatEvery resolves effective repeat interval for rule.
// Params: rule notify overrides.
// Returns: repeat duration.
func (m *Manager) repeatEvery(rule config.RuleConfig) time.Duration {
	m.mu.RLock()
	seconds := m.cfg.Notify.RepeatEverySec
	m.mu.RUnlock()
	if rule.Notify.RepeatEverySec > 0 {
		seconds = rule.Notify.RepeatEverySec
	}
	if seconds <= 0 {
		seconds = 300
	}
	return time.Duration(seconds) * time.Second
}

// notifyOnPending resolves pending notification toggle.
// Params: rule notify overrides.
// Returns: effective pending notification flag.
func (m *Manager) notifyOnPending(rule config.RuleConfig) bool {
	if rule.Notify.OnPending != nil {
		return *rule.Notify.OnPending
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cfg.Notify.OnPending
}

// notificationMessage renders simple textual message for channel payload.
// Params: state transition and optional resolve cause.
// Returns: message string for outbound payload.
func (m *Manager) notificationMessage(stateValue domain.AlertState, resolveCause string) string {
	switch stateValue {
	case domain.AlertStatePending:
		return "alert entered pending"
	case domain.AlertStateFiring:
		return "alert is firing"
	case domain.AlertStateResolved:
		if resolveCause == "" {
			return "alert resolved"
		}
		return "alert resolved: " + resolveCause
	default:
		return "alert state changed"
	}
}

// notifyRoutes sends notification using explicit rule routes.
// Params: base notification payload and route bindings (channel+template).
// Returns: channel send metadata or first send error when any route fails.
func (m *Manager) notifyRoutes(ctx context.Context, notification domain.Notification, routes []config.RuleNotifyRoute, dropTrackerWithoutRef bool) (map[string]notify.SendResult, error) {
	dispatcher := m.dispatcherSnapshot()
	if dispatcher == nil {
		return nil, nil
	}
	if len(routes) == 0 {
		return nil, nil
	}
	results := make(map[string]notify.SendResult, len(routes))
	for _, route := range routes {
		channel, templateName, ok := normalizeNotifyRoute(route)
		if !ok {
			continue
		}
		perChannel, ok := m.prepareNotificationForChannel(notification, channel)
		if !ok {
			continue
		}
		if dropTrackerWithoutRef &&
			perChannel.State == domain.AlertStateResolved &&
			(channel == config.NotifyChannelJira || channel == config.NotifyChannelYouTrack) &&
			strings.TrimSpace(perChannel.ExternalRef) == "" {
			m.logger.Warn("drop tracker resolved notification without external ref", "channel", channel, "alert_id", perChannel.AlertID)
			continue
		}
		result, err := dispatcher.Send(ctx, channel, templateName, perChannel)
		if err != nil {
			return nil, err
		}
		results[channel] = result
	}
	return results, nil
}

// dispatcherSnapshot returns current dispatcher pointer under read lock.
// Params: none.
// Returns: dispatcher reference or nil.
func (m *Manager) dispatcherSnapshot() *notify.Dispatcher {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.dispatcher
}

// queueProducerSnapshot returns current queue producer pointer under read lock.
// Params: none.
// Returns: queue producer reference or nil.
func (m *Manager) queueProducerSnapshot() notifyqueue.Producer {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.producer
}

// repeatPerChannel resolves global repeat keying mode.
// Params: none.
// Returns: true when repeat tracking is per channel.
func (m *Manager) repeatPerChannel() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cfg.Notify.RepeatPerChannel
}

// runtimeStateIdleTTL resolves idle eviction threshold for inactive runtime states.
// Params: none.
// Returns: idle TTL duration (0 disables idle eviction).
func (m *Manager) runtimeStateIdleTTL() time.Duration {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.cfg.Service.RuntimeStateIdleSec <= 0 {
		return 0
	}
	return time.Duration(m.cfg.Service.RuntimeStateIdleSec) * time.Second
}

// runtimeStateMax resolves maximum runtime-state cap.
// Params: none.
// Returns: max state count (0 disables max-cap eviction).
func (m *Manager) runtimeStateMax() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cfg.Service.RuntimeStateMax
}

// dispatchNotification sends one notification by rule routes and updates runtime send markers.
// Params: rule config, base notification payload, and optional explicit routes override.
// Returns: first channel send error.
func (m *Manager) dispatchNotification(ctx context.Context, rule config.RuleConfig, notification domain.Notification, routeOverride ...config.RuleNotifyRoute) error {
	routes := rule.Notify.Route
	if len(routeOverride) > 0 {
		routes = routeOverride
	}
	if producer := m.queueProducerSnapshot(); producer != nil {
		return m.enqueueNotificationJobs(ctx, producer, notification, routes)
	}
	results, err := m.notifyRoutes(ctx, notification, routes, false)
	if err != nil {
		return err
	}
	m.captureOpeningTelegramMessageID(notification.AlertID, notification.State, results)
	m.captureExternalRefs(notification.AlertID, results)
	m.captureLastNotified(notification.AlertID, notification.Timestamp, results)
	if notification.State == domain.AlertStateFiring {
		if err := m.persistExternalRefs(ctx, notification.AlertID, results); err != nil {
			return err
		}
	}
	return nil
}

// ProcessQueuedNotification delivers one queued notification job via dispatcher.
// Params: queued delivery job produced by manager enqueue path.
// Returns: delivery error for worker NAK/redelivery.
func (m *Manager) ProcessQueuedNotification(ctx context.Context, job notifyqueue.Job) error {
	channel, templateName, ok := normalizeNotifyRoute(config.RuleNotifyRoute{
		Channel:  job.Channel,
		Template: job.Template,
	})
	if !ok {
		return nil
	}

	notification := job.Notification
	results, err := m.notifyRoutes(ctx, notification, []config.RuleNotifyRoute{{Channel: channel, Template: templateName}}, true)
	if err != nil {
		if isPermanentNotifyError(err) {
			m.logger.Warn("drop queued notification due permanent error", "channel", channel, "alert_id", notification.AlertID, "error", err.Error())
			return nil
		}
		return err
	}

	m.captureOpeningTelegramMessageID(notification.AlertID, notification.State, results)
	m.captureExternalRefs(notification.AlertID, results)
	m.captureLastNotified(notification.AlertID, notification.Timestamp, results)
	if notification.State == domain.AlertStateFiring {
		if err := m.persistExternalRefs(ctx, notification.AlertID, results); err != nil {
			return err
		}
	}
	return nil
}

// enqueueNotificationJobs publishes per-channel delivery jobs into async queue.
// Params: queue producer, base notification payload, and route bindings.
// Returns: error only when all route enqueues fail.
func (m *Manager) enqueueNotificationJobs(ctx context.Context, producer notifyqueue.Producer, notification domain.Notification, routes []config.RuleNotifyRoute) error {
	if producer == nil || len(routes) == 0 {
		return nil
	}
	success := 0
	var firstErr error
	for _, route := range routes {
		channel, templateName, ok := normalizeNotifyRoute(route)
		if !ok {
			continue
		}
		perChannel, ok := m.prepareNotificationForChannel(notification, channel)
		if !ok {
			continue
		}
		job := notifyqueue.Job{
			ID:           notifyqueue.BuildJobID(channel, templateName, perChannel),
			Channel:      channel,
			Template:     templateName,
			Notification: perChannel,
			CreatedAt:    m.clock.Now(),
		}
		if err := producer.Enqueue(ctx, job); err != nil {
			m.logger.Error("enqueue notification failed", "channel", channel, "alert_id", perChannel.AlertID, "error", err.Error())
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		success++
	}
	if success == 0 && firstErr != nil {
		return firstErr
	}
	return nil
}

// normalizeNotifyRoute normalizes route channel/template and validates they are non-empty.
// Params: raw route config.
// Returns: normalized channel/template and validity flag.
func normalizeNotifyRoute(route config.RuleNotifyRoute) (string, string, bool) {
	channel := strings.ToLower(strings.TrimSpace(route.Channel))
	templateName := strings.TrimSpace(route.Template)
	if channel == "" || templateName == "" {
		return "", "", false
	}
	return channel, templateName, true
}

// prepareNotificationForChannel enriches notification payload with channel-specific runtime metadata.
// Params: base notification and destination channel.
// Returns: channel-specific notification and send eligibility flag.
func (m *Manager) prepareNotificationForChannel(notification domain.Notification, channel string) (domain.Notification, bool) {
	perChannel := notification
	perChannel.Channel = channel
	if perChannel.Timestamp.IsZero() {
		perChannel.Timestamp = m.clock.Now()
	}
	if strings.TrimSpace(perChannel.ExternalRef) == "" {
		if externalRef, exists := m.engine.GetChannelExternalRef(perChannel.AlertID, channel); exists {
			perChannel.ExternalRef = externalRef
		}
	}
	if perChannel.State == domain.AlertStateResolved &&
		channel == config.NotifyChannelMattermost &&
		strings.TrimSpace(perChannel.ExternalRef) == "" {
		m.logger.Warn("skip mattermost resolved notification without firing reference", "channel", channel, "alert_id", perChannel.AlertID)
		return domain.Notification{}, false
	}
	if perChannel.State == domain.AlertStateResolved && channel == config.NotifyChannelTelegram {
		if openingMessageID, exists := m.engine.GetChannelMessageID(perChannel.AlertID, channel); exists {
			perChannel.ReplyToMessageID = &openingMessageID
		}
	}
	return perChannel, true
}

// isPermanentNotifyError classifies delivery errors that should not be retried.
// Params: dispatcher send error.
// Returns: true when retry cannot recover (template/config mismatch).
func isPermanentNotifyError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "notify template") && strings.Contains(message, "not configured") ||
		strings.Contains(message, "notify template") && strings.Contains(message, "is invalid") ||
		strings.Contains(message, "notify channel") && strings.Contains(message, "not configured")
}

// ruleByName resolves one active rule by name under read lock.
// Params: rule name from alert card.
// Returns: rule definition and existence flag.
func (m *Manager) ruleByName(name string) (config.RuleConfig, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	rule, ok := m.rules[name]
	return rule, ok
}

// captureOpeningTelegramMessageID stores first Telegram message id for alert lifecycle threading.
// Params: alert id, notification state, and per-channel send results.
// Returns: runtime state updated only for first active Telegram message.
func (m *Manager) captureOpeningTelegramMessageID(alertID string, stateValue domain.AlertState, results map[string]notify.SendResult) {
	if stateValue != domain.AlertStatePending && stateValue != domain.AlertStateFiring {
		return
	}
	result, ok := results[config.NotifyChannelTelegram]
	if !ok || result.MessageID <= 0 {
		return
	}
	_, exists := m.engine.GetChannelMessageID(alertID, config.NotifyChannelTelegram)
	if exists {
		return
	}
	m.engine.SetChannelMessageID(alertID, config.NotifyChannelTelegram, result.MessageID)
}

// captureExternalRefs stores external issue references from send results in runtime state.
// Params: alert id and per-channel send results.
// Returns: runtime external refs updated for channels that returned non-empty refs.
func (m *Manager) captureExternalRefs(alertID string, results map[string]notify.SendResult) {
	if len(results) == 0 {
		return
	}
	for channel, result := range results {
		externalRef := strings.TrimSpace(result.ExternalRef)
		if externalRef == "" {
			continue
		}
		m.engine.SetChannelExternalRef(alertID, channel, externalRef)
	}
}

// seedExternalRefs copies persisted channel refs into runtime state.
// Params: alert id and persisted external refs map from card.
// Returns: runtime external refs seeded for known channels.
func (m *Manager) seedExternalRefs(alertID string, refs map[string]string) {
	if len(refs) == 0 {
		return
	}
	for channel, externalRef := range refs {
		value := strings.TrimSpace(externalRef)
		if value == "" {
			continue
		}
		m.engine.SetChannelExternalRef(alertID, channel, value)
	}
}

// persistExternalRefs stores external issue references in alert card for restart-safe resolve path.
// Params: alert id and per-channel send results with external refs.
// Returns: persistence error when card update fails.
func (m *Manager) persistExternalRefs(ctx context.Context, alertID string, results map[string]notify.SendResult) error {
	if len(results) == 0 {
		return nil
	}
	updates := make(map[string]string)
	for channel, result := range results {
		externalRef := strings.TrimSpace(result.ExternalRef)
		if externalRef == "" {
			continue
		}
		updates[channel] = externalRef
	}
	if len(updates) == 0 {
		return nil
	}

	for attempt := 0; attempt < 3; attempt++ {
		card, revision, err := m.store.GetCard(ctx, alertID)
		if err != nil {
			if errors.Is(err, state.ErrNotFound) {
				return nil
			}
			return err
		}
		if card.ExternalRefs == nil {
			card.ExternalRefs = make(map[string]string, len(updates))
		}

		changed := false
		for channel, externalRef := range updates {
			if card.ExternalRefs[channel] == externalRef {
				continue
			}
			card.ExternalRefs[channel] = externalRef
			changed = true
		}
		if !changed {
			return nil
		}
		if _, err := m.store.UpdateCard(ctx, alertID, revision, card); err != nil {
			if errors.Is(err, state.ErrConflict) {
				continue
			}
			return err
		}
		return nil
	}

	return fmt.Errorf("persist external refs conflict retries exceeded for %s", alertID)
}

// captureLastNotified stores notification timestamp for repeat scheduler keys.
// Params: alert id, send time, and sent-channel results.
// Returns: runtime state markers updated for delivered channels.
func (m *Manager) captureLastNotified(alertID string, now time.Time, results map[string]notify.SendResult) {
	if len(results) == 0 {
		return
	}
	repeatPerChannel := m.repeatPerChannel()
	for channel := range results {
		notifyKey := channel
		if !repeatPerChannel {
			notifyKey = "global"
		}
		m.engine.SetLastNotified(alertID, notifyKey, now)
	}
}

// applyCardStateTransition updates state-specific card fields for pending/firing/resolved.
// Params: mutable card pointer, target state, optional resolve cause, and transition time.
// Returns: card updated in place.
func applyCardStateTransition(card *domain.AlertCard, stateValue domain.AlertState, resolveCause string, now time.Time) {
	switch stateValue {
	case domain.AlertStatePending:
		pendingSince := now
		card.PendingSince = &pendingSince
		card.ClosedAt = nil
		card.ResolveCause = ""
	case domain.AlertStateFiring:
		card.PendingSince = nil
		card.ClosedAt = nil
		card.ResolveCause = ""
	case domain.AlertStateResolved:
		closedAt := now
		card.ClosedAt = &closedAt
		card.ResolveCause = resolveCause
	}
}

// shortAlertID builds compact human-friendly alert identifier.
// Params: full alert id path.
// Returns: short id (last segment, first 5 chars).
func shortAlertID(alertID string) string {
	trimmed := strings.TrimSpace(alertID)
	if trimmed == "" {
		return ""
	}
	short := trimmed
	if idx := strings.LastIndex(trimmed, "/"); idx >= 0 && idx < len(trimmed)-1 {
		short = trimmed[idx+1:]
	}
	if len(short) > 5 {
		short = short[:5]
	}
	return short
}

// startedAt returns a non-nil start timestamp pointer for template rendering.
// Params: primary timestamp and fallback timestamp.
// Returns: non-nil pointer to primary/fallback timestamp.
func startedAt(value, fallback time.Time) *time.Time {
	started := value
	if started.IsZero() {
		started = fallback
	}
	if started.IsZero() {
		started = time.UnixMilli(0).UTC()
	}
	return &started
}

// alertDuration computes elapsed time between start and end.
// Params: start and end timestamps.
// Returns: duration or zero for invalid range.
func alertDuration(start, end time.Time) time.Duration {
	if start.IsZero() || end.Before(start) {
		return 0
	}
	return end.Sub(start)
}

// buildRuleIndex creates by-name lookup and sorted immutable rule slice.
// Params: decoded rule list from active snapshot.
// Returns: by-name map and sorted slice for hot-path reads.
func buildRuleIndex(input []config.RuleConfig) (map[string]config.RuleConfig, []config.RuleConfig) {
	byName := make(map[string]config.RuleConfig, len(input))
	sorted := make([]config.RuleConfig, 0, len(input))
	for _, rule := range input {
		normalized := normalizeRule(rule)
		byName[normalized.Name] = normalized
		sorted = append(sorted, normalized)
	}
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Name < sorted[j].Name
	})
	return byName, sorted
}

// normalizeRule canonicalizes rule fragments used in per-event hot path.
// Params: one decoded rule config.
// Returns: normalized copy with stable key-tag order.
func normalizeRule(rule config.RuleConfig) config.RuleConfig {
	if len(rule.Key.FromTags) > 1 && !sort.StringsAreSorted(rule.Key.FromTags) {
		sortedTags := append([]string(nil), rule.Key.FromTags...)
		sort.Strings(sortedTags)
		rule.Key.FromTags = sortedTags
	}
	if len(rule.Notify.Route) > 0 {
		normalizedRoutes := make([]config.RuleNotifyRoute, 0, len(rule.Notify.Route))
		for _, route := range rule.Notify.Route {
			channel := strings.ToLower(strings.TrimSpace(route.Channel))
			templateName := strings.TrimSpace(route.Template)
			if channel == "" || templateName == "" {
				continue
			}
			normalizedRoutes = append(normalizedRoutes, config.RuleNotifyRoute{
				Channel:  channel,
				Template: templateName,
			})
		}
		rule.Notify.Route = normalizedRoutes
	}
	return rule
}
