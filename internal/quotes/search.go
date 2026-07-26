package quotes

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"stockbook/internal/models"
)

// SearchResult is one instrument the provider knows about, already translated
// into this system's own vocabulary.
//
// Symbol is the bare symbol to store — the provider's exchange suffix has been
// stripped, because the suffix belongs to the market and is re-derived from it.
// Ticker keeps the provider's full form for display, so it is obvious which
// listing a result refers to when several exchanges carry the same number.
type SearchResult struct {
	Symbol   string
	Name     string
	Market   string
	Currency models.Currency
	Ticker   string
}

// twMarkets maps the provider's Taiwan exchange codes to this system's markets.
// Taiwan is enumerated because the suffix is load-bearing there: it is appended
// on every lookup, so a listing filed under the wrong one of these two is
// re-fetched under a ticker that does not exist.
var twMarkets = map[string]string{
	"TAI": "TWSE",
	"TWO": "TPEX",
}

// nasdaqExchanges are the provider's codes for the Nasdaq tiers. They exist only
// to name a US listing accurately; nothing about pricing depends on them, since
// every US listing is looked up by its bare symbol either way.
var nasdaqExchanges = map[string]bool{
	"NMS": true, // Global Select
	"NGM": true, // Global Market
	"NCM": true, // Capital Market
	"NAS": true,
}

// holdableTypes are the instrument kinds that can be owned as shares. Indices,
// futures, currency pairs and mutual funds come back from the same endpoints and
// are refused.
var holdableTypes = map[string]bool{
	"EQUITY": true,
	"ETF":    true,
}

// IsHoldable reports whether the provider's word for an instrument's kind
// describes something this system can hold.
//
// An empty kind passes. The provider not saying what something is has to be
// treated as no information rather than as a refusal, or a change on their side
// would lock every instrument out of the system at once; the checks that follow
// — a supported currency, a market this system can name — still apply.
func IsHoldable(kind string) bool {
	kind = strings.ToUpper(strings.TrimSpace(kind))
	return kind == "" || holdableTypes[kind]
}

// usMarket names the market of a US listing. The Nasdaq tiers are named exactly;
// every other US venue — NYSE Arca (PCX), Cboe (BTS), NYSE American (ASE), and
// whatever the provider codes next — falls back to NYSE.
//
// The fallback is the point. Market here decides how an instrument is priced and
// how it is labelled, and for a US listing the pricing half is identical
// whichever venue it trades on, so an unrecognized code is a labelling
// imprecision rather than a reason to refuse a real holding. Enumerating venues
// instead is what kept every NYSE Arca ETF — VOO, SPY, VTI, IWM — out of the
// system entirely.
func usMarket(exchange string) string {
	if nasdaqExchanges[strings.ToUpper(exchange)] {
		return "NASDAQ"
	}
	return "NYSE"
}

// FromTicker translates a provider ticker and exchange code into the bare symbol
// and market this system stores. It is the inverse of Ticker, and reports false
// for a listing this system cannot address.
//
// The question it asks is not "do we recognize this exchange?" but "can we look
// this up again exactly as the provider names it?", which is what actually
// decides whether an instrument will ever price:
//
//   - a suffix this package appends itself means Taiwan, and the suffix must
//     agree with the exchange reported alongside it;
//   - any other suffix belongs to a venue this system does not address — a
//     ".DE" or ".L" listing looked up bare would resolve to a different
//     security, or to nothing;
//   - a bare ticker is a US listing, which prices as-is.
func FromTicker(ticker, exchange string) (symbol, market string, ok bool) {
	symbol = strings.ToUpper(strings.TrimSpace(ticker))
	exchange = strings.ToUpper(strings.TrimSpace(exchange))
	if symbol == "" || exchange == "" {
		return "", "", false
	}

	if suffix, has := ExchangeSuffix(symbol); has {
		market, known := twMarkets[exchange]
		// A ".TW" ticker reported on TPEX (or the reverse) is a contradiction,
		// and believing the exchange over the suffix would file it so that every
		// later fetch asks for a ticker that does not exist.
		if !known || marketSuffix[market] != suffix {
			return "", "", false
		}
		symbol = strings.TrimSuffix(symbol, suffix)
		if symbol == "" {
			return "", "", false
		}
		return symbol, market, true
	}

	if strings.Contains(symbol, ".") {
		return "", "", false
	}
	return symbol, usMarket(exchange), true
}

// searchResponse is the slice of the provider's payload this package reads.
type searchResponse struct {
	Quotes []struct {
		Symbol    string `json:"symbol"`
		Exchange  string `json:"exchange"`
		QuoteType string `json:"quoteType"`
		ShortName string `json:"shortname"`
		LongName  string `json:"longname"`
	} `json:"quotes"`
}

// Search looks up instruments by symbol or name.
//
// Results the system cannot model — foreign exchanges, indices, futures — are
// dropped rather than returned as unusable rows, so everything a caller sees is
// something it can actually add. Note the provider matches on symbols and
// English names only; a query in Chinese returns nothing.
func (c *Client) Search(ctx context.Context, query string) ([]SearchResult, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}

	endpoint := fmt.Sprintf("%s/v1/finance/search?q=%s&quotesCount=20&newsCount=0",
		c.baseURL, url.QueryEscape(query))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	// The endpoint returns 403 to clients without a browser-like User-Agent.
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; StockBook/1.0)")

	res, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("quote provider returned %s", res.Status)
	}
	var body searchResponse
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("decoding search response: %w", err)
	}

	results := make([]SearchResult, 0, len(body.Quotes))
	for _, q := range body.Quotes {
		// The search endpoint always says what a result is, so an unknown kind
		// here is a kind we cannot hold rather than missing information.
		if !holdableTypes[strings.ToUpper(q.QuoteType)] {
			continue
		}
		symbol, market, ok := FromTicker(q.Symbol, q.Exchange)
		if !ok {
			continue
		}
		name := q.LongName
		if name == "" {
			name = q.ShortName
		}
		results = append(results, SearchResult{
			Symbol:   symbol,
			Name:     strings.TrimSpace(name),
			Market:   market,
			Currency: models.DefaultCurrencyForMarket(market),
			Ticker:   strings.ToUpper(q.Symbol),
		})
	}
	return results, nil
}
