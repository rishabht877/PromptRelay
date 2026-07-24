package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
)

// postJSON marshals in, POSTs it to url with the given headers, and returns the
// status code and raw response body. A non-nil error means a transport-level
// failure (connection, timeout) — the status code is 0 in that case. A non-2xx
// response is NOT an error here; callers inspect the status.
func postJSON(ctx context.Context, client *http.Client, url string, headers map[string]string, in any) (int, []byte, error) {
	buf, err := json.Marshal(in)
	if err != nil {
		return 0, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(buf))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, data, nil
}

// truncate keeps error messages from carrying a full upstream body into logs/metrics.
func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "…"
}
