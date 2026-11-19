package proxy

import (
	"github.com/hyperme-sh/hypermesh-conductor/internal/config"
	"github.com/hyperme-sh/hypermesh-conductor/internal/spendlimit"
	"github.com/hyperme-sh/hypermesh-conductor/internal/translate"
	"net/http"
	"time"
)

type monetaryBound struct {
	tier                      config.Tier
	price                     spendlimit.Price
	input, output, candidates int
}
type monetaryError struct {
	status  int
	message string
}

func (p *Proxy) reserveMoney(r *http.Request, protocol string, body []byte, tier config.Tier) (monetaryBound, bool, *monetaryError) {
	a, limited := spendlimit.From(r.Context())
	if !limited {
		return monetaryBound{}, false, nil
	}
	unavailable := &monetaryError{503, "authoritative monetary bound unavailable"}
	price, known := p.Config.SpendPrices[tier.Name]
	if !known || price.Currency != a.Currency || price.Model != tier.Model || price.Endpoint != tier.BaseURL || protocol != translate.ProtoChat || tier.InputCounter != "vllm" || tier.ContextWindow < 1 || tier.ContextWindow > 1000000000 || tier.OutputLimit < 1 {
		return monetaryBound{}, true, unavailable
	}
	_, allowance, err := boundedOutput(protocol, body, tier.OutputLimit)
	if err != nil || allowance.candidates < 1 || tier.ContextWindow > 1000000000/allowance.candidates || tier.OutputLimit > 1000000000/allowance.candidates {
		return monetaryBound{}, true, unavailable
	}
	b := monetaryBound{tier: tier, price: price, input: tier.ContextWindow * allowance.candidates, output: tier.OutputLimit * allowance.candidates, candidates: allowance.candidates}
	amount, err := price.Maximum(b.input, b.output, time.Now())
	if err != nil {
		return monetaryBound{}, true, unavailable
	}
	if a.Reserve == nil || a.Reserve(r.Context(), amount, price.Source) != nil {
		return monetaryBound{}, true, &monetaryError{409, "monetary budget unavailable or exhausted"}
	}
	return b, true, nil
}
func (b monetaryBound) accepts(tier config.Tier, count int, allowance generationAllowance) bool {
	return tier.Name == b.tier.Name && tier.Model == b.tier.Model && tier.BaseURL == b.tier.BaseURL && time.Now().Unix() < b.price.Expires && count >= 0 && allowance.candidates > 0 && allowance.candidates <= b.candidates && count <= b.input/allowance.candidates && allowance.output <= b.output
}
