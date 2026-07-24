package types

import "fmt"

// ProviderError is the normalized error every provider adapter returns. StatusCode
// is the upstream HTTP status, or 0 for a transport-level failure (connection
// refused, timeout, DNS, etc.). The router uses Retryable to decide whether to fall
// over to the next provider.
type ProviderError struct {
	Provider   string
	StatusCode int
	Message    string
	Err        error
}

func (e *ProviderError) Error() string {
	if e.StatusCode != 0 {
		return fmt.Sprintf("provider %s: http %d: %s", e.Provider, e.StatusCode, e.Message)
	}
	return fmt.Sprintf("provider %s: %s", e.Provider, e.Message)
}

func (e *ProviderError) Unwrap() error { return e.Err }

// Retryable reports whether the failure is worth failing over to another provider.
//
// Retryable (try the next provider): transport/timeout (status 0), 429 rate limits,
// and 5xx server errors. Terminal (return immediately): 4xx client errors — a
// malformed request or bad auth will fail identically on every provider, so failing
// over just wastes latency and money.
func (e *ProviderError) Retryable() bool {
	switch {
	case e.StatusCode == 0: // transport error or timeout
		return true
	case e.StatusCode == 429:
		return true
	case e.StatusCode >= 500:
		return true
	default: // 4xx
		return false
	}
}

// Reason is a low-cardinality label for the fallback metric.
func (e *ProviderError) Reason() string {
	switch {
	case e.StatusCode == 0:
		return "timeout_or_transport"
	case e.StatusCode == 429:
		return "rate_limit"
	case e.StatusCode >= 500:
		return "server_error"
	default:
		return "client_error"
	}
}
