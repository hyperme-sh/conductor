package permissiondream

import (
	"context"
	"errors"
	"github.com/hyperme-sh/hypermesh-conductor/internal/localtransport"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// LocalBatch sends no credentials or action data to the same-user Visor peer.
// Visor owns the snapshot and pending ID; Host and AIuditor verify authority.
func LocalBatch(socket string) Batch {
	return func(ctx context.Context) error {
		transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return localtransport.DialContext(ctx, socket)
		}}
		defer transport.CloseIdleConnections()
		client := http.Client{Transport: transport, Timeout: 45 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		request, err := http.NewRequestWithContext(ctx, "POST", "http://localhost/permissions/dream", strings.NewReader("{}"))
		if err != nil {
			return err
		}
		request.Header.Set("Content-Type", "application/json")
		response, err := client.Do(request)
		if err != nil {
			return errors.New("permission dreaming unavailable")
		}
		defer response.Body.Close()
		_, err = io.Copy(io.Discard, io.LimitReader(response.Body, 131073))
		if err != nil || response.StatusCode != 200 {
			return errors.New("permission dreaming unavailable")
		}
		return nil
	}
}
