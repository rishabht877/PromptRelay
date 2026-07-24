package types

import "testing"

func TestProviderErrorRetryable(t *testing.T) {
	cases := []struct {
		status int
		want   bool
	}{
		{0, true},    // transport / timeout
		{429, true},  // rate limited
		{500, true},  // server error
		{503, true},  // server error
		{400, false}, // bad request — terminal
		{401, false}, // auth — terminal
		{404, false}, // not found — terminal
	}
	for _, c := range cases {
		e := &ProviderError{StatusCode: c.status}
		if got := e.Retryable(); got != c.want {
			t.Errorf("status %d: Retryable()=%v, want %v", c.status, got, c.want)
		}
	}
}

func TestProviderErrorReason(t *testing.T) {
	cases := map[int]string{
		0:   "timeout_or_transport",
		429: "rate_limit",
		502: "server_error",
		400: "client_error",
	}
	for status, want := range cases {
		if got := (&ProviderError{StatusCode: status}).Reason(); got != want {
			t.Errorf("status %d: Reason()=%q, want %q", status, got, want)
		}
	}
}
