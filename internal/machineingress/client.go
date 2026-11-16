package machineingress

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/hyperme-sh/hypermesh-conductor/internal/localtransport"
)

// Submit sends the exact owner-authorized bytes over the verified private socket.
// It never retries, follows redirects, or substitutes an HTTP network endpoint.
func Submit(ctx context.Context, socket, credential string, raw []byte) (json.RawMessage, error) {
	if !strings.HasPrefix(credential, "hm_msi_") || len(credential) > 4096 || strings.ContainsAny(credential, "\r\n") || len(raw) > 96<<10 || !json.Valid(raw) {
		return nil, refused
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return localtransport.DialContext(ctx, socket)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	request, e := http.NewRequestWithContext(ctx, "POST", "http://conductor.local/v1/machine/prompt-submit", bytes.NewReader(raw))
	if e != nil {
		return nil, refused
	}
	request.Header.Set("Authorization", "Bearer "+credential)
	request.Header.Set("Content-Type", "application/json")
	response, e := client.Do(request)
	if e != nil {
		return nil, refused
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return nil, refused
	}
	body, e := io.ReadAll(io.LimitReader(response.Body, 262145))
	if e != nil || len(body) > 262144 || !json.Valid(body) {
		return nil, refused
	}
	return body, nil
}
