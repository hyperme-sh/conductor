package businesstool

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
)

const authorizationPath = "/api/v1/aiuditor/workspace-connections/authorize"
const signaturePrefix = "aiuditor.workspace-connection.v1\n"

type authorityRequest struct {
	Version   int    `json:"version"`
	RequestID string `json:"request_id"`
	IssuedAt  int64  `json:"issued_at"`
	Binding
	Operation string `json:"operation"`
}

type Authority struct {
	endpoint *url.URL
	key      string
	client   *http.Client
}

func NewAuthority(origin, key string, transport http.RoundTripper) (*Authority, error) {
	endpoint, err := originURL(origin)
	if err != nil || len(key) < 32 {
		return nil, Invalid
	}
	endpoint.Path = authorizationPath
	return &Authority{endpoint, key, clientFor(transport)}, nil
}

func (a *Authority) Authorize(ctx context.Context, binding Binding, operation string) error {
	call := authorityRequest{1, uuid.NewString(), time.Now().Unix(), binding, operation}
	body, err := json.Marshal(call)
	if err != nil {
		return Denied
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, a.endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return Denied
	}
	hash := hmac.New(sha256.New, []byte(a.key))
	hash.Write([]byte(signaturePrefix))
	hash.Write(body)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Workspace-Connection-Signature", "sha256="+hex.EncodeToString(hash.Sum(nil)))
	var decision struct {
		Version   int    `json:"version"`
		RequestID string `json:"request_id"`
		Allowed   bool   `json:"allowed"`
		Reason    string `json:"reason"`
	}
	if getJSON(a.client, request, 4096, &decision) != nil ||
		decision.Version != 1 || decision.RequestID != call.RequestID || !decision.Allowed {
		return Denied
	}
	return nil
}

func originURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery ||
		parsed.Fragment != "" || parsed.RawPath != "" || (parsed.Path != "" && parsed.Path != "/") {
		return nil, Invalid
	}
	loopback := parsed.Hostname() == "127.0.0.1" || parsed.Hostname() == "::1"
	if parsed.Scheme != "https" && !(loopback && parsed.Scheme == "http") {
		return nil, Invalid
	}
	return parsed, nil
}

func clientFor(transport http.RoundTripper) *http.Client {
	if transport == nil {
		// Do not proxy private provider requests through ambient proxy settings.
		transport = &http.Transport{TLSHandshakeTimeout: 4 * time.Second}
	}
	return &http.Client{
		Transport: transport, Timeout: 8 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func getJSON(client *http.Client, request *http.Request, limit int64, into any) error {
	request.Header.Set("Accept", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return Unavailable
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return NotFound
	case http.StatusUnauthorized:
		return Revoked
	case http.StatusForbidden:
		return Denied
	case http.StatusTooManyRequests:
		return Throttled
	default:
		return Unavailable
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil || int64(len(raw)) > limit || !strings.HasPrefix(strings.TrimSpace(string(raw)), "{") || json.Unmarshal(raw, into) != nil {
		return Unavailable
	}
	return nil
}
