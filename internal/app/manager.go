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

type tickRefresh struct {
	lastSeen time.Time
	ttl      time.Duration
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
	return m.processEvent(context.Background(), event, nil)
}

// PushBatch processes batch of incoming events from ingest interfaces.
// Params: validated incoming event slice.
// Returns: processing error when backend operation fails.
func (m *Manager) PushBatch(events []domain.Event) error {
	ctx := context.Background()
	pendingTickRefresh := make(map[string]tickRefresh)
	for _, event := range events {
		if err := m.processEvent(ctx, event, pendingTickRefresh); err != nil {
			return err
		}
	}
	if err := m.flushTickRefresh(ctx, pendingTickRefresh); err != nil {
		return err
	}
	return nil
}

// ProcessEvent runs all rule evaluations for one event.
// Params: context and validated event payload.
// Returns: first backend error on store/notify failure.
func (m *Manager) ProcessEvent(ctx context.Context, event domain.Event) error {
	return m.processEvent(ctx, event, nil)
}

func (m *Manager) processEvent(ctx context.Context, event domain.Event, pendingTickRefresh map[string]tickRefresh) error {
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
		if err := m.applyDecision(ctx, rule, alertID, decision, now, pendingTickRefresh); err != nil {
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
			if err := m.applyDecision(ctx, rule, decision.AlertID, decision, now, nil); err != nil {
				return err
			}
		}

		repeatEnabled := m.repeatEnabled(rule)
		repeatEvery := m.repeatEvery(rule)
		routes := rule.Notify.Route
		for _, alertID := range firingAlertIDs {
			repeatSnapshot, exists := m.engine.GetRepeatSnapshot(alertID)
			if !exists {
				continue
			}
			if !repeatEnabled || len(routes) == 0 {
				continue
			}
			notification := buildNotification(
				alertID,
				rule.Name,
				repeatSnapshot.Var,
				repeatSnapshot.Tags,
				repeatSnapshot.MetricValue,
				domain.AlertStateFiring,
				"firing repeat",
				now,
				repeatSnapshot.RaisedAt,
			)

			if !repeatPerChannel {
				last := repeatSnapshot.LastNotified["global"]
				if !last.IsZero() && now.Sub(last) < repeatEvery {
					continue
				}
				if err := m.dispatchNotification(ctx, rule, notification); err != nil {
					m.logger.Error("repeat notification failed", "rule", rule.Name, "alert_id", alertID, "error", err.Error())
				}
				continue
			}

			for _, route := range routes {
				normalizedRoute, ok := normalizedRouteFromRuntimeRoute(route)
				if !ok {
					continue
				}
				notifyKey := normalizedRoute.Key
				last := repeatSnapshot.LastNotified[notifyKey]
				if !last.IsZero() && now.Sub(last) < repeatEvery {
					continue
				}
				if err := m.dispatchNotification(ctx, rule, notification, route); err != nil {
					m.logger.Error("repeat notification failed", "rule", rule.Name, "alert_id", alertID, "error", err.Error())
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
			notification := buildNotification(
				card.AlertID,
				card.RuleName,
				card.Var,
				card.Tags,
				card.MetricValue,
				domain.AlertStateResolved,
				"resolved due rule removal",
				now,
				card.RaisedAt,
			)
			notification.Duration = alertDuration(card.RaisedAt, now)
			_ = m.dispatchNotification(ctx, removedRule, notification)
		}
		_ = m.store.DeleteCard(ctx, alertID)
		m.engine.RemoveAlertState(alertID)
	}
	return nil
}

// applyDecision persists state transition and emits notifications.
// Params: rule, alert ID, engine decision, and current time.
// Returns: backend error from store or notifier.
func (m *Manager) applyDecision(ctx context.Context, rule config.RuleConfig, alertID string, decision domain.Decision, now time.Time, pendingTickRefresh map[string]tickRefresh) error {
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
		if pendingTickRefresh == nil {
			if err := m.store.RefreshTick(ctx, alertID, now, ttl); err != nil {
				return fmt.Errorf("refresh tick: %w", err)
			}
		} else {
			if prev, exists := pendingTickRefresh[alertID]; !exists || prev.lastSeen.Before(now) {
				pendingTickRefresh[alertID] = tickRefresh{lastSeen: now, ttl: ttl}
			}
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
		notification := buildNotification(
			alertID,
			rule.Name,
			identity.Var,
			identity.Tags,
			metricValue,
			decision.State,
			m.notificationMessage(decision.State, decision.ResolveCause),
			now,
			raisedAt,
		)
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

func (m *Manager) flushTickRefresh(ctx context.Context, pendingTickRefresh map[string]tickRefresh) error {
	for alertID, tick := range pendingTickRefresh {
		if err := m.store.RefreshTick(ctx, alertID, tick.lastSeen, tick.ttl); err != nil {
			return fmt.Errorf("refresh tick: %w", err)
		}
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

// normalizedNotifyRoute stores canonical route metadata used by runtime.
// Params: normalized route fields from config.
// Returns: deterministic route descriptor.
type normalizedNotifyRoute struct {
	Key      string
	Channel  string
	Template string
	Mode     string
}

// routeSendResult keeps route context with channel send result.
// Params: route metadata and transport result.
// Returns: one delivery outcome item.
type routeSendResult struct {
	Route  normalizedNotifyRoute
	Result notify.SendResult
}

// notifyRoutes sends notification using explicit rule routes.
// Params: base notification payload and route bindings (channel+template).
// Returns: channel send metadata or first send error when any route fails.
func (m *Manager) notifyRoutes(ctx context.Context, notification domain.Notification, routes []config.RuleNotifyRoute, dropTrackerWithoutRef bool) ([]routeSendResult, error) {
	dispatcher := m.dispatcherSnapshot()
	if dispatcher == nil {
		return nil, nil
	}
	if len(routes) == 0 {
		return nil, nil
	}
	results := make([]routeSendResult, 0, len(routes))
	for _, route := range routes {
		normalizedRoute, ok := normalizedRouteFromRuntimeRoute(route)
		if !ok {
			continue
		}
		perRoute, ok := m.prepareNotificationForRoute(notification, normalizedRoute)
		if !ok {
			continue
		}
		if dropTrackerWithoutRef &&
			perRoute.State == domain.AlertStateResolved &&
			(normalizedRoute.Channel == config.NotifyChannelJira || normalizedRoute.Channel == config.NotifyChannelYouTrack) &&
			strings.TrimSpace(perRoute.ExternalRef) == "" {
			m.logger.Warn("drop tracker resolved notification without external ref", "channel", normalizedRoute.Channel, "alert_id", perRoute.AlertID)
			continue
		}
		result, err := dispatcher.Send(ctx, normalizedRoute.Channel, normalizedRoute.Template, perRoute)
		if err != nil {
			return nil, err
		}
		results = append(results, routeSendResult{
			Route:  normalizedRoute,
			Result: result,
		})
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
	return m.applyDeliveryResults(ctx, notification, results)
}

// ProcessQueuedNotification delivers one queued notification job via dispatcher.
// Params: queued delivery job produced by manager enqueue path.
// Returns: delivery error for worker NAK/redelivery.
func (m *Manager) ProcessQueuedNotification(ctx context.Context, job notifyqueue.Job) error {
	normalizedRoute, ok := normalizedRouteFromRuntimeRoute(config.RuleNotifyRoute{
		Name:     job.RouteKey,
		Channel:  job.Channel,
		Template: job.Template,
		Mode:     job.RouteMode,
	})
	if !ok {
		return nil
	}

	notification := job.Notification
	results, err := m.notifyRoutes(ctx, notification, []config.RuleNotifyRoute{{
		Name:     normalizedRoute.Key,
		Channel:  normalizedRoute.Channel,
		Template: normalizedRoute.Template,
		Mode:     normalizedRoute.Mode,
	}}, true)
	if err != nil {
		if isPermanentNotifyError(err) {
			m.logger.Warn("drop queued notification due permanent error", "channel", normalizedRoute.Channel, "alert_id", notification.AlertID, "error", err.Error())
			return notifyqueue.MarkPermanent(err)
		}
		return err
	}

	return m.applyDeliveryResults(ctx, notification, results)
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
		normalizedRoute, ok := normalizedRouteFromRuntimeRoute(route)
		if !ok {
			continue
		}
		perRoute, ok := m.prepareNotificationForRoute(notification, normalizedRoute)
		if !ok {
			continue
		}
		job := notifyqueue.Job{
			ID:           notifyqueue.BuildJobID(normalizedRoute.Channel, normalizedRoute.Template, perRoute),
			RouteKey:     normalizedRoute.Key,
			RouteMode:    normalizedRoute.Mode,
			Channel:      normalizedRoute.Channel,
			Template:     normalizedRoute.Template,
			Notification: perRoute,
			CreatedAt:    m.clock.Now(),
		}
		if err := producer.Enqueue(ctx, job); err != nil {
			m.logger.Error("enqueue notification failed", "channel", normalizedRoute.Channel, "route_key", normalizedRoute.Key, "alert_id", perRoute.AlertID, "error", err.Error())
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

// normalizeNotifyRoute normalizes route fields and validates required values.
// Params: raw route config.
// Returns: normalized route descriptor and validity flag.
func normalizeNotifyRoute(route config.RuleNotifyRoute) (normalizedNotifyRoute, bool) {
	channel := strings.ToLower(strings.TrimSpace(route.Channel))
	templateName := strings.TrimSpace(route.Template)
	if channel == "" || templateName == "" {
		return normalizedNotifyRoute{}, false
	}
	routeKey := strings.ToLower(strings.TrimSpace(route.Name))
	if routeKey == "" {
		routeKey = channel
	}
	mode := config.NormalizeNotifyRouteMode(route.Mode)
	return normalizedNotifyRoute{
		Key:      routeKey,
		Channel:  channel,
		Template: templateName,
		Mode:     mode,
	}, true
}

// normalizedRouteFromRuntimeRoute converts already-normalized runtime route into descriptor.
// Params: route config from in-memory normalized rule set.
// Returns: route descriptor and validity flag for required fields.
func normalizedRouteFromRuntimeRoute(route config.RuleNotifyRoute) (normalizedNotifyRoute, bool) {
	if route.Channel == "" || route.Template == "" {
		return normalizedNotifyRoute{}, false
	}
	routeKey := route.Name
	if routeKey == "" {
		routeKey = route.Channel
	}
	mode := route.Mode
	if mode == "" {
		mode = config.NotifyRouteModeHistory
	}
	return normalizedNotifyRoute{
		Key:      routeKey,
		Channel:  route.Channel,
		Template: route.Template,
		Mode:     mode,
	}, true
}

// prepareNotificationForRoute enriches notification payload with route-scoped runtime metadata.
// Params: base notification and normalized destination route.
// Returns: route-specific notification and send eligibility flag.
func (m *Manager) prepareNotificationForRoute(notification domain.Notification, route normalizedNotifyRoute) (domain.Notification, bool) {
	perRoute := notification
	perRoute.Channel = route.Channel
	perRoute.RouteKey = route.Key
	perRoute.RouteMode = route.Mode
	if perRoute.Timestamp.IsZero() {
		perRoute.Timestamp = m.clock.Now()
	}
	if strings.TrimSpace(perRoute.ExternalRef) == "" {
		if externalRef, exists := m.engine.GetChannelExternalRef(perRoute.AlertID, route.Key); exists {
			perRoute.ExternalRef = externalRef
		}
	}

	if perRoute.State == domain.AlertStateResolved {
		switch route.Mode {
		case config.NotifyRouteModeActiveOnly:
			if strings.TrimSpace(perRoute.ExternalRef) == "" {
				m.logger.Warn("skip active_only resolved notification without active reference", "channel", route.Channel, "route_key", route.Key, "alert_id", perRoute.AlertID)
				return domain.Notification{}, false
			}
		default:
			if route.Channel == config.NotifyChannelMattermost && strings.TrimSpace(perRoute.ExternalRef) == "" {
				m.logger.Warn("skip mattermost resolved notification without firing reference", "channel", route.Channel, "route_key", route.Key, "alert_id", perRoute.AlertID)
				return domain.Notification{}, false
			}
			if route.Channel == config.NotifyChannelTelegram {
				if openingMessageID, exists := m.engine.GetChannelMessageID(perRoute.AlertID, route.Key); exists {
					perRoute.ReplyToMessageID = &openingMessageID
				}
			}
		}
	}
	return perRoute, true
}

// isPermanentNotifyError classifies delivery errors that should not be retried.
// Params: dispatcher send error.
// Returns: true when retry cannot recover (template/config mismatch).
func isPermanentNotifyError(err error) bool {
	return notify.IsPermanent(err)
}

// applyDeliveryResults persists channel delivery side effects after successful send.
// Params: notification payload and per-route send results.
// Returns: persistence error for external refs only.
func (m *Manager) applyDeliveryResults(ctx context.Context, notification domain.Notification, results []routeSendResult) error {
	m.captureOpeningTelegramMessageID(notification.AlertID, notification.State, results)
	m.captureExternalRefs(notification.AlertID, results)
	m.captureLastNotified(notification.AlertID, notification.Timestamp, results)
	if notification.State == domain.AlertStateResolved {
		return nil
	}
	return m.persistExternalRefs(ctx, notification.AlertID, results)
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
func (m *Manager) captureOpeningTelegramMessageID(alertID string, stateValue domain.AlertState, results []routeSendResult) {
	if stateValue != domain.AlertStatePending && stateValue != domain.AlertStateFiring {
		return
	}
	for _, entry := range results {
		if entry.Route.Channel != config.NotifyChannelTelegram {
			continue
		}
		if entry.Route.Mode == config.NotifyRouteModeActiveOnly {
			continue
		}
		if entry.Result.MessageID <= 0 {
			continue
		}
		_, exists := m.engine.GetChannelMessageID(alertID, entry.Route.Key)
		if exists {
			continue
		}
		m.engine.SetChannelMessageID(alertID, entry.Route.Key, entry.Result.MessageID)
	}
}

// captureExternalRefs stores external issue references from send results in runtime state.
// Params: alert id and per-channel send results.
// Returns: runtime external refs updated for channels that returned non-empty refs.
func (m *Manager) captureExternalRefs(alertID string, results []routeSendResult) {
	if len(results) == 0 {
		return
	}
	for _, entry := range results {
		externalRef := strings.TrimSpace(entry.Result.ExternalRef)
		if externalRef == "" {
			continue
		}
		m.engine.SetChannelExternalRef(alertID, entry.Route.Key, externalRef)
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
func (m *Manager) persistExternalRefs(ctx context.Context, alertID string, results []routeSendResult) error {
	if len(results) == 0 {
		return nil
	}
	updates := make(map[string]string)
	for _, entry := range results {
		externalRef := strings.TrimSpace(entry.Result.ExternalRef)
		if externalRef == "" {
			continue
		}
		updates[entry.Route.Key] = externalRef
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
		for routeKey, externalRef := range updates {
			if card.ExternalRefs[routeKey] == externalRef {
				continue
			}
			card.ExternalRefs[routeKey] = externalRef
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
func (m *Manager) captureLastNotified(alertID string, now time.Time, results []routeSendResult) {
	if len(results) == 0 {
		return
	}
	repeatPerChannel := m.repeatPerChannel()
	for _, entry := range results {
		notifyKey := entry.Route.Key
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

// buildNotification assembles base notification payload with consistent fields.
// Params: alert identity, state, message, timestamps.
// Returns: populated notification payload.
func buildNotification(alertID, ruleName, varName string, tags map[string]string, metricValue string, state domain.AlertState, message string, now time.Time, raisedAt time.Time) domain.Notification {
	return domain.Notification{
		AlertID:     alertID,
		ShortID:     shortAlertID(alertID),
		RuleName:    ruleName,
		Var:         varName,
		Tags:        tags,
		State:       state,
		Message:     message,
		MetricValue: metricValue,
		StartedAt:   startedAt(raisedAt, now),
		Timestamp:   now,
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
			normalizedRoute, ok := normalizeNotifyRoute(route)
			if !ok {
				continue
			}
			normalizedRoutes = append(normalizedRoutes, config.RuleNotifyRoute{
				Name:     normalizedRoute.Key,
				Channel:  normalizedRoute.Channel,
				Template: normalizedRoute.Template,
				Mode:     normalizedRoute.Mode,
			})
		}
		rule.Notify.Route = normalizedRoutes
	}
	return rule
}
