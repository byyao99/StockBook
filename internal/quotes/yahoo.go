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

// Quote is one fetched price.
//
// Price is in minor units (hundredths) to match the rest of the system, and
// AsOf is the market timestamp the provider reported rather than the moment we
// asked — so staleness shown to a user reflects the price's own age.
type Quote struct {
	Price    int64
	Currency models.Currency
	AsOf     time.Time
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

// Ticker maps an instrument to the symbol Yahoo knows it by: Taiwan listings
// take a market suffix, US listings are used as-is. It reports false for
// markets this provider cannot address.
func Ticker(symbol, market string) (string, bool) {
	switch market {
	case "TWSE":
		return symbol + ".TW", true
	case "TPEX":
		return symbol + ".TWO", true
	case "NYSE", "NASDAQ":
		return symbol, true
	default:
		return "", false
	}
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
	if body.Chart.Error != nil {
		desc := strings.TrimSpace(body.Chart.Error.Description)
		if desc == "" {
			desc = body.Chart.Error.Code
		}
		return Quote{}, fmt.Errorf("%w: %s", ErrNoQuote, desc)
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

	return Quote{
		Price:    toMinorUnits(meta.RegularMarketPrice),
		Currency: currency,
		AsOf:     time.Unix(meta.RegularMarketTime, 0),
	}, nil
}

// toMinorUnits converts a decimal price to integer hundredths, rounding half
// away from zero. The provider speaks floats; everything past this boundary is
// an exact integer.
func toMinorUnits(price float64) int64 {
	return int64(math.Round(price * 100))
}
