// Package healthz optionally serves GET /healthz for container health
// checks. It only exists when HEALTHZ_ADDR is set — by default the agent
// opens no listening ports at all (outbound-only is a security property).
package healthz

import (
	"encoding/json"
	"net"
	"net/http"
	"sync"
	"time"
)

// State tracks the agent's last successful hello, shared between the agent
// loop (writer) and the health endpoint (reader).
type State struct {
	mu           sync.Mutex
	lastHelloOK  time.Time
	pollInterval time.Duration
}

func NewState() *State {
	return &State{pollInterval: 60 * time.Second}
}

// HelloSucceeded records a successful hello and the server-driven interval.
func (s *State) HelloSucceeded(interval time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastHelloOK = time.Now()
	if interval > 0 {
		s.pollInterval = interval
	}
}

func (s *State) snapshot() (time.Time, time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastHelloOK, s.pollInterval
}

// Serve starts the health listener. Returns the close func, or an error if
// the address cannot be bound. Healthy = at least one successful hello, the
// most recent within 5 poll intervals (generous: rate-limit waits and long
// batches legitimately delay hellos).
func Serve(addr string, state *State) (func() error, error) {
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		lastOK, interval := state.snapshot()
		healthy := !lastOK.IsZero() && time.Since(lastOK) < 5*interval
		w.Header().Set("Content-Type", "application/json")
		if !healthy {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		payload := map[string]any{"healthy": healthy}
		if !lastOK.IsZero() {
			payload["last_hello_ok"] = lastOK.UTC().Format(time.RFC3339)
		}
		_ = json.NewEncoder(w).Encode(payload)
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = server.Serve(listener) }()
	return server.Close, nil
}
