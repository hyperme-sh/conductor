package proxy

import (
	"crypto/tls"
	"github.com/hyperme-sh/hypermesh-conductor/internal/config"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPhoneIngressDoesNotExposeOperatorRoutes(t *testing.T) {
	calls := 0
	p := &Proxy{Config: config.Config{ListenMode: config.ListenModeNetwork}, PhoneIngress: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		// Dedicated handler performs enrolled-phone authentication; no operator
		// bearer is accepted or injected by the network router.
		if r.Header.Get("Authorization") != "Bearer hm_msi_phone" {
			t.Error("credential changed")
		}
		w.WriteHeader(http.StatusNoContent)
	})}
	for _, path := range []string{"/v1/phone/prompt-submit", "/v1/chat/completions", "/v1/machine/prompt-submit"} {
		r := httptest.NewRequest(http.MethodPost, "https://10.0.0.2"+path, nil)
		r.TLS = &tls.ConnectionState{}
		r.Header.Set("Authorization", "Bearer hm_msi_phone")
		w := httptest.NewRecorder()
		p.ServeHTTP(w, r)
		want := http.StatusUnauthorized
		if path == "/v1/phone/prompt-submit" {
			want = http.StatusNoContent
		}
		if w.Code != want {
			t.Fatalf("%s: got %d, want %d", path, w.Code, want)
		}
	}
	if calls != 1 {
		t.Fatal(calls)
	}
}
