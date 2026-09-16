package agentloop

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/trimci-opensource/trimci-agent/internal/api"
	"github.com/trimci-opensource/trimci-agent/internal/collector"
)

const testToken = "trimci_agent_TESTKEY1_secret-secret-secret"

// ── fakes ───────────────────────────────────────────────────────────────────

// fakeTrimCI is an httptest-backed ingest API: the loop tests exercise the
// real api.Client wire format against it.
type fakeTrimCI struct {
	t *testing.T

	mu       sync.Mutex
	hellos   []api.HelloRequest
	catalogs []api.CatalogRequest
	runs     []api.RunsRequest
	logs     []api.LogsRequest

	// ctl scripts the control plane per hello (1-based call count).
	ctl func(helloCount int) api.HelloResponse
	// runsHandler / logsHandler script data responses (1-based call count).
	// nil = accept everything.
	runsHandler func(call int, req api.RunsRequest) (int, any)
	logsHandler func(call int, req api.LogsRequest) (int, any)
	onHello     func(count int)
}

func envelope(code string) map[string]any {
	return map[string]any{"error": map[string]any{"code": code, "message": code}}
}

func (f *fakeTrimCI) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /ingest/v1/hello/", func(w http.ResponseWriter, r *http.Request) {
		f.checkAuth(r)
		var req api.HelloRequest
		f.decode(r, &req)
		f.mu.Lock()
		f.hellos = append(f.hellos, req)
		count := len(f.hellos)
		f.mu.Unlock()
		if f.onHello != nil {
			f.onHello(count)
		}
		writeJSON(w, http.StatusOK, f.ctl(count))
	})
	mux.HandleFunc("POST /ingest/v1/catalog/", func(w http.ResponseWriter, r *http.Request) {
		f.checkAuth(r)
		var req api.CatalogRequest
		f.decode(r, &req)
		f.mu.Lock()
		f.catalogs = append(f.catalogs, req)
		f.mu.Unlock()
		writeJSON(w, http.StatusOK, api.CatalogResponse{Discovered: len(req.Projects)})
	})
	mux.HandleFunc("POST /ingest/v1/runs/", func(w http.ResponseWriter, r *http.Request) {
		f.checkAuth(r)
		var req api.RunsRequest
		f.decode(r, &req)
		f.mu.Lock()
		f.runs = append(f.runs, req)
		call := len(f.runs)
		f.mu.Unlock()
		if f.runsHandler != nil {
			status, body := f.runsHandler(call, req)
			writeJSON(w, status, body)
			return
		}
		writeJSON(w, http.StatusOK, api.RunsResponse{Accepted: len(req.Runs), Rejected: []api.RejectedRun{}})
	})
	mux.HandleFunc("POST /ingest/v1/logs/", func(w http.ResponseWriter, r *http.Request) {
		f.checkAuth(r)
		var req api.LogsRequest
		f.decode(r, &req)
		f.mu.Lock()
		f.logs = append(f.logs, req)
		call := len(f.logs)
		f.mu.Unlock()
		if f.logsHandler != nil {
			status, body := f.logsHandler(call, req)
			writeJSON(w, status, body)
			return
		}
		writeJSON(w, http.StatusOK, api.LogsResponse{Stored: len(req.Logs), WantLogs: []api.WantLog{}})
	})
	return mux
}

func (f *fakeTrimCI) checkAuth(r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+testToken {
		f.t.Errorf("bad Authorization header: %q", r.Header.Get("Authorization"))
	}
}

func (f *fakeTrimCI) decode(r *http.Request, out any) {
	raw, _ := io.ReadAll(r.Body)
	if err := json.Unmarshal(raw, out); err != nil {
		f.t.Errorf("undecodable body: %v — %s", err, raw)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// fakeCollector scripts the provider side.
type fakeCollector struct {
	mu          sync.Mutex
	status      api.CollectorStatus
	projects    []api.Project
	truncated   bool
	pages       map[string][]collector.RunsPage
	tails       map[string]string
	unavailable map[string]bool
	listCalls   []string
}

func (f *fakeCollector) Describe(ctx context.Context) api.CollectorStatus {
	if f.status.Kind == "" {
		return api.CollectorStatus{Kind: "gitlab_ci", BaseURL: "https://gitlab.internal", Version: "17.8.1", Reachable: true}
	}
	return f.status
}

func (f *fakeCollector) ListProjects(ctx context.Context, max int) ([]api.Project, bool, error) {
	return f.projects, f.truncated, nil
}

func (f *fakeCollector) ListRuns(
	ctx context.Context, projectID string, since time.Time, page func(collector.RunsPage) error,
) error {
	f.mu.Lock()
	f.listCalls = append(f.listCalls, projectID+"|"+since.UTC().Format(time.RFC3339))
	pages := f.pages[projectID]
	f.mu.Unlock()
	if len(pages) == 0 {
		return page(collector.RunsPage{Last: true})
	}
	for _, p := range pages {
		if err := page(p); err != nil {
			return err
		}
	}
	return nil
}

func (f *fakeCollector) LogTail(ctx context.Context, projectID, jobID string, maxChars int) (string, bool, error) {
	if f.unavailable[jobID] {
		return "", true, nil
	}
	return f.tails[jobID], false, nil
}

// ── harness ─────────────────────────────────────────────────────────────────

type recordingSleeper struct {
	mu    sync.Mutex
	slept []time.Duration
}

func (s *recordingSleeper) Sleep(ctx context.Context, d time.Duration) error {
	s.mu.Lock()
	s.slept = append(s.slept, d)
	s.mu.Unlock()
	return ctx.Err()
}

func baseCtl(repos []api.RepoEntry, wants []api.WantLog, catalog bool) api.HelloResponse {
	return api.HelloResponse{
		Protocol:            api.ProtocolVersion,
		PollIntervalSeconds: 60,
		CatalogRequested:    catalog,
		BackfillNotBefore:   time.Now().Add(-60 * 24 * time.Hour).UTC().Format(time.RFC3339),
		Limits:              api.Limits{MaxRunsPerBatch: 100, MaxBodyBytes: 1 << 20, LogTailChars: 8000, MaxLogsPerRequest: 20},
		Repos:               repos, WantLogs: wants,
	}
}

func dueRepo(id string) api.RepoEntry {
	return api.RepoEntry{ExternalID: id, Name: "acme/api", SyncDue: true}
}

func newAgent(t *testing.T, fake *fakeTrimCI, col collector.Collector, once bool) (*Agent, *recordingSleeper) {
	t.Helper()
	server := httptest.NewServer(fake.handler())
	t.Cleanup(server.Close)
	sleeper := &recordingSleeper{}
	agent := &Agent{
		API:       api.NewClient(server.URL, testToken, "trimci-agent/test"),
		Collector: col,
		Log:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		Sleep:     sleeper.Sleep,
		Once:      once,
	}
	return agent, sleeper
}

func mkRun(id int64, status string, jobs ...api.RunJob) api.Run {
	now := time.Now().UTC()
	return api.Run{
		ID: id, Status: status, Ref: "main",
		CreatedAt: now.Add(-10 * time.Minute).Format(time.RFC3339),
		UpdatedAt: now.Add(-5 * time.Minute).Format(time.RFC3339),
		Jobs:      jobs,
	}
}

// ── tests ───────────────────────────────────────────────────────────────────

func TestSingleCycleJourney(t *testing.T) {
	col := &fakeCollector{
		projects: []api.Project{{ID: 42, PathWithNamespace: "acme/api"}, {ID: 57, PathWithNamespace: "acme/web"}},
		pages: map[string][]collector.RunsPage{
			"42": {{
				Runs: []api.Run{
					mkRun(1, "success"),
					mkRun(2, "failed", api.RunJob{ID: 7001, Name: "pytest", Status: "failed"}),
				},
				Last: true,
			}},
		},
		tails: map[string]string{"7001": "FAILED tests/test_api.py::test_x - assert 1 == 2"},
	}
	fake := &fakeTrimCI{t: t}
	fake.ctl = func(int) api.HelloResponse { return baseCtl([]api.RepoEntry{dueRepo("42")}, nil, true) }
	fake.runsHandler = func(call int, req api.RunsRequest) (int, any) {
		return http.StatusOK, api.RunsResponse{
			Accepted: len(req.Runs),
			Rejected: []api.RejectedRun{},
			WantLogs: []api.WantLog{{RepoExternalID: "42", RunExternalID: "2", JobExternalID: "7001"}},
		}
	}

	agent, _ := newAgent(t, fake, col, true)
	if err := agent.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(fake.hellos) != 1 {
		t.Fatalf("hellos = %d", len(fake.hellos))
	}
	hello := fake.hellos[0]
	if hello.InstanceID == "" || hello.Agent.Version == "" || hello.Collector.Kind != "gitlab_ci" {
		t.Errorf("hello body incomplete: %+v", hello)
	}

	if len(fake.catalogs) != 1 {
		t.Fatalf("catalogs = %d", len(fake.catalogs))
	}
	catalog := fake.catalogs[0]
	if catalog.Protocol != 1 || len(catalog.Projects) != 2 || catalog.Truncated {
		t.Errorf("catalog body = %+v", catalog)
	}

	if len(fake.runs) != 1 {
		t.Fatalf("runs calls = %d", len(fake.runs))
	}
	runsReq := fake.runs[0]
	if runsReq.RepoExternalID != "42" || !runsReq.Final || len(runsReq.Runs) != 2 {
		t.Errorf("runs body = %+v", runsReq)
	}
	if runsReq.Runs[1].Status != "failed" || len(runsReq.Runs[1].Jobs) != 1 {
		t.Errorf("raw run shape lost: %+v", runsReq.Runs[1])
	}

	if len(fake.logs) != 1 {
		t.Fatalf("logs calls = %d", len(fake.logs))
	}
	logsReq := fake.logs[0]
	if logsReq.RepoExternalID != "42" || len(logsReq.Logs) != 1 {
		t.Fatalf("logs body = %+v", logsReq)
	}
	entry := logsReq.Logs[0]
	if entry.RunExternalID != "2" || entry.JobExternalID != "7001" || entry.Tail == "" || entry.Unavailable {
		t.Errorf("log entry = %+v", entry)
	}
}

func TestEmptyFinalBatch(t *testing.T) {
	col := &fakeCollector{} // no pages: nothing new for the due repo
	fake := &fakeTrimCI{t: t}
	fake.ctl = func(int) api.HelloResponse { return baseCtl([]api.RepoEntry{dueRepo("42")}, nil, false) }

	agent, _ := newAgent(t, fake, col, true)
	if err := agent.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(fake.runs) != 1 {
		t.Fatalf("runs calls = %d, want exactly the empty final batch", len(fake.runs))
	}
	req := fake.runs[0]
	if !req.Final || len(req.Runs) != 0 {
		t.Errorf("empty final batch = %+v", req)
	}
}

func TestNotDueRepoSkipped(t *testing.T) {
	col := &fakeCollector{pages: map[string][]collector.RunsPage{"42": {{Runs: []api.Run{mkRun(1, "success")}, Last: true}}}}
	fake := &fakeTrimCI{t: t}
	repo := dueRepo("42")
	repo.SyncDue = false
	fake.ctl = func(int) api.HelloResponse { return baseCtl([]api.RepoEntry{repo}, nil, false) }

	agent, _ := newAgent(t, fake, col, true)
	if err := agent.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(fake.runs) != 0 || len(col.listCalls) != 0 {
		t.Errorf("not-due repo was synced: runs=%d listCalls=%v", len(fake.runs), col.listCalls)
	}
}

func TestPausedConnectionPushesNothing(t *testing.T) {
	col := &fakeCollector{projects: []api.Project{{ID: 42, PathWithNamespace: "acme/api"}}}
	fake := &fakeTrimCI{t: t}
	fake.ctl = func(int) api.HelloResponse {
		ctl := baseCtl([]api.RepoEntry{dueRepo("42")}, []api.WantLog{{RepoExternalID: "42", RunExternalID: "1", JobExternalID: "2"}}, true)
		ctl.Connection.Paused = true
		return ctl
	}

	agent, _ := newAgent(t, fake, col, true)
	if err := agent.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(fake.catalogs)+len(fake.runs)+len(fake.logs) != 0 {
		t.Errorf("paused connection pushed data: catalog=%d runs=%d logs=%d",
			len(fake.catalogs), len(fake.runs), len(fake.logs))
	}
}

func TestBatchHalvingOn413(t *testing.T) {
	page := collector.RunsPage{Last: true}
	for i := int64(1); i <= 4; i++ {
		page.Runs = append(page.Runs, mkRun(i, "success"))
	}
	col := &fakeCollector{pages: map[string][]collector.RunsPage{"42": {page}}}
	fake := &fakeTrimCI{t: t}
	fake.ctl = func(int) api.HelloResponse { return baseCtl([]api.RepoEntry{dueRepo("42")}, nil, false) }
	fake.runsHandler = func(call int, req api.RunsRequest) (int, any) {
		if len(req.Runs) > 1 {
			return http.StatusRequestEntityTooLarge, envelope(api.CodePayloadTooLarge)
		}
		return http.StatusOK, api.RunsResponse{Accepted: len(req.Runs), Rejected: []api.RejectedRun{}}
	}

	agent, _ := newAgent(t, fake, col, true)
	if err := agent.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Count only DELIVERED batches (size 1 — everything larger was refused
	// with 413 before processing): exactly the last one may carry final.
	var singles, deliveredFinals int
	var lastDeliveredHasFinal bool
	for _, req := range fake.runs {
		if len(req.Runs) == 1 {
			singles++
			lastDeliveredHasFinal = req.Final
			if req.Final {
				deliveredFinals++
			}
		}
	}
	if singles != 4 {
		t.Errorf("expected 4 single-run deliveries after halving, got %d (calls=%d)", singles, len(fake.runs))
	}
	if deliveredFinals != 1 || !lastDeliveredHasFinal {
		t.Errorf("exactly the last delivered batch must carry final: finals=%d last=%v",
			deliveredFinals, lastDeliveredHasFinal)
	}
	// The halved cap sticks: agent-side outcome counts all four as accepted.
	if len(agent.outcomes) != 1 || agent.outcomes[0].Status != "ok" {
		t.Errorf("outcomes = %+v", agent.outcomes)
	}
}

func TestLongRateWindowSkipsRepo(t *testing.T) {
	col := &fakeCollector{pages: map[string][]collector.RunsPage{"42": {{Runs: []api.Run{mkRun(1, "success")}, Last: true}}}}
	fake := &fakeTrimCI{t: t}
	fake.ctl = func(int) api.HelloResponse { return baseCtl([]api.RepoEntry{dueRepo("42")}, nil, false) }

	agent, sleeper := newAgent(t, fake, col, true)
	// Wrap the fake so the runs endpoint answers 429 with a LONG Retry-After.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ingest/v1/runs/" {
			w.Header().Set("Retry-After", "3600")
			writeJSON(w, http.StatusTooManyRequests, envelope(api.CodeRateLimited))
			return
		}
		fake.handler().ServeHTTP(w, r)
	}))
	defer server.Close()
	agent.API = api.NewClient(server.URL, testToken, "trimci-agent/test")

	if err := agent.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, d := range sleeper.slept {
		if d >= time.Hour {
			t.Errorf("agent slept through a long rate window (%v) instead of abandoning the repo", d)
		}
	}
	if len(agent.outcomes) != 1 || agent.outcomes[0].Code != "rate_limited" || agent.outcomes[0].Status != "skipped" {
		t.Errorf("outcomes = %+v", agent.outcomes)
	}
}

func TestInlineRateLimitRetried(t *testing.T) {
	col := &fakeCollector{pages: map[string][]collector.RunsPage{"42": {{Runs: []api.Run{mkRun(1, "success")}, Last: true}}}}
	fake := &fakeTrimCI{t: t}
	fake.ctl = func(int) api.HelloResponse { return baseCtl([]api.RepoEntry{dueRepo("42")}, nil, false) }

	var rateLimited bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ingest/v1/runs/" && !rateLimited {
			rateLimited = true
			w.Header().Set("Retry-After", "5")
			writeJSON(w, http.StatusTooManyRequests, envelope(api.CodeRateLimited))
			return
		}
		fake.handler().ServeHTTP(w, r)
	}))
	defer server.Close()

	agent, sleeper := newAgent(t, fake, col, true)
	agent.API = api.NewClient(server.URL, testToken, "trimci-agent/test")
	if err := agent.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(fake.runs) != 1 {
		t.Fatalf("expected the batch to be retried to success, recorded=%d", len(fake.runs))
	}
	var sleptExactly bool
	for _, d := range sleeper.slept {
		if d == 5*time.Second {
			sleptExactly = true
		}
	}
	if !sleptExactly {
		t.Errorf("Retry-After 5 not honored exactly: %v", sleeper.slept)
	}
	if len(agent.outcomes) != 1 || agent.outcomes[0].Status != "ok" {
		t.Errorf("outcomes = %+v", agent.outcomes)
	}
}

func TestOutcomesReportedInNextHello(t *testing.T) {
	col := &fakeCollector{pages: map[string][]collector.RunsPage{"42": {{Runs: []api.Run{mkRun(1, "success")}, Last: true}}}}
	fake := &fakeTrimCI{t: t}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fake.ctl = func(count int) api.HelloResponse {
		if count == 1 {
			return baseCtl([]api.RepoEntry{dueRepo("42")}, nil, false)
		}
		ctl := baseCtl(nil, nil, false)
		ctl.Connection.Paused = true
		return ctl
	}
	fake.onHello = func(count int) {
		if count >= 2 {
			cancel()
		}
	}

	agent, _ := newAgent(t, fake, col, false)
	_ = agent.Run(ctx)

	if len(fake.hellos) < 2 {
		t.Fatalf("hellos = %d", len(fake.hellos))
	}
	second := fake.hellos[1]
	if len(second.Repos) != 1 || second.Repos[0].ExternalID != "42" || second.Repos[0].Status != "ok" {
		t.Errorf("second hello outcomes = %+v", second.Repos)
	}
	if len(fake.hellos[0].Repos) != 0 {
		t.Errorf("first hello must carry no outcomes, got %+v", fake.hellos[0].Repos)
	}
}

func TestUnknownRepoWantsDropped(t *testing.T) {
	col := &fakeCollector{tails: map[string]string{"7001": "tail-a", "8001": "tail-b"}}
	fake := &fakeTrimCI{t: t}
	wants := []api.WantLog{
		{RepoExternalID: "42", RunExternalID: "1", JobExternalID: "7001"},
		{RepoExternalID: "57", RunExternalID: "2", JobExternalID: "8001"},
	}
	fake.ctl = func(int) api.HelloResponse { return baseCtl(nil, wants, false) }
	fake.logsHandler = func(call int, req api.LogsRequest) (int, any) {
		if req.RepoExternalID == "42" {
			return http.StatusNotFound, envelope(api.CodeUnknownRepo)
		}
		return http.StatusOK, api.LogsResponse{Stored: len(req.Logs), WantLogs: []api.WantLog{}}
	}

	agent, _ := newAgent(t, fake, col, true)
	if err := agent.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(fake.logs) != 2 {
		t.Fatalf("logs calls = %d (want the 404'd repo dropped, the other delivered)", len(fake.logs))
	}
	if fake.logs[1].RepoExternalID != "57" || fake.logs[1].Logs[0].Tail != "tail-b" {
		t.Errorf("second logs call = %+v", fake.logs[1])
	}
}

func TestLogDrainLivelockGuard(t *testing.T) {
	col := &fakeCollector{unavailable: map[string]bool{"7001": true}}
	fake := &fakeTrimCI{t: t}
	wants := []api.WantLog{{RepoExternalID: "42", RunExternalID: "1", JobExternalID: "7001"}}
	fake.ctl = func(int) api.HelloResponse { return baseCtl(nil, wants, false) }
	fake.logsHandler = func(call int, req api.LogsRequest) (int, any) {
		// Server keeps wanting the same entry and stores nothing — the agent
		// must bail out instead of hammering it 30 times.
		return http.StatusOK, api.LogsResponse{Stored: 0, Skipped: len(req.Logs), WantLogs: wants}
	}

	agent, _ := newAgent(t, fake, col, true)
	if err := agent.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(fake.logs) > 2 {
		t.Errorf("livelock: %d identical log pushes", len(fake.logs))
	}
	if len(fake.logs) >= 1 && !fake.logs[0].Logs[0].Unavailable {
		t.Errorf("unavailable trace must be reported as unavailable: %+v", fake.logs[0].Logs[0])
	}
	if len(fake.logs) >= 1 && fake.logs[0].Logs[0].RunExternalID == "" {
		t.Errorf("unavailable entries still require run_external_id")
	}
}

func TestCursorUsedAsSince(t *testing.T) {
	col := &fakeCollector{}
	fake := &fakeTrimCI{t: t}
	cursor := "2026-08-20T10:00:00+00:00"
	repo := dueRepo("42")
	repo.Cursor = &cursor
	fake.ctl = func(int) api.HelloResponse { return baseCtl([]api.RepoEntry{repo}, nil, false) }

	agent, _ := newAgent(t, fake, col, true)
	if err := agent.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(col.listCalls) != 1 {
		t.Fatalf("listCalls = %v", col.listCalls)
	}
	want := fmt.Sprintf("42|%s", "2026-08-20T10:00:00Z")
	if col.listCalls[0] != want {
		t.Errorf("since = %q, want %q (the server cursor, not the backfill floor)", col.listCalls[0], want)
	}
}
