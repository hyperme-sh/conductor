package spendlimit

import (
	"testing"
	"time"
)

func TestMaximum(t *testing.T) {
	p := Price{Currency: "USD", Model: "model", Endpoint: "https://provider", Source: "provider maximum", Expires: 200, Input: 1500000, Output: 3000000, Fixed: 2}
	for _, tc := range []struct {
		in, out int
		want    int64
	}{{1, 1, 7}, {0, 0, 2}, {2, 2, 11}} {
		got, e := p.Maximum(tc.in, tc.out, time.Unix(100, 0))
		if e != nil || got != tc.want {
			t.Fatalf("%+v: %d %v", tc, got, e)
		}
	}
	if _, e := p.Maximum(1, 1, time.Unix(200, 0)); e == nil {
		t.Fatal("expired price accepted")
	}
	p.Input = 1e12
	if _, e := p.Maximum(1000000000, 1, time.Unix(100, 0)); e == nil {
		t.Fatal("overflow accepted")
	}
}
