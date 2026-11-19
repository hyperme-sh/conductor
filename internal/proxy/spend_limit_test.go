package proxy

import (
	"context"
	"fmt"
	"github.com/hyperme-sh/hypermesh-conductor/internal/config"
	"github.com/hyperme-sh/hypermesh-conductor/internal/spendlimit"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestMonetaryAdmissionBeforeGeneration(t *testing.T) {
	calls := 0
	counts := 0
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/tokenize" {
			counts++
			fmt.Fprint(w, `{"count":10,"max_model_len":256}`)
			return
		}
		calls++
		fmt.Fprint(w, `{"choices":[{"message":{"content":"done"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2}}`)
	}))
	defer provider.Close()
	dir := t.TempDir()
	cfg := loadTestConfig(t, dir, provider.URL+"/v1", provider.URL+"/v1", config.AuthPassthrough, config.ProviderOpenAICompatible)
	cfg.SpendPrices = map[string]spendlimit.Price{}
	for _, tier := range []*config.Tier{&cfg.Cheap, &cfg.Frontier} {
		tier.InputCounter = "vllm"
		tier.OutputLimit = 16
		tier.ContextWindow = 256
		cfg.SpendPrices[tier.Name] = spendlimit.Price{Currency: "USD", Model: tier.Model, Endpoint: tier.BaseURL, Source: "fixture maximum", Expires: time.Now().Unix() + 300, Input: 1000000, Output: 2000000}
	}
	invoke := func(allow bool) int {
		req := request("/v1/chat/completions", "code", "task", "step", "", `{"messages":[{"role":"user","content":"code"}],"n":2,"max_tokens":3}`)
		req.Header.Set("Authorization", "Bearer fixture")
		req = req.WithContext(spendlimit.With(req.Context(), spendlimit.Admission{Currency: "USD", Reserve: func(_ context.Context, amount int64, source string) error {
			if amount != 576 || source != "fixture maximum" {
				t.Fatalf("incorrect bound %d %s", amount, source)
			}
			if !allow {
				return spendlimit.Err
			}
			return nil
		}}))
		w := httptest.NewRecorder()
		New(cfg, nil, openJournal(t, dir)).ServeHTTP(w, req)
		return w.Code
	}
	if code := invoke(false); code != 409 || calls != 0 || counts != 0 {
		t.Fatalf("unfunded dispatch %d %d", code, calls)
	}
	if code := invoke(true); code != 200 || calls != 1 {
		t.Fatalf("funded dispatch %d %d", code, calls)
	}
	cfg.SpendPrices = nil
	if code := invoke(true); code != 503 || calls != 1 {
		t.Fatalf("unknown price dispatched %d %d", code, calls)
	}
}
