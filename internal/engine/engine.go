package engine

import (
	"sort"
	"strconv"
	"sync"
	"time"

	"alerting/internal/config"
	"alerting/internal/domain"
)

// RuntimeState stores per-alert mutable evaluation state.
// Params: counters, time windows, and lifecycle markers.
// Returns: mutable state used by rule engine.
type RuntimeState struct {
	RuleName        string
	AlertID         string
	Var             string
	Tags            map[string]string
	MetricValue     string
	RaisedAt        time.Time
	Initialized     bool
	LastSeen        time.Time
	PendingSince    *time.Time
	HysteresisSince *time.Time
	CurrentState    domain.AlertState
	CountTotal      int64
	CountWindow     []WindowPoint
	CountWindowSum  int64
	LastNotified    map[string]time.Time
	ChannelRefs     map[string]int
	ExternalRefs    map[string]string
}

// AlertIdentity stores immutable alert dimensions used in notifications/card writes.
// Params: variable name and immutable tags map.
// Returns: minimal identity snapshot for hot path.
type AlertIdentity struct {
	Var         string
	Tags        map[string]string
	MetricValue string
}

// RepeatSnapshot stores minimal runtime fields for repeat notifications.
// Params: immutable alert identity and repeat timing markers.
// Returns: lightweight snapshot for repeat scheduling.
type RepeatSnapshot struct {
	AlertID      string
	RuleName     string
	Var          string
	Tags         map[string]string
	MetricValue  string
	RaisedAt     time.Time
	LastNotified map[string]time.Time
}

// WindowPoint stores one count contribution in sliding window.
// Params: event processing time and event contribution count.
// Returns: one window sample for count_window rule.
type WindowPoint struct {
	At    time.Time
	Count int64
}

// Engine evaluates events and periodic ticks into alert decisions.
// Params: in-memory state map keyed by alert ID.
// Returns: deterministic decision stream for manager.
type Engine struct {
	mu     sync.RWMutex
	states map[string]*RuntimeState
}

// New constructs rule engine with empty runtime state.
// Params: none.
// Returns: initialized engine instance.
func New() *Engine {
	return &Engine{states: make(map[string]*RuntimeState)}
}

// ProcessEvent evaluates one rule against one event.
// Params: rule, event, alert ID, and current processing time.
// Returns: rule decision and state mutation side effects.
func (e *Engine) ProcessEvent(rule config.RuleConfig, event domain.Event, alertID string, now time.Time) domain.Decision {
	e.mu.Lock()
	defer e.mu.Unlock()

	state := e.ensureState(rule, event, alertID)
	state.LastSeen = now
	state.MetricValue = metricValue(event.Value)

	switch rule.AlertType {
	case "count_total":
		state.Initialized = true
		state.CountTotal += event.AggCnt
		return e.applyActiveCondition(state, rule, now, state.CountTotal >= int64(rule.Raise.N), false, "")
	case "count_window":
		state.Initialized = true
		state.CountWindow = append(state.CountWindow, WindowPoint{At: now, Count: event.AggCnt})
		state.CountWindowSum += event.AggCnt
		state.CountWindow, state.CountWindowSum = pruneWindow(state.CountWindow, now, time.Duration(rule.Raise.WindowSec)*time.Second, state.CountWindowSum)
		return e.applyActiveCondition(state, rule, now, state.CountWindowSum >= int64(rule.Raise.N), false, "")
	case "missing_heartbeat":
		if !state.Initialized {
			state.Initialized = true
			state.CurrentState = ""
			state.PendingSince = nil
			state.HysteresisSince = nil
			return domain.Decision{Matched: true, AlertID: alertID}
		}
		if state.CurrentState == domain.AlertStatePending || state.CurrentState == domain.AlertStateFiring {
			hysteresisWindow := time.Duration(rule.Resolve.HysteresisSec) * time.Second
			if hysteresisWindow > 0 {
				if state.HysteresisSince == nil {
					hysteresisSince := now
					state.HysteresisSince = &hysteresisSince
				}
				if now.Sub(*state.HysteresisSince) < hysteresisWindow {
					return domain.Decision{
						Matched:     true,
						AlertID:     alertID,
						State:       state.CurrentState,
						ShouldStore: true,
					}
				}
			}
			decision := domain.Decision{
				Matched:          true,
				AlertID:          alertID,
				State:            domain.AlertStateResolved,
				StateChanged:     true,
				ShouldStore:      true,
				ShouldNotify:     true,
				ResolveImmediate: true,
				ResolveCause:     "heartbeat_recovered",
			}
			resetResolvedState(state)
			return decision
		}
		return domain.Decision{Matched: true, AlertID: alertID}
	default:
		return domain.Decision{}
	}
}

// TickRule evaluates no-event transitions for one rule.
// Params: rule and current processing time.
// Returns: decisions for pending/firing/resolved transitions.
func (e *Engine) TickRule(rule config.RuleConfig, now time.Time) []domain.Decision {
	decisions, _ := e.TickRuleWithFiring(rule, now)
	return decisions
}

// TickRuleWithFiring evaluates no-event transitions and returns active firing alert IDs.
// Params: rule and current processing time.
// Returns: decisions and firing alert IDs after in-memory state transition.
func (e *Engine) TickRuleWithFiring(rule config.RuleConfig, now time.Time) ([]domain.Decision, []string) {
	e.mu.Lock()
	defer e.mu.Unlock()

	decisions := make([]domain.Decision, 0)
	firingAlertIDs := make([]string, 0)
	for _, state := range e.states {
		if state.RuleName != rule.Name {
			continue
		}

		switch rule.AlertType {
		case "count_total", "count_window":
			if state.CurrentState != domain.AlertStatePending && state.CurrentState != domain.AlertStateFiring {
				continue
			}
			resolveAfter := time.Duration(rule.Resolve.SilenceSec+rule.Resolve.HysteresisSec) * time.Second
			if now.Sub(state.LastSeen) >= resolveAfter {
				decision := domain.Decision{
					Matched:      true,
					AlertID:      state.AlertID,
					State:        domain.AlertStateResolved,
					StateChanged: true,
					ShouldStore:  true,
					ShouldNotify: true,
					ResolveCause: "silence_timeout",
				}
				resetResolvedState(state)
				decisions = append(decisions, decision)
			}
		case "missing_heartbeat":
			if !state.Initialized {
				continue
			}
			active := now.Sub(state.LastSeen) >= time.Duration(rule.Raise.MissingSec)*time.Second
			if active {
				state.HysteresisSince = nil
			}
			decision := e.applyActiveCondition(state, rule, now, active, true, "")
			if decision.Matched {
				decisions = append(decisions, decision)
			}
			if active || (state.CurrentState != domain.AlertStatePending && state.CurrentState != domain.AlertStateFiring) {
				continue
			}

			hysteresisWindow := time.Duration(rule.Resolve.HysteresisSec) * time.Second
			if hysteresisWindow <= 0 {
				continue
			}
			if state.HysteresisSince == nil {
				hysteresisSince := state.LastSeen
				if hysteresisSince.IsZero() {
					hysteresisSince = now
				}
				state.HysteresisSince = &hysteresisSince
			}
			if now.Sub(*state.HysteresisSince) >= hysteresisWindow {
				decisions = append(decisions, domain.Decision{
					Matched:      true,
					AlertID:      state.AlertID,
					State:        domain.AlertStateResolved,
					StateChanged: true,
					ShouldStore:  true,
					ShouldNotify: true,
					ResolveCause: "heartbeat_recovered",
				})
				resetResolvedState(state)
				continue
			}
			decisions = append(decisions, domain.Decision{
				Matched:     true,
				AlertID:     state.AlertID,
				State:       state.CurrentState,
				ShouldStore: true,
			})
		}
		if state.CurrentState == domain.AlertStateFiring {
			firingAlertIDs = append(firingAlertIDs, state.AlertID)
		}
	}

	return decisions, firingAlertIDs
}

// RemoveAlertState removes one runtime state by alert ID.
// Params: alert ID to remove.
// Returns: state removed from engine cache.
func (e *Engine) RemoveAlertState(alertID string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.states, alertID)
}

// applyActiveCondition applies pending/firing transitions for active condition state.
// Params: runtime state, rule, current time, active flag, tick source flag, and resolve cause.
// Returns: resulting decision.
func (e *Engine) applyActiveCondition(state *RuntimeState, rule config.RuleConfig, now time.Time, active bool, fromTick bool, resolveCause string) domain.Decision {
	if !active {
		if fromTick {
			return domain.Decision{}
		}
		return domain.Decision{Matched: true, AlertID: state.AlertID}
	}

	state.HysteresisSince = nil
	hasPending := rule.Pending.Enabled
	switch state.CurrentState {
	case "":
		if hasPending {
			state.RaisedAt = now
			pendingSince := now
			state.PendingSince = &pendingSince
			state.CurrentState = domain.AlertStatePending
			return domain.Decision{
				Matched:      true,
				AlertID:      state.AlertID,
				State:        domain.AlertStatePending,
				StateChanged: true,
				ShouldStore:  true,
			}
		}
		state.RaisedAt = now
		state.CurrentState = domain.AlertStateFiring
		state.PendingSince = nil
		return domain.Decision{
			Matched:      true,
			AlertID:      state.AlertID,
			State:        domain.AlertStateFiring,
			StateChanged: true,
			ShouldStore:  true,
			ShouldNotify: true,
		}
	case domain.AlertStatePending:
		if state.PendingSince == nil {
			pendingSince := now
			state.PendingSince = &pendingSince
		}
		if now.Sub(*state.PendingSince) >= time.Duration(rule.Pending.DelaySec)*time.Second {
			state.CurrentState = domain.AlertStateFiring
			state.PendingSince = nil
			return domain.Decision{
				Matched:      true,
				AlertID:      state.AlertID,
				State:        domain.AlertStateFiring,
				StateChanged: true,
				ShouldStore:  true,
				ShouldNotify: true,
			}
		}
		return domain.Decision{Matched: true, AlertID: state.AlertID, State: domain.AlertStatePending, ShouldStore: true}
	case domain.AlertStateFiring:
		return domain.Decision{Matched: true, AlertID: state.AlertID, State: domain.AlertStateFiring, ShouldStore: true}
	case domain.AlertStateResolved:
		state.CurrentState = ""
		state.RaisedAt = time.Time{}
		return domain.Decision{Matched: true, AlertID: state.AlertID}
	default:
		_ = resolveCause
		return domain.Decision{Matched: true, AlertID: state.AlertID}
	}
}

// resetResolvedState clears runtime fields after alert resolve transition.
// Params: mutable runtime state pointer.
// Returns: state reset in place for next lifecycle.
func resetResolvedState(state *RuntimeState) {
	state.CurrentState = ""
	state.PendingSince = nil
	state.HysteresisSince = nil
	state.CountWindow = nil
	state.CountWindowSum = 0
	state.CountTotal = 0
	state.RaisedAt = time.Time{}
}

// ensureState gets or initializes runtime state for alert ID.
// Params: rule, event, and alert ID.
// Returns: mutable runtime state pointer.
func (e *Engine) ensureState(rule config.RuleConfig, event domain.Event, alertID string) *RuntimeState {
	if existing, ok := e.states[alertID]; ok {
		if existing.LastNotified == nil {
			existing.LastNotified = make(map[string]time.Time)
		}
		if existing.ChannelRefs == nil {
			existing.ChannelRefs = make(map[string]int)
		}
		if existing.ExternalRefs == nil {
			existing.ExternalRefs = make(map[string]string)
		}
		return existing
	}
	state := &RuntimeState{
		RuleName:     rule.Name,
		AlertID:      alertID,
		Var:          event.Var,
		Tags:         cloneTags(event.Tags),
		MetricValue:  metricValue(event.Value),
		LastNotified: make(map[string]time.Time),
		ChannelRefs:  make(map[string]int),
		ExternalRefs: make(map[string]string),
	}
	e.states[alertID] = state
	return state
}

// GetStateSnapshot returns one runtime state copy by alert ID.
// Params: alert ID.
// Returns: state copy and existence flag.
func (e *Engine) GetStateSnapshot(alertID string) (RuntimeState, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	state, ok := e.states[alertID]
	if !ok {
		return RuntimeState{}, false
	}
	return cloneRuntimeState(state), true
}

// GetAlertIdentity returns immutable alert identity for hot path operations.
// Params: alert ID.
// Returns: identity view and existence flag.
func (e *Engine) GetAlertIdentity(alertID string) (AlertIdentity, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	state, ok := e.states[alertID]
	if !ok {
		return AlertIdentity{}, false
	}
	return AlertIdentity{
		Var:         state.Var,
		Tags:        cloneTags(state.Tags),
		MetricValue: state.MetricValue,
	}, true
}

// GetRepeatSnapshot returns lightweight runtime snapshot for repeat notifications.
// Params: alert ID.
// Returns: snapshot and existence flag.
func (e *Engine) GetRepeatSnapshot(alertID string) (RepeatSnapshot, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	state, ok := e.states[alertID]
	if !ok {
		return RepeatSnapshot{}, false
	}
	return RepeatSnapshot{
		AlertID:      state.AlertID,
		RuleName:     state.RuleName,
		Var:          state.Var,
		Tags:         cloneTags(state.Tags),
		MetricValue:  state.MetricValue,
		RaisedAt:     state.RaisedAt,
		LastNotified: cloneTimes(state.LastNotified),
	}, true
}

// metricValue formats typed event value for notification/template rendering.
// Params: typed event value payload.
// Returns: compact string representation.
func metricValue(value domain.TypedValue) string {
	switch value.Type {
	case "n":
		if value.N == nil {
			return ""
		}
		return strconv.FormatFloat(*value.N, 'f', -1, 64)
	case "s":
		if value.S == nil {
			return ""
		}
		return *value.S
	case "b":
		if value.B == nil {
			return ""
		}
		return strconv.FormatBool(*value.B)
	default:
		return ""
	}
}

// SetLastNotified updates per-channel last notification timestamp.
// Params: alert ID, channel name, and timestamp.
// Returns: true when state exists and timestamp is updated.
func (e *Engine) SetLastNotified(alertID, channel string, now time.Time) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	state, ok := e.states[alertID]
	if !ok {
		return false
	}
	if state.LastNotified == nil {
		state.LastNotified = make(map[string]time.Time)
	}
	state.LastNotified[channel] = now
	return true
}

// GetChannelMessageID reads stored per-channel message id for alert lifecycle threading.
// Params: alert ID and channel key.
// Returns: stored message id and existence flag.
func (e *Engine) GetChannelMessageID(alertID, channel string) (int, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	state, ok := e.states[alertID]
	if !ok || state.ChannelRefs == nil {
		return 0, false
	}
	messageID, exists := state.ChannelRefs[channel]
	return messageID, exists
}

// SetChannelMessageID stores per-channel message id for alert lifecycle threading.
// Params: alert ID, channel key, and Telegram message id.
// Returns: true when state exists and value is updated.
func (e *Engine) SetChannelMessageID(alertID, channel string, messageID int) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	state, ok := e.states[alertID]
	if !ok {
		return false
	}
	if state.ChannelRefs == nil {
		state.ChannelRefs = make(map[string]int)
	}
	state.ChannelRefs[channel] = messageID
	return true
}

// GetChannelExternalRef reads stored per-channel external issue reference.
// Params: alert ID and channel key.
// Returns: stored external reference and existence flag.
func (e *Engine) GetChannelExternalRef(alertID, channel string) (string, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	state, ok := e.states[alertID]
	if !ok || state.ExternalRefs == nil {
		return "", false
	}
	value, exists := state.ExternalRefs[channel]
	return value, exists
}

// SetChannelExternalRef stores per-channel external issue reference.
// Params: alert ID, channel key, and external reference value.
// Returns: true when state exists and value is updated.
func (e *Engine) SetChannelExternalRef(alertID, channel, value string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	state, ok := e.states[alertID]
	if !ok {
		return false
	}
	if state.ExternalRefs == nil {
		state.ExternalRefs = make(map[string]string)
	}
	state.ExternalRefs[channel] = value
	return true
}

// ListStatesByRule returns current states for one rule.
// Params: rule name.
// Returns: shallow copies of matching runtime states.
func (e *Engine) ListStatesByRule(ruleName string) []RuntimeState {
	e.mu.RLock()
	defer e.mu.RUnlock()
	states := make([]RuntimeState, 0)
	for _, state := range e.states {
		if state.RuleName != ruleName {
			continue
		}
		states = append(states, cloneRuntimeState(state))
	}
	return states
}

// cloneRuntimeState duplicates mutable maps/slices from runtime state.
// Params: source runtime state pointer.
// Returns: detached runtime state copy.
func cloneRuntimeState(source *RuntimeState) RuntimeState {
	copyState := *source
	copyState.Tags = cloneTags(source.Tags)
	copyState.CountWindow = append([]WindowPoint(nil), source.CountWindow...)
	copyState.LastNotified = cloneTimes(source.LastNotified)
	copyState.ChannelRefs = cloneInts(source.ChannelRefs)
	copyState.ExternalRefs = cloneStrings(source.ExternalRefs)
	return copyState
}

// pruneWindow removes stale points outside sliding window.
// Params: points list, current time, and window width.
// Returns: filtered points list.
func pruneWindow(points []WindowPoint, now time.Time, window time.Duration, windowSum int64) ([]WindowPoint, int64) {
	if window <= 0 {
		return nil, 0
	}
	cutoff := now.Add(-window)
	drop := 0
	for ; drop < len(points); drop++ {
		point := points[drop]
		if !point.At.Before(cutoff) {
			break
		}
		windowSum -= point.Count
	}
	if drop == 0 {
		return points, windowSum
	}
	return points[drop:], windowSum
}

// cloneTags duplicates tag map.
// Params: source tags map.
// Returns: copied map.
func cloneTags(source map[string]string) map[string]string {
	if len(source) == 0 {
		return nil
	}
	out := make(map[string]string, len(source))
	for key, value := range source {
		out[key] = value
	}
	return out
}

// cloneStrings duplicates string map.
// Params: source string map.
// Returns: copied map.
func cloneStrings(source map[string]string) map[string]string {
	if len(source) == 0 {
		return nil
	}
	out := make(map[string]string, len(source))
	for key, value := range source {
		out[key] = value
	}
	return out
}

// cloneTimes duplicates time map.
// Params: source channel timestamp map.
// Returns: copied map.
func cloneTimes(source map[string]time.Time) map[string]time.Time {
	if len(source) == 0 {
		return nil
	}
	out := make(map[string]time.Time, len(source))
	for key, value := range source {
		out[key] = value
	}
	return out
}

// cloneInts duplicates channel message id map.
// Params: source channel message map.
// Returns: copied map.
func cloneInts(source map[string]int) map[string]int {
	if len(source) == 0 {
		return nil
	}
	out := make(map[string]int, len(source))
	for key, value := range source {
		out[key] = value
	}
	return out
}

// CompactStates evicts inactive runtime states by idle TTL and optional max cap.
// Params: current time, idle TTL threshold, and maximum state count (0 disables cap).
// Returns: number of evicted states.
func (e *Engine) CompactStates(now time.Time, idleTTL time.Duration, maxStates int) int {
	e.mu.Lock()
	defer e.mu.Unlock()

	removed := 0
	if idleTTL > 0 {
		for alertID, runtimeState := range e.states {
			if runtimeState.CurrentState != "" {
				continue
			}
			if runtimeState.LastSeen.IsZero() {
				continue
			}
			if now.Sub(runtimeState.LastSeen) < idleTTL {
				continue
			}
			delete(e.states, alertID)
			removed++
		}
	}

	if maxStates <= 0 || len(e.states) <= maxStates {
		return removed
	}

	type candidate struct {
		alertID  string
		lastSeen time.Time
	}
	candidates := make([]candidate, 0, len(e.states))
	for alertID, runtimeState := range e.states {
		if runtimeState.CurrentState != "" {
			continue
		}
		candidates = append(candidates, candidate{
			alertID:  alertID,
			lastSeen: runtimeState.LastSeen,
		})
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].lastSeen.Before(candidates[j].lastSeen)
	})

	need := len(e.states) - maxStates
	for _, item := range candidates {
		if need <= 0 {
			break
		}
		if _, ok := e.states[item.alertID]; !ok {
			continue
		}
		delete(e.states, item.alertID)
		removed++
		need--
	}
	return removed
}
