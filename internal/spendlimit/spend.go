// Package spendlimit carries owner-bound, integer monetary admission to dispatch.
package spendlimit

import (
	"context"
	"errors"
	"math/big"
	"regexp"
	"time"
)

var Err = errors.New("authoritative monetary bound unavailable or exhausted")

type Price struct {
	Currency string `json:"currency"`
	Model    string `json:"model"`
	Endpoint string `json:"endpoint"`
	Source   string `json:"source"`
	Expires  int64  `json:"expires_at"`
	Input    int64  `json:"input_micros_per_million"`
	Output   int64  `json:"output_micros_per_million"`
	Fixed    int64  `json:"fixed_micros"`
}

func (p Price) Valid() bool {
	return regexp.MustCompile(`^[A-Z]{3}$`).MatchString(p.Currency) && p.Model != "" && p.Endpoint != "" && len(p.Source) > 0 && len(p.Source) <= 512 && p.Expires > 0 && p.Input >= 0 && p.Input <= 1e12 && p.Output >= 0 && p.Output <= 1e12 && p.Fixed >= 0 && p.Fixed <= 1e12
}
func (p Price) Maximum(input, output int, now time.Time) (int64, error) {
	if !p.Valid() || now.Unix() >= p.Expires || input < 0 || output < 0 {
		return 0, Err
	}
	n := new(big.Int).Mul(big.NewInt(int64(input)), big.NewInt(p.Input))
	n.Add(n, new(big.Int).Mul(big.NewInt(int64(output)), big.NewInt(p.Output)))
	n.Add(n, big.NewInt(999999))
	n.Div(n, big.NewInt(1000000))
	n.Add(n, big.NewInt(p.Fixed))
	if !n.IsInt64() || n.Int64() > 1e12 {
		return 0, Err
	}
	return n.Int64(), nil
}

type Admission struct {
	Currency string
	Reserve  func(context.Context, int64, string) error
}
type key struct{}

func With(ctx context.Context, a Admission) context.Context { return context.WithValue(ctx, key{}, a) }
func From(ctx context.Context) (Admission, bool)            { a, ok := ctx.Value(key{}).(Admission); return a, ok }
