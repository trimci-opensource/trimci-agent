package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPostShape(t *testing.T) {
	var gotPath, gotAuth, gotAgentHeader, gotContentType string
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotAgentHeader = r.Header.Get("X-TrimCI-Agent")
		gotContentType = r.Header.Get("Content-Type")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		if r.Method != http.MethodPost {
			t.Errorf("method = %s", r.Method)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"protocol": 1, "repos": []any{}})
	}))
	defer server.Close()

	client := NewClient(server.URL, "trimci_agent_KEY12345_secret", "trimci-agent/9.9.9")
	if _, err := client.Hello(context.Background(), HelloRequest{InstanceID: "host-1"}); err != nil {
		t.Fatalf("Hello: %v", err)
	}
	if gotPath != "/ingest/v1/hello/" {
		t.Errorf("path = %q — the trailing slash is part of the protocol", gotPath)
	}
	if gotAuth != "Bearer trimci_agent_KEY12345_secret" {
		t.Errorf("auth = %q", gotAuth)
	}
	if gotAgentHeader != "trimci-agent/9.9.9" || gotContentType != "application/json" {
		t.Errorf("headers = %q / %q", gotAgentHeader, gotContentType)
	}
	if gotBody["protocol"] != float64(ProtocolVersion) {
		t.Errorf("protocol = %v — must be injected into every body", gotBody["protocol"])
	}
	if gotBody["instance_id"] != "host-1" {
		t.Errorf("instance_id = %v", gotBody["instance_id"])
	}
}

func TestEmptyFinalRunsMarshalsAsList(t *testing.T) {
	var raw []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ = io.ReadAll(r.Body)
		_ = json.NewEncoder(w).Encode(map[string]any{"accepted": 0, "rejected": []any{}})
	}))
	defer server.Close()

	client := NewClient(server.URL, "token-token-token", "ua")
	_, err := client.PushRuns(context.Background(), RunsRequest{RepoExternalID: "42", Final: true})
	if err != nil {
		t.Fatalf("PushRuns: %v", err)
	}
	body := string(raw)
	if !strings.Contains(body, `"runs":[]`) {
		t.Errorf(`body must carry "runs":[] (the empty final batch), got %s`, body)
	}
	if !strings.Contains(body, `"final":true`) {
		t.Errorf("final flag missing: %s", body)
	}
}

func TestErrorEnvelopeParsed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "17")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error": {"code": "rate_limited", "message": "Rate limit exceeded."}}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "token-token-token", "ua")
	_, err := client.Hello(context.Background(), HelloRequest{})
	if err == nil {
		t.Fatal("expected error")
	}
	if !IsCode(err, CodeRateLimited) {
		t.Errorf("IsCode(rate_limited) = false: %v", err)
	}
	if Status(err) != http.StatusTooManyRequests {
		t.Errorf("Status = %d", Status(err))
	}
	if RetryAfter(err) != 17*time.Second {
		t.Errorf("RetryAfter = %v", RetryAfter(err))
	}
}

func TestNonEnvelopeErrorTolerated(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("<html>proxy says no</html>"))
	}))
	defer server.Close()

	client := NewClient(server.URL, "token-token-token", "ua")
	_, err := client.Hello(context.Background(), HelloRequest{})
	if Status(err) != http.StatusBadGateway {
		t.Fatalf("Status = %d (%v)", Status(err), err)
	}
	if IsCode(err, CodeRateLimited) || RetryAfter(err) != 0 {
		t.Errorf("proxy error misparsed: %v", err)
	}
}
