package gitlabci

import "testing"

func TestScopeAllowlist(t *testing.T) {
	allowed := []string{
		"/api/v4/version",
		"/api/v4/projects",
		"/api/v4/projects/42",
		"/api/v4/projects/42/pipelines",
		"/api/v4/projects/42/pipelines/9912",
		"/api/v4/projects/42/pipelines/9912/jobs",
		"/api/v4/projects/42/jobs/40071/trace",
	}
	for _, path := range allowed {
		if err := enforceScope(path); err != nil {
			t.Errorf("enforceScope(%q) refused: %v", path, err)
		}
	}

	refused := []string{
		// The server-side allowlist entries this agent deliberately DROPS:
		"/api/v4/user",
		"/api/v4/personal_access_tokens/self",
		"/api/v4/projects/42/jobs/40071", // bare single-job endpoint
		// The zero-code-access boundary itself:
		"/api/v4/projects/42/repository/files/settings.py/raw",
		"/api/v4/projects/42/repository/commits",
		"/api/v4/projects/42/merge_requests",
		"/api/v4/projects/42/snippets",
		"/api/v4/groups",
		"/api/v4/projects/42/pipelines/9912/variables",
		"/api/v4/projects/42/pipelines/../../user",
		"/api/v4/projectsX",
		"/api/v4/projects/42/pipelines/9912/jobs/extra",
	}
	for _, path := range refused {
		if err := enforceScope(path); err == nil {
			t.Errorf("enforceScope(%q) allowed — outside the zero-code-access scope", path)
		}
	}
}
