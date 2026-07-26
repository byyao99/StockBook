// Package quotes fetches market prices from Yahoo Finance.
//
// Yahoo has no official public API — this uses the undocumented endpoint behind
// their charts, which needs no key but is unsupported and has broken before. If
// it stops working, replacing this file with another provider is the whole job:
// nothing outside this package knows where prices come from.
package quotes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	"stockbook/internal/models"
)

// ErrNoQuote is returned when the provider has no price for a symbol — a
// delisted ticker, a bad mapping, or a market that has never traded it.
var ErrNoQuote = errors.New("no quote available")

// ErrUnsupportedMarket is returned for instruments this provider cannot address,
// which is anything outside the markets Ticker knows how to name.
var ErrUnsupportedMarket = errors.New("market is not supported by this quote provider")

const defaultBaseURL = "https://query1.finance.yahoo.com"

// Quote is one fetched price together with what the provider knows about the
// instrument behind it.
//
// Price is in minor units (hundredths) to match the rest of the system, and
// AsOf is the market timestamp the provider reported rather than the moment we
// asked — so staleness shown to a user reflects the price's own age.
//
// Name, Exchange and Type come along because a single fetch is then enough to
// describe an instrument completely: the create path uses them so that the name
// and currency on file are the provider's, not a caller's claim, and so that an
// index or a currency pair is refused for being unholdable rather than for
// happening to trade somewhere unrecognized.
//
// Type is the provider's own word for what the instrument is ("EQUITY", "ETF",
// "INDEX", "CRYPTOCURRENCY"…) and may be empty if it stops reporting one; see
// IsHoldable for how that is treated.
type Quote struct {
	Price    int64
	Currency models.Currency
	AsOf     time.Time
	Name     string
	Exchange string
	Type     string
}

// Client fetches quotes from Yahoo Finance.
type Client struct {
	http    *http.Client
	baseURL string
}

// NewClient returns a Client with a sensible per-request timeout.
func NewClient() *Client {
	return &Client{
		http:    &http.Client{Timeout: 10 * time.Second},
		baseURL: defaultBaseURL,
	}
}

// newTestClient points a Client at a stub server; used by the tests so no
// network call is ever made.
func newTestClient(baseURL string) *Client {
	return &Client{http: &http.Client{Timeout: 5 * time.Second}, baseURL: baseURL}
}

// marketSuffix is the suffix the provider names a market's listings with, and
// the single source both directions come from: Ticker appends it and FromTicker
// strips it. Deriving both from one table is what stops the round trip being
// broken by editing only one side of it.
//
// US listings are absent because they carry no suffix — which is exactly what
// FromTicker uses to recognize them.
var marketSuffix = map[string]string{
	"TWSE": ".TW",
	"TPEX": ".TWO",
}

// Ticker maps an instrument to the symbol Yahoo knows it by: Taiwan listings
// take a market suffix, US listings are used as-is. It reports false for
// markets this provider cannot address.
func Ticker(symbol, market string) (string, bool) {
	if suffix, ok := marketSuffix[market]; ok {
		return symbol + suffix, true
	}
	switch market {
	case "NYSE", "NASDAQ":
		return symbol, true
	default:
		return "", false
	}
}

// ExchangeSuffix reports whether symbol already ends in a market suffix that
// Ticker would append on its own.
//
// It answers a narrow question — "is this one of ours?" — and deliberately not
// the broader "does this ticker belong to some exchange?". A ".DE" or ".L"
// ticker is not one of ours, and FromTicker rejects it by a different rule.
//
// It exists so a pasted provider ticker can be caught when the instrument is
// created rather than when a quote is first fetched: "2330.TW" filed under TWSE
// would otherwise be looked up as "2330.TW.TW", and the only symptom would be a
// missing quote long after the mistake was made.
//
// Iteration order does not matter: no suffix in marketSuffix is a suffix of
// another (".TW" does not match "2330.TWO"), so at most one can ever apply.
func ExchangeSuffix(symbol string) (string, bool) {
	upper := strings.ToUpper(symbol)
	for _, suffix := range marketSuffix {
		if strings.HasSuffix(upper, suffix) {
			return suffix, true
		}
	}
	return "", false
}

// chartResponse is the slice of Yahoo's payload this package reads. Their error
// object carries a human-readable description, which is passed through to the
// caller rather than replaced with a generic message — when a fetch fails, what
// the provider said about it is the only useful diagnostic.
type chartResponse struct {
	Chart struct {
		Result []struct {
			Meta struct {
				Symbol             string  `json:"symbol"`
				Currency           string  `json:"currency"`
				RegularMarketPrice float64 `json:"regularMarketPrice"`
				RegularMarketTime  int64   `json:"regularMarketTime"`
				ShortName          string  `json:"shortName"`
				LongName           string  `json:"longName"`
				ExchangeName       string  `json:"exchangeName"`
				InstrumentType     string  `json:"instrumentType"`
			} `json:"meta"`
		} `json:"result"`
		Error *struct {
			Code        string `json:"code"`
			Description string `json:"description"`
		} `json:"error"`
	} `json:"chart"`
}

// Fetch returns the current quote for one Yahoo ticker.
//
// Failures carry the provider's own wording where there is any: a delisted
// symbol comes back as Yahoo described it, so the operator sees the real reason
// rather than a flattened "fetch failed".
func (c *Client) Fetch(ctx context.Context, ticker string) (Quote, error) {
	endpoint := fmt.Sprintf("%s/v8/finance/chart/%s?interval=1d&range=1d",
		c.baseURL, url.PathEscape(ticker))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return Quote{}, err
	}
	// The endpoint returns 403 to clients without a browser-like User-Agent.
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; StockBook/1.0)")

	res, err := c.http.Do(req)
	if err != nil {
		return Quote{}, err
	}
	defer res.Body.Close()

	var body chartResponse
	decodeErr := json.NewDecoder(res.Body).Decode(&body)

	// Yahoo reports a missing symbol as a 404 whose body still explains why, so
	// check the payload's error before falling back to the status code.
	//
	// The ticker is named in the message because it is derived from the
	// instrument's market, and a wrong market is the most common cause of a
	// lookup failing: "no quote for 2330.TWO" tells an operator that the symbol
	// was filed under TPEX when it trades on TWSE, which "symbol may be
	// delisted" on its own does not.
	if body.Chart.Error != nil {
		desc := strings.TrimSpace(body.Chart.Error.Description)
		if desc == "" {
			desc = body.Chart.Error.Code
		}
		return Quote{}, fmt.Errorf("%w for %s: %s", ErrNoQuote, ticker, desc)
	}
	if res.StatusCode != http.StatusOK {
		return Quote{}, fmt.Errorf("quote provider returned %s", res.Status)
	}
	if decodeErr != nil {
		return Quote{}, fmt.Errorf("decoding quote response: %w", decodeErr)
	}
	if len(body.Chart.Result) == 0 {
		return Quote{}, fmt.Errorf("%w: provider returned no result for %s", ErrNoQuote, ticker)
	}

	meta := body.Chart.Result[0].Meta
	if meta.RegularMarketPrice == 0 {
		return Quote{}, fmt.Errorf("%w: provider returned no price for %s", ErrNoQuote, ticker)
	}
	currency, ok := models.CanonicalCurrency(meta.Currency)
	if !ok {
		return Quote{}, fmt.Errorf("unsupported quote currency %q for %s", meta.Currency, ticker)
	}

	name := meta.LongName
	if name == "" {
		name = meta.ShortName
	}
	return Quote{
		Price:    toMinorUnits(meta.RegularMarketPrice),
		Currency: currency,
		AsOf:     time.Unix(meta.RegularMarketTime, 0),
		Name:     strings.TrimSpace(name),
		Exchange: strings.ToUpper(meta.ExchangeName),
		Type:     strings.ToUpper(strings.TrimSpace(meta.InstrumentType)),
	}, nil
}

// toMinorUnits converts a decimal price to integer hundredths, rounding half
// away from zero. The provider speaks floats; everything past this boundary is
// an exact integer.
func toMinorUnits(price float64) int64 {
	return int64(math.Round(price * 100))
}
