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

// exchangeMarkets maps the provider's exchange codes to this system's markets.
// It is the inverse of Ticker: TAI listings are named "2330.TW", so a TAI result
// becomes symbol 2330 on TWSE. Anything absent is a market this system does not
// model, and is dropped from results rather than guessed at.
var exchangeMarkets = map[string]string{
	"TAI": "TWSE",
	"TWO": "TPEX",
	"NMS": "NASDAQ",
	"NGM": "NASDAQ",
	"NCM": "NASDAQ",
	"NYQ": "NYSE",
}

// searchableTypes are the instrument kinds worth offering. Indices, futures and
// currencies come back from the same endpoint but cannot be held as shares.
var searchableTypes = map[string]bool{
	"EQUITY": true,
	"ETF":    true,
}

// FromTicker translates a provider ticker and exchange code into the bare symbol
// and market this system stores. It reports false for exchanges not modelled
// here.
func FromTicker(ticker, exchange string) (symbol, market string, ok bool) {
	market, ok = exchangeMarkets[strings.ToUpper(exchange)]
	if !ok {
		return "", "", false
	}
	symbol = strings.ToUpper(ticker)
	if suffix, has := ExchangeSuffix(symbol); has {
		symbol = strings.TrimSuffix(symbol, suffix)
	}
	if symbol == "" {
		return "", "", false
	}
	return symbol, market, true
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
		if !searchableTypes[strings.ToUpper(q.QuoteType)] {
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
