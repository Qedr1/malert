package permanent

import "errors"

// Error marks operation failures that are not retryable.
// Params: wrapped root cause.
// Returns: typed permanent error marker.
type Error struct {
	Err error
}

// Error returns wrapped error message.
// Params: none.
// Returns: string representation.
func (e Error) Error() string {
	if e.Err == nil {
		return "permanent error"
	}
	return e.Err.Error()
}

// Unwrap exposes wrapped cause for errors.Is/errors.As.
// Params: none.
// Returns: wrapped error.
func (e Error) Unwrap() error {
	return e.Err
}

// Permanent marks error as non-retryable.
// Params: none.
// Returns: true.
func (Error) Permanent() bool {
	return true
}

// Mark wraps error with permanent marker.
// Params: source error.
// Returns: wrapped error or nil.
func Mark(err error) error {
	if err == nil {
		return nil
	}
	return Error{Err: err}
}

// Is reports whether error has permanent marker.
// Params: candidate error.
// Returns: true when non-retryable marker is present.
func Is(err error) bool {
	if err == nil {
		return false
	}
	type marker interface {
		Permanent() bool
	}
	var tagged marker
	if !errors.As(err, &tagged) {
		return false
	}
	return tagged.Permanent()
}
