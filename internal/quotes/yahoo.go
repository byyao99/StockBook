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
		Result []chartResult `json:"result"`
		Error  *struct {
			Code        string `json:"code"`
			Description string `json:"description"`
		} `json:"error"`
	} `json:"chart"`
}

// chartResult is one symbol's payload. Meta describes the instrument and its
// latest price; Timestamp and Indicators carry the series, which is empty unless
// the request asked for a range.
//
// Close is []*float64 rather than []float64 because the provider reports a
// missing bar as a null rather than omitting it — a halted session, or a day the
// exchange was closed but the series still spans. Reading those as 0.0 would
// record a stock as worthless for a day, so the pointer is what keeps "no bar"
// distinguishable from "a bar at zero".
type chartResult struct {
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
	Timestamp  []int64 `json:"timestamp"`
	Indicators struct {
		Quote []struct {
			Close []*float64 `json:"close"`
		} `json:"quote"`
	} `json:"indicators"`
}

// Fetch returns the current quote for one Yahoo ticker.
//
// Failures carry the provider's own wording where there is any: a delisted
// symbol comes back as Yahoo described it, so the operator sees the real reason
// rather than a flattened "fetch failed".
func (c *Client) Fetch(ctx context.Context, ticker string) (Quote, error) {
	result, err := c.chart(ctx, ticker, "interval=1d&range=1d")
	if err != nil {
		return Quote{}, err
	}

	meta := result.Meta
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

// chart performs one call to the chart endpoint and returns the single result
// it carries. query is the request's own parameters — a bare range for a live
// quote, a period window for history — and everything else about talking to the
// provider is the same either way, which is why both callers share this.
func (c *Client) chart(ctx context.Context, ticker, query string) (chartResult, error) {
	endpoint := fmt.Sprintf("%s/v8/finance/chart/%s?%s",
		c.baseURL, url.PathEscape(ticker), query)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return chartResult{}, err
	}
	// The endpoint returns 403 to clients without a browser-like User-Agent.
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; StockBook/1.0)")

	res, err := c.http.Do(req)
	if err != nil {
		return chartResult{}, err
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
		return chartResult{}, fmt.Errorf("%w for %s: %s", ErrNoQuote, ticker, desc)
	}
	if res.StatusCode != http.StatusOK {
		return chartResult{}, fmt.Errorf("quote provider returned %s", res.Status)
	}
	if decodeErr != nil {
		return chartResult{}, fmt.Errorf("decoding quote response: %w", decodeErr)
	}
	if len(body.Chart.Result) == 0 {
		return chartResult{}, fmt.Errorf("%w: provider returned no result for %s", ErrNoQuote, ticker)
	}
	return body.Chart.Result[0], nil
}

// DailyClose is one trading day's closing price.
//
// Date is the calendar day as YYYY-MM-DD rather than a timestamp, because that
// is the granularity the data actually has: a daily bar belongs to a session,
// not to an instant, and carrying a time would invite comparisons that depend on
// the reader's zone. It is also how the rest of the system already names a day.
type DailyClose struct {
	Date  string
	Close int64
}

// History is a run of daily closes for one ticker, with the currency they are
// quoted in.
//
// Currency comes along for the same reason Quote carries it: a series adopted
// into an instrument quoted in something else would reinterpret its whole
// history, so the caller has to be able to check before storing.
type History struct {
	Currency models.Currency
	Closes   []DailyClose
}

// History fetches daily closing prices for ticker over [from, to].
//
// The window is inclusive of both ends as far as the provider allows: period2 is
// pushed to the end of `to` because Yahoo treats it as an exclusive instant, and
// a bare date would drop the last session — the same trap a bare `to` date
// springs on the reports.
//
// Bars the provider reports as null are skipped rather than read as zero, and a
// window with no sessions in it at all (a request spanning only a weekend) comes
// back as an empty slice with no error: nothing arrived, but nothing is wrong.
func (c *Client) History(ctx context.Context, ticker string, from, to time.Time) (History, error) {
	if to.Before(from) {
		return History{}, fmt.Errorf("history window ends (%s) before it starts (%s)",
			to.Format(time.DateOnly), from.Format(time.DateOnly))
	}
	query := fmt.Sprintf("interval=1d&period1=%d&period2=%d",
		from.Unix(), to.Add(24*time.Hour).Unix())

	result, err := c.chart(ctx, ticker, query)
	if err != nil {
		return History{}, err
	}
	currency, ok := models.CanonicalCurrency(result.Meta.Currency)
	if !ok {
		return History{}, fmt.Errorf("unsupported quote currency %q for %s",
			result.Meta.Currency, ticker)
	}
	if len(result.Indicators.Quote) == 0 {
		return History{}, fmt.Errorf("%w: provider returned no series for %s", ErrNoQuote, ticker)
	}

	closes := result.Indicators.Quote[0].Close
	out := make([]DailyClose, 0, len(result.Timestamp))
	for i, ts := range result.Timestamp {
		if i >= len(closes) || closes[i] == nil || *closes[i] <= 0 {
			continue
		}
		out = append(out, DailyClose{
			Date:  barDate(ts),
			Close: toMinorUnits(*closes[i]),
		})
	}
	return History{Currency: currency, Closes: out}, nil
}

// barDate names the session a daily bar belongs to.
//
// The provider stamps a daily bar at the session's opening instant in exchange
// local time, and for every market this system addresses that instant falls on
// the same UTC calendar day as the session itself: Taiwan opens at 01:00 UTC and
// New York at 13:30 or 14:30 UTC. So the UTC date is the trading date, and no
// exchange calendar is needed to work it out. A market opening before 00:00 UTC
// would break that, and none of the supported ones does.
func barDate(unix int64) string {
	return time.Unix(unix, 0).UTC().Format(time.DateOnly)
}

// toMinorUnits converts a decimal price to integer hundredths, rounding half
// away from zero. The provider speaks floats; everything past this boundary is
// an exact integer.
func toMinorUnits(price float64) int64 {
	return int64(math.Round(price * 100))
}
