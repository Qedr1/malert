package clock

import "time"

// Clock provides current time abstraction for deterministic tests.
// Params: none.
// Returns: current wall-clock time.
type Clock interface {
	Now() time.Time
}

// RealClock reads current UTC time from system clock.
// Params: none.
// Returns: current UTC timestamp.
type RealClock struct{}

// Now returns current UTC time.
// Params: none.
// Returns: current UTC timestamp.
func (RealClock) Now() time.Time {
	return time.Now().UTC()
}
