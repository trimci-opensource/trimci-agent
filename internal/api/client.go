package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

// requestTimeout bounds one ingest round trip. The server processes runs
// batches synchronously inside a 120 s worker budget, so the client allows
// a little more than that before giving up.
const requestTimeout = 150 * time.Second

// maxErrorBody caps how much of an error response is read. Error envelopes
// are tiny; anything bigger is a proxy page we only need a sniff of.
const maxErrorBody = 64 * 1024

// APIError is a non-2xx response from the ingest API, carrying the stable
// machine code from the error envelope (empty when the body was not the
// envelope — e.g. a proxy-generated 502 page).
type APIError struct {
	Status     int
	Code       string
	Message    string
	RetryAfter time.Duration // from the Retry-After header, 0 if absent
}

func (e *APIError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("ingest API %d %s: %s", e.Status, e.Code, e.Message)
	}
	return fmt.Sprintf("ingest API %d", e.Status)
}

// IsCode reports whether err is an *APIError with the given code.
func IsCode(err error, code string) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Code == code
}

// Status returns the HTTP status of an *APIError, or 0 for transport errors.
func Status(err error) int {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.Status
	}
	return 0
}

// RetryAfter returns the server-provided Retry-After, or 0.
func RetryAfter(err error) time.Duration {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.RetryAfter
	}
	return 0
}

// Client is the TrimCI ingest transport. It is deliberately policy-free:
// every request is a single attempt, and all retry/backoff/halving decisions
// live in the agent loop where batch composition is known.
type Client struct {
	baseURL   string
	token     string
	userAgent string
	http      *http.Client
}

// NewClient builds a client for the given origin (e.g. https://trimci.com).
func NewClient(baseURL, token, userAgent string) *Client {
	return &Client{
		baseURL:   baseURL,
		token:     token,
		userAgent: userAgent,
		// The default transport honors HTTPS_PROXY / NO_PROXY from the
		// environment — required in locked-down networks.
		http: &http.Client{Timeout: requestTimeout},
	}
}

// Hello posts the heartbeat/status body and returns the control plane.
func (c *Client) Hello(ctx context.Context, req HelloRequest) (*HelloResponse, error) {
	req.Protocol = ProtocolVersion
	var out HelloResponse
	if err := c.post(ctx, "/ingest/v1/hello/", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// PushCatalog pushes the project catalog.
func (c *Client) PushCatalog(ctx context.Context, req CatalogRequest) (*CatalogResponse, error) {
	req.Protocol = ProtocolVersion
	var out CatalogResponse
	if err := c.post(ctx, "/ingest/v1/catalog/", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// PushRuns pushes one runs batch for one repository.
func (c *Client) PushRuns(ctx context.Context, req RunsRequest) (*RunsResponse, error) {
	req.Protocol = ProtocolVersion
	if req.Runs == nil {
		req.Runs = []Run{} // "runs" must be a JSON list even when empty (the empty final batch)
	}
	var out RunsResponse
	if err := c.post(ctx, "/ingest/v1/runs/", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// PushLogs pushes log tails for one repository.
func (c *Client) PushLogs(ctx context.Context, req LogsRequest) (*LogsResponse, error) {
	req.Protocol = ProtocolVersion
	var out LogsResponse
	if err := c.post(ctx, "/ingest/v1/logs/", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// post marshals body, POSTs it and decodes a 2xx response into out.
//
// Paths keep their trailing slash on purpose: the endpoints are mounted
// slash-terminated, and Django's APPEND_SLASH redirect does not preserve a
// POST body — a slashless URL would quietly lose the payload.
func (c *Client) post(ctx context.Context, path string, body any, out any) error {
	encoded, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("encoding %s body: %w", path, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(encoded))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("X-TrimCI-Agent", c.userAgent)

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("decoding %s response: %w", path, err)
		}
		return nil
	}

	apiErr := &APIError{Status: resp.StatusCode}
	if seconds, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && seconds > 0 {
		apiErr.RetryAfter = time.Duration(seconds) * time.Second
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	var envelope errorEnvelope
	if json.Unmarshal(raw, &envelope) == nil {
		apiErr.Code = envelope.Error.Code
		apiErr.Message = envelope.Error.Message
	}
	return apiErr
}
