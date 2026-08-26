package gitlabci

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/trimci/agent/internal/backoff"
)

const (
	apiTimeout   = 30 * time.Second
	traceTimeout = 120 * time.Second
	perPage      = 100
	// rateLimitRetries bounds 429 honoring per logical call so a
	// misconfigured instance cannot pin the agent inside one request.
	rateLimitRetries = 5
	// defaultRetryAfter applies when a 429 carries no usable Retry-After.
	defaultRetryAfter = 60 * time.Second
	// maxRetryAfter caps how long a single Retry-After is honored.
	maxRetryAfter = 15 * time.Minute
)

// ErrUnauthorized means GitLab rejected the configured token (401/403).
var ErrUnauthorized = errors.New("gitlab rejected the configured token (check GITLAB_TOKEN scope read_api)")

// httpError is any other non-2xx GitLab response. The body is deliberately
// not captured: error payloads can quote request details, and the agent
// never logs response content.
type httpError struct {
	status int
	path   string
}

func (e *httpError) Error() string {
	return fmt.Sprintf("gitlab returned HTTP %d for %s", e.status, e.path)
}

// isNotFound reports whether err is a GitLab 404.
func isNotFound(err error) bool {
	var httpErr *httpError
	return errors.As(err, &httpErr) && httpErr.status == http.StatusNotFound
}

// client is the scope-guarded GitLab transport.
type client struct {
	baseURL   string
	token     string
	userAgent string
	api       *http.Client
	trace     *http.Client
	sleep     backoff.Sleeper
}

func newClient(baseURL, token, caBundle, userAgent string, sleep backoff.Sleeper) (*client, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if caBundle != "" {
		pool, err := x509.SystemCertPool()
		if err != nil {
			pool = x509.NewCertPool()
		}
		pem, err := os.ReadFile(caBundle)
		if err != nil {
			return nil, fmt.Errorf("reading GITLAB_CA_BUNDLE: %w", err)
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("GITLAB_CA_BUNDLE %q contains no usable PEM certificates", caBundle)
		}
		transport.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}
	return &client{
		baseURL:   baseURL,
		token:     token,
		userAgent: userAgent,
		api:       &http.Client{Timeout: apiTimeout, Transport: transport},
		trace:     &http.Client{Timeout: traceTimeout, Transport: transport},
		sleep:     sleep,
	}, nil
}

// do performs one scope-guarded GET, honoring 429 Retry-After exactly
// (bounded), and returns the raw response. The caller owns resp.Body.
func (c *client) do(ctx context.Context, httpClient *http.Client, path string, params url.Values) (*http.Response, error) {
	if err := enforceScope(path); err != nil {
		return nil, err
	}
	full := c.baseURL + path
	if len(params) > 0 {
		full += "?" + params.Encode()
	}
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, full, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+c.token)
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", c.userAgent)

		resp, err := httpClient.Do(req)
		if err != nil {
			return nil, err
		}
		switch {
		case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
			resp.Body.Close()
			return nil, ErrUnauthorized
		case resp.StatusCode == http.StatusTooManyRequests:
			resp.Body.Close()
			if attempt >= rateLimitRetries {
				return nil, &httpError{status: resp.StatusCode, path: path}
			}
			if err := c.sleep(ctx, retryAfterOf(resp)); err != nil {
				return nil, err
			}
			continue
		case resp.StatusCode >= 400:
			resp.Body.Close()
			return nil, &httpError{status: resp.StatusCode, path: path}
		}
		return resp, nil
	}
}

// getJSON GETs path and decodes the body into out.
func (c *client) getJSON(ctx context.Context, path string, params url.Values, out any) error {
	resp, err := c.do(ctx, c.api, path, params)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decoding %s: %w", path, err)
	}
	return nil
}

// getPage GETs one page of a list endpoint and reports whether GitLab
// advertises a next page (X-Next-Page — integers beat parsing RFC 5988).
func getPage[T any](ctx context.Context, c *client, path string, params url.Values, page int) (items []T, hasNext bool, err error) {
	withPage := url.Values{}
	for key, values := range params {
		withPage[key] = values
	}
	withPage.Set("per_page", strconv.Itoa(perPage))
	withPage.Set("page", strconv.Itoa(page))
	resp, err := c.do(ctx, c.api, path, withPage)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		return nil, false, fmt.Errorf("decoding %s page %d: %w", path, page, err)
	}
	return items, resp.Header.Get("X-Next-Page") != "", nil
}

func retryAfterOf(resp *http.Response) time.Duration {
	header := resp.Header.Get("Retry-After")
	if header == "" {
		header = resp.Header.Get("RateLimit-Reset-After")
	}
	seconds, err := strconv.Atoi(header)
	if err != nil || seconds <= 0 {
		return defaultRetryAfter
	}
	d := time.Duration(seconds) * time.Second
	if d > maxRetryAfter {
		return maxRetryAfter
	}
	return d
}

// readTail streams r keeping only enough bytes to reconstruct the last
// maxChars characters — a job trace can be tens of megabytes, and the agent
// only ever needs its tail in memory.
func readTail(r io.Reader, maxChars int) (string, error) {
	// UTF-8 is at most 4 bytes per rune; the slack covers a cut mid-rune.
	keep := maxChars*4 + 4
	buf := make([]byte, 0, keep+32*1024)
	chunk := make([]byte, 32*1024)
	for {
		n, err := r.Read(chunk)
		buf = append(buf, chunk[:n]...)
		if len(buf) > keep {
			copy(buf, buf[len(buf)-keep:])
			buf = buf[:keep]
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
	}
	return lastChars(buf, maxChars), nil
}

func lastChars(raw []byte, maxChars int) string {
	// The []rune round trip maps invalid bytes (including a cut mid-rune at
	// the buffer's front) to U+FFFD — valid UTF-8, JSON-encodable, and the
	// same replacement the server's own pull path produces when decoding.
	runes := []rune(string(raw))
	if len(runes) > maxChars {
		runes = runes[len(runes)-maxChars:]
	}
	return string(runes)
}
