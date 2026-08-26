package gitlabci

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/trimci/agent/internal/collector"
)

// fakeGitLab is a minimal in-memory GitLab API for collector tests: real
// updated_after filtering, real pagination headers, scripted details, jobs
// and traces.
type fakeGitLab struct {
	t *testing.T

	mu            sync.Mutex
	projects      []glProject
	pipelines     []glPipeline // ascending by updated_at
	details       map[int64]glPipeline
	jobs          map[int64][]glJob
	traces        map[int64]string
	rateLimitOnce bool

	pipelineQueries []string // "updated_after|page" per request
	detailCalls     int
	jobsCalls       int
}

func (f *fakeGitLab) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/version", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]string{"version": "17.8.1"})
	})
	mux.HandleFunc("/api/v4/projects", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("membership") != "true" || r.URL.Query().Get("simple") != "true" {
			f.t.Errorf("projects listed without membership/simple narrowing: %s", r.URL.RawQuery)
		}
		writePage(w, r, f.projects)
	})
	mux.HandleFunc("GET /api/v4/projects/{id}/pipelines", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		if f.rateLimitOnce {
			f.rateLimitOnce = false
			f.mu.Unlock()
			w.Header().Set("Retry-After", "7")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		f.pipelineQueries = append(f.pipelineQueries,
			r.URL.Query().Get("updated_after")+"|"+r.URL.Query().Get("page"))
		f.mu.Unlock()
		if r.URL.Query().Get("order_by") != "updated_at" || r.URL.Query().Get("sort") != "asc" {
			f.t.Errorf("pipelines listed without asc updated_at ordering: %s", r.URL.RawQuery)
		}
		after, err := time.Parse(time.RFC3339, r.URL.Query().Get("updated_after"))
		if err != nil {
			f.t.Errorf("unparseable updated_after %q", r.URL.Query().Get("updated_after"))
		}
		var filtered []glPipeline
		for _, p := range f.pipelines {
			ts, parseErr := time.Parse(time.RFC3339, p.UpdatedAt)
			// Rows with a broken updated_at are always listed (they exist on
			// the instance regardless of the filter window).
			if parseErr != nil || !ts.Before(after) { // GitLab semantics: updated_at >= after
				filtered = append(filtered, p)
			}
		}
		writePage(w, r, filtered)
	})
	mux.HandleFunc("GET /api/v4/projects/{id}/pipelines/{pid}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.detailCalls++
		f.mu.Unlock()
		pid, _ := strconv.ParseInt(r.PathValue("pid"), 10, 64)
		detail, ok := f.details[pid]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		writeJSON(w, detail)
	})
	mux.HandleFunc("GET /api/v4/projects/{id}/pipelines/{pid}/jobs", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.jobsCalls++
		f.mu.Unlock()
		pid, _ := strconv.ParseInt(r.PathValue("pid"), 10, 64)
		writePage(w, r, f.jobs[pid])
	})
	mux.HandleFunc("GET /api/v4/projects/{id}/jobs/{jid}/trace", func(w http.ResponseWriter, r *http.Request) {
		jid, _ := strconv.ParseInt(r.PathValue("jid"), 10, 64)
		trace, ok := f.traces[jid]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(trace))
	})
	return mux
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// writePage slices items per page/per_page and sets X-Next-Page like GitLab.
func writePage[T any](w http.ResponseWriter, r *http.Request, items []T) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	size, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
	if size < 1 {
		size = 20
	}
	start := (page - 1) * size
	if start > len(items) {
		start = len(items)
	}
	end := start + size
	if end > len(items) {
		end = len(items)
	}
	if end < len(items) {
		w.Header().Set("X-Next-Page", strconv.Itoa(page+1))
	}
	writeJSON(w, items[start:end])
}

type sleepRecorder struct {
	mu    sync.Mutex
	slept []time.Duration
}

func (s *sleepRecorder) Sleep(ctx context.Context, d time.Duration) error {
	s.mu.Lock()
	s.slept = append(s.slept, d)
	s.mu.Unlock()
	return ctx.Err()
}

func newTestCollector(t *testing.T, fake *fakeGitLab) (*Collector, *sleepRecorder) {
	t.Helper()
	server := httptest.NewServer(fake.handler())
	t.Cleanup(server.Close)
	rec := &sleepRecorder{}
	col, err := New(server.URL, "glpat-test-token-value", "", "trimci-agent/test", rec.Sleep)
	if err != nil {
		t.Fatal(err)
	}
	return col, rec
}

func ts(base time.Time, offset int) string {
	return base.Add(time.Duration(offset) * time.Second).UTC().Format("2006-01-02T15:04:05.000Z")
}

func mkPipeline(id int64, updatedAt, status string) glPipeline {
	ref := "main"
	return glPipeline{
		ID: id, Status: status, Ref: ref,
		CreatedAt: updatedAt, UpdatedAt: updatedAt,
		WebURL: fmt.Sprintf("https://gitlab.example.com/acme/api/-/pipelines/%d", id),
	}
}

func collectAll(t *testing.T, col *Collector, since time.Time) (pages []collector.RunsPage) {
	t.Helper()
	err := col.ListRuns(context.Background(), "42", since, func(p collector.RunsPage) error {
		pages = append(pages, p)
		return nil
	})
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(pages) == 0 || !pages[len(pages)-1].Last {
		t.Fatalf("walk must end with a Last page, got %d pages", len(pages))
	}
	return pages
}

func TestDescribe(t *testing.T) {
	col, _ := newTestCollector(t, &fakeGitLab{t: t})
	status := col.Describe(context.Background())
	if !status.Reachable || status.Version != "17.8.1" || status.Kind != "gitlab_ci" {
		t.Errorf("Describe = %+v", status)
	}
}

func TestDescribeUnreachable(t *testing.T) {
	rec := &sleepRecorder{}
	col, err := New("https://127.0.0.1:1", "glpat-x-token-value", "", "ua", rec.Sleep)
	if err != nil {
		t.Fatal(err)
	}
	status := col.Describe(context.Background())
	if status.Reachable || status.Error == "" {
		t.Errorf("Describe = %+v", status)
	}
}

func TestListProjects(t *testing.T) {
	fake := &fakeGitLab{t: t}
	for i := int64(1); i <= 250; i++ {
		fake.projects = append(fake.projects, glProject{ID: i, PathWithNamespace: fmt.Sprintf("acme/p%d", i)})
	}
	col, _ := newTestCollector(t, fake)

	all, truncated, err := col.ListProjects(context.Background(), 2000)
	if err != nil || len(all) != 250 || truncated {
		t.Fatalf("full list: n=%d truncated=%v err=%v", len(all), truncated, err)
	}
	cut, truncated, err := col.ListProjects(context.Background(), 150)
	if err != nil || len(cut) != 150 || !truncated {
		t.Fatalf("capped list: n=%d truncated=%v err=%v", len(cut), truncated, err)
	}
}

func TestListRunsKeysetWalk(t *testing.T) {
	base := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	fake := &fakeGitLab{t: t}
	for i := int64(0); i < 250; i++ {
		fake.pipelines = append(fake.pipelines, mkPipeline(1000+i, ts(base, int(i)), "running"))
	}
	col, _ := newTestCollector(t, fake)

	pages := collectAll(t, col, base.Add(-time.Hour))

	seen := map[int64]bool{}
	for _, page := range pages {
		previous := time.Time{}
		for _, run := range page.Runs {
			seen[run.ID] = true
			ts, err := time.Parse(time.RFC3339, run.UpdatedAt)
			if err != nil || ts.Before(previous) {
				t.Fatalf("page not ascending at run %d", run.ID)
			}
			previous = ts
		}
	}
	if len(seen) != 250 {
		t.Errorf("expected all 250 distinct runs delivered, got %d", len(seen))
	}
	// The filter itself must advance (keyset), not the page number.
	fake.mu.Lock()
	defer fake.mu.Unlock()
	firstAfter := strings.Split(fake.pipelineQueries[0], "|")[0]
	lastAfter := strings.Split(fake.pipelineQueries[len(fake.pipelineQueries)-1], "|")[0]
	if firstAfter == lastAfter {
		t.Errorf("updated_after never advanced across %d queries", len(fake.pipelineQueries))
	}
	for _, q := range fake.pipelineQueries {
		if !strings.HasSuffix(q, "|1") {
			t.Errorf("keyset walk used offset page in %q without a same-second slice", q)
		}
	}
}

func TestSameSecondSliceFallsBackToOffset(t *testing.T) {
	base := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	fake := &fakeGitLab{t: t}
	for i := int64(0); i < 150; i++ {
		fake.pipelines = append(fake.pipelines, mkPipeline(2000+i, ts(base, 0), "running"))
	}
	col, _ := newTestCollector(t, fake)

	pages := collectAll(t, col, base.Add(-time.Hour))
	total := 0
	for _, page := range pages {
		total += len(page.Runs)
	}
	if total != 150 {
		t.Errorf("same-second slice delivered %d runs, want 150 exactly once each", total)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.pipelineQueries) != 2 || !strings.HasSuffix(fake.pipelineQueries[1], "|2") {
		t.Errorf("expected offset fallback (page=2), got %v", fake.pipelineQueries)
	}
}

func TestEnrichmentAndJobsGates(t *testing.T) {
	base := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	fake := &fakeGitLab{t: t, details: map[int64]glPipeline{}, jobs: map[int64][]glJob{}}

	success := mkPipeline(1, ts(base, 1), "success")
	running := mkPipeline(2, ts(base, 2), "running")
	failed := mkPipeline(3, ts(base, 3), "failed")
	canceled := mkPipeline(4, ts(base, 4), "canceled")
	withUser := mkPipeline(5, ts(base, 5), "success")
	userID := int64(7)
	withUser.User = &glUser{ID: &userID, Username: "mkowalski"}
	fake.pipelines = []glPipeline{success, running, failed, canceled, withUser}

	duration := 421.0
	startedAt := ts(base, 0)
	detail := success
	detail.Name = "Nightly build"
	detail.User = &glUser{ID: &userID, Username: "mkowalski"}
	detail.Duration = &duration
	detail.StartedAt = &startedAt
	detail.UpdatedAt = ts(base, 999) // detail newer than list — list value must win
	fake.details[1] = detail
	fake.details[3] = failed
	fake.details[4] = canceled

	fake.jobs[3] = []glJob{
		{ID: 31, Name: "pytest", Status: "failed", Duration: &duration},
		{ID: 32, Name: "lint", Status: "success"},
	}
	fake.jobs[4] = []glJob{{ID: 41, Name: "deploy", Status: "canceled"}}

	col, _ := newTestCollector(t, fake)
	pages := collectAll(t, col, base.Add(-time.Hour))
	if len(pages) != 1 {
		t.Fatalf("expected one Last page, got %d", len(pages))
	}
	runs := pages[0].Runs
	if len(runs) != 5 {
		t.Fatalf("got %d runs", len(runs))
	}

	byID := map[int64]int{}
	for i, run := range runs {
		byID[run.ID] = i
	}
	enriched := runs[byID[1]]
	if enriched.Name != "Nightly build" || enriched.User == nil || enriched.User.Username != "mkowalski" {
		t.Errorf("terminal run without user was not enriched: %+v", enriched)
	}
	if enriched.UpdatedAt != ts(base, 1) {
		t.Errorf("enrichment must keep the LIST updated_at for cursor math, got %s", enriched.UpdatedAt)
	}
	if got := runs[byID[2]]; got.User != nil || got.Status != "running" {
		t.Errorf("running run must pass through un-enriched: %+v", got)
	}
	if got := len(runs[byID[3]].Jobs); got != 2 {
		t.Errorf("failed run carries %d jobs, want 2", got)
	}
	if got := len(runs[byID[4]].Jobs); got != 1 {
		t.Errorf("canceled run carries %d jobs, want 1", got)
	}
	if got := runs[byID[5]].Jobs; got != nil {
		t.Errorf("success run must not carry jobs, got %v", got)
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.detailCalls != 3 {
		t.Errorf("detail fetched %d times, want 3 (terminal-without-user only)", fake.detailCalls)
	}
	if fake.jobsCalls != 2 {
		t.Errorf("jobs fetched %d times, want 2 (failed/canceled only)", fake.jobsCalls)
	}
}

func TestUnparseableUpdatedAtDropped(t *testing.T) {
	base := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	fake := &fakeGitLab{t: t}
	good := mkPipeline(1, ts(base, 1), "running")
	bad := mkPipeline(2, ts(base, 2), "running")
	bad.UpdatedAt = "not-a-timestamp"
	fake.pipelines = []glPipeline{good, bad}

	col, _ := newTestCollector(t, fake)
	pages := collectAll(t, col, base.Add(-time.Hour))
	if len(pages[0].Runs) != 1 || pages[0].Runs[0].ID != 1 {
		t.Errorf("runs = %+v", pages[0].Runs)
	}
	if pages[0].Dropped != 1 {
		t.Errorf("Dropped = %d, want 1", pages[0].Dropped)
	}
}

func TestLogTail(t *testing.T) {
	fake := &fakeGitLab{t: t, traces: map[int64]string{}}
	long := strings.Repeat("x", 4000) + strings.Repeat("y", 8000)
	fake.traces[40071] = long
	col, _ := newTestCollector(t, fake)

	tail, unavailable, err := col.LogTail(context.Background(), "42", "40071", 8000)
	if err != nil || unavailable {
		t.Fatalf("LogTail: %v %v", err, unavailable)
	}
	if tail != strings.Repeat("y", 8000) {
		t.Errorf("tail = %d chars, first=%q", len(tail), tail[:1])
	}

	_, unavailable, err = col.LogTail(context.Background(), "42", "99999", 8000)
	if err != nil || !unavailable {
		t.Errorf("purged trace: unavailable=%v err=%v", unavailable, err)
	}
}

func TestGitLabRateLimitHonored(t *testing.T) {
	base := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	fake := &fakeGitLab{t: t, rateLimitOnce: true}
	fake.pipelines = []glPipeline{mkPipeline(1, ts(base, 1), "running")}
	col, rec := newTestCollector(t, fake)

	pages := collectAll(t, col, base.Add(-time.Hour))
	if len(pages[0].Runs) != 1 {
		t.Fatalf("runs = %+v", pages[0].Runs)
	}
	found := false
	for _, d := range rec.slept {
		if d == 7*time.Second {
			found = true
		}
	}
	if !found {
		t.Errorf("Retry-After 7 not honored exactly; slept %v", rec.slept)
	}
}

func TestLastCharsMultibyte(t *testing.T) {
	// A multibyte rune cut mid-sequence at the front must not corrupt output.
	s := strings.Repeat("ż", 10)
	raw := []byte(s)
	got := lastChars(raw[1:], 8) // drop one byte of the first two-byte rune
	if !strings.HasSuffix(got, "żżżż") {
		t.Errorf("lastChars mangled multibyte input: %q", got)
	}
	if got2 := lastChars([]byte("abcdef"), 3); got2 != "def" {
		t.Errorf("lastChars = %q", got2)
	}
}
