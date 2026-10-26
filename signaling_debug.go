package main

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
)

// debugTransport logs the NetherNet signaling exchange with the backend (offer and answer SDP). It is only
// installed when debug is on, to diagnose connections that stall after signaling succeeds.
type debugTransport struct{ log *slog.Logger }

func (t debugTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	var req []byte
	if r.Body != nil {
		req, _ = io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(req))
	}
	resp, err := http.DefaultTransport.RoundTrip(r)
	if err != nil {
		t.log.Debug("backend signaling request failed", "method", r.Method, "url", r.URL.String(), "err", err)
		return nil, err
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(body))
	t.log.Debug("backend signaling exchange", "method", r.Method, "url", r.URL.String(), "status", resp.StatusCode,
		"request", string(req), "response", string(body))
	return resp, nil
}
