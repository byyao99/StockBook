package quotes

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"stockbook/internal/models"
)

// These tests never touch the network: they serve recorded payloads from a stub
// so the suite stays deterministic and CI does not depend on Yahoo being up.

func TestTicker(t *testing.T) {
	tests := []struct {
		symbol, market string
		want           string
		ok             bool
	}{
		{"2330", "TWSE", "2330.TW", true},
		{"6488", "TPEX", "6488.TWO", true},
		{"TSLA", "NASDAQ", "TSLA", true},
		{"BRK-B", "NYSE", "BRK-B", true},
		{"ANY", "OTHER", "", false},
		{"ANY", "", "", false},
	}
	for _, tc := range tests {
		got, ok := Ticker(tc.symbol, tc.market)
		if got != tc.want || ok != tc.ok {
			t.Errorf("Ticker(%q, %q) = %q, %v; want %q, %v",
				tc.symbol, tc.market, got, ok, tc.want, tc.ok)
		}
	}
}

// serve returns a client pointed at a stub replying with the given status and body.
func serve(t *testing.T, status int, body string) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return newTestClient(srv.URL)
}

const tslaBody = `{"chart":{"result":[{"meta":{"symbol":"TSLA","currency":"USD",
	"regularMarketPrice":313.03,"regularMarketTime":1784923201,
	"exchangeName":"NMS","instrumentType":"EQUITY"}}],"error":null}}`

const tsmcBody = `{"chart":{"result":[{"meta":{"symbol":"2330.TW","currency":"TWD",
	"regularMarketPrice":2350.0,"regularMarketTime":1784871010}}],"error":null}}`

func TestFetchParsesAQuote(t *testing.T) {
	c := serve(t, http.StatusOK, tslaBody)

	q, err := c.Fetch(context.Background(), "TSLA")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	// The float price must land on exact integer hundredths.
	if q.Price != 31303 {
		t.Errorf("price = %d, want 31303", q.Price)
	}
	if q.Currency != models.CurrencyUSD {
		t.Errorf("currency = %q, want USD", q.Currency)
	}
	// AsOf is the market's timestamp, not the moment of the request, so a stale
	// quote reads as stale rather than as freshly fetched.
	if q.AsOf.Unix() != 1784923201 {
		t.Errorf("as-of = %d, want the reported market time 1784923201", q.AsOf.Unix())
	}
	// The exchange and kind come back with the price so one fetch is enough to
	// decide whether the instrument can be filed at all.
	if q.Exchange != "NMS" || q.Type != "EQUITY" {
		t.Errorf("exchange = %q type = %q, want NMS and EQUITY", q.Exchange, q.Type)
	}
}

func TestIsHoldable(t *testing.T) {
	for _, kind := range []string{"EQUITY", "ETF", "etf", ""} {
		if !IsHoldable(kind) {
			t.Errorf("%q should be holdable", kind)
		}
	}
	// These price perfectly well and still cannot be owned as shares.
	for _, kind := range []string{"INDEX", "FUTURE", "CRYPTOCURRENCY", "MUTUALFUND", "CURRENCY"} {
		if IsHoldable(kind) {
			t.Errorf("%q should not be holdable", kind)
		}
	}
}

func TestFetchHandlesTaiwanQuotes(t *testing.T) {
	c := serve(t, http.StatusOK, tsmcBody)

	q, err := c.Fetch(context.Background(), "2330.TW")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if q.Price != 235000 {
		t.Errorf("price = %d, want 235000", q.Price)
	}
	if q.Currency != models.CurrencyTWD {
		t.Errorf("currency = %q, want TWD", q.Currency)
	}
}

// When the provider explains a failure, that explanation must reach the caller —
// it is the only useful diagnostic for a bad symbol.
func TestFetchSurfacesTheProvidersOwnMessage(t *testing.T) {
	const body = `{"chart":{"result":null,"error":{"code":"Not Found",
		"description":"No data found, symbol may be delisted"}}}`
	c := serve(t, http.StatusNotFound, body)

	_, err := c.Fetch(context.Background(), "BOGUS")
	if !errors.Is(err, ErrNoQuote) {
		t.Fatalf("got %v, want ErrNoQuote", err)
	}
	if !strings.Contains(err.Error(), "symbol may be delisted") {
		t.Errorf("error %q should carry the provider's description", err)
	}
	// The ticker must be named too: a wrong market is the usual cause, and
	// seeing "2330.TWO" is what reveals it.
	if !strings.Contains(err.Error(), "BOGUS") {
		t.Errorf("error %q should name the ticker that was looked up", err)
	}
}

// The ticker in the message is what makes a mis-filed market diagnosable: a
// TWSE-listed symbol recorded as TPEX is looked up with the wrong suffix, and
// the provider can only report that as a missing symbol.
func TestFetchNamesTheTickerItLookedUp(t *testing.T) {
	const body = `{"chart":{"result":null,"error":{"code":"Not Found",
		"description":"No data found, symbol may be delisted"}}}`
	c := serve(t, http.StatusNotFound, body)

	_, err := c.Fetch(context.Background(), "2330.TWO")
	if err == nil || !strings.Contains(err.Error(), "2330.TWO") {
		t.Errorf("error %v should name 2330.TWO so the wrong market is obvious", err)
	}
}

// An error object with no description still has to say something.
func TestFetchFallsBackToTheErrorCode(t *testing.T) {
	const body = `{"chart":{"result":null,"error":{"code":"Unauthorized","description":""}}}`
	c := serve(t, http.StatusUnauthorized, body)

	_, err := c.Fetch(context.Background(), "TSLA")
	if err == nil || !strings.Contains(err.Error(), "Unauthorized") {
		t.Errorf("error %v should carry the provider's code", err)
	}
}

func TestFetchRejectsAnEmptyResult(t *testing.T) {
	c := serve(t, http.StatusOK, `{"chart":{"result":[],"error":null}}`)

	if _, err := c.Fetch(context.Background(), "TSLA"); !errors.Is(err, ErrNoQuote) {
		t.Errorf("got %v, want ErrNoQuote", err)
	}
}

// A currency we cannot represent must fail loudly rather than being stored under
// the wrong one, which would silently corrupt a per-currency total.
func TestFetchRejectsAnUnsupportedCurrency(t *testing.T) {
	const body = `{"chart":{"result":[{"meta":{"symbol":"VOD.L","currency":"GBp",
		"regularMarketPrice":70.5,"regularMarketTime":1784923201}}],"error":null}}`
	c := serve(t, http.StatusOK, body)

	_, err := c.Fetch(context.Background(), "VOD.L")
	if err == nil || !strings.Contains(err.Error(), "GBp") {
		t.Errorf("error %v should name the unsupported currency", err)
	}
}

func TestFetchReportsATransportFailure(t *testing.T) {
	// A server that is not listening: the transport error must propagate.
	c := newTestClient("http://127.0.0.1:1")

	if _, err := c.Fetch(context.Background(), "TSLA"); err == nil {
		t.Error("expected a transport error")
	}
}

func TestToMinorUnitsRoundsRatherThanTruncates(t *testing.T) {
	tests := []struct {
		price float64
		want  int64
	}{
		{313.03, 31303},
		{2350.0, 235000},
		{0.005, 1},    // rounds away from zero
		{19.99, 1999}, // 1998.9999999999998 in IEEE-754
		{1234.565, 123457},
	}
	for _, tc := range tests {
		if got := toMinorUnits(tc.price); got != tc.want {
			t.Errorf("toMinorUnits(%v) = %d, want %d", tc.price, got, tc.want)
		}
	}
}

// A pasted provider ticker must be recognizable at entry time, before it turns
// into a double-suffixed lookup that only fails much later.
func TestExchangeSuffix(t *testing.T) {
	tests := []struct {
		symbol string
		want   string
		ok     bool
	}{
		{"2330.TW", ".TW", true},
		{"6488.TWO", ".TWO", true},
		{"2330.tw", ".TW", true}, // case-insensitive, since symbols arrive unnormalized
		{"2330", "", false},
		{"TSLA", "", false},
		{"BRK-B", "", false},
	}
	for _, tc := range tests {
		got, ok := ExchangeSuffix(tc.symbol)
		if got != tc.want || ok != tc.ok {
			t.Errorf("ExchangeSuffix(%q) = %q, %v; want %q, %v", tc.symbol, got, ok, tc.want, tc.ok)
		}
	}
}

// The longer suffix has to win, or ".TWO" would be reported as ".TW" and the
// suggested symbol would keep a stray "O".
func TestExchangeSuffixPrefersTheLongerMatch(t *testing.T) {
	if got, _ := ExchangeSuffix("6488.TWO"); got != ".TWO" {
		t.Errorf("got %q, want .TWO", got)
	}
}

func TestFromTicker(t *testing.T) {
	tests := []struct {
		ticker, exchange string
		wantSymbol       string
		wantMarket       string
		ok               bool
	}{
		// The provider names Taiwan listings with a suffix; the suffix belongs to
		// the market here, so it is stripped back off.
		{"2330.TW", "TAI", "2330", "TWSE", true},
		{"6488.TWO", "TWO", "6488", "TPEX", true},
		{"TSLA", "NMS", "TSLA", "NASDAQ", true},
		{"TSM", "NYQ", "TSM", "NYSE", true},
		{"tsla", "nms", "TSLA", "NASDAQ", true},
		// A bare ticker is a US listing whichever venue carries it, so the
		// venues that are not enumerated anywhere still resolve. These four are
		// the ones that used to vanish: NYSE Arca, Cboe and NYSE American carry
		// most US ETFs between them.
		{"VOO", "PCX", "VOO", "NYSE", true},   // NYSE Arca
		{"ARKK", "BTS", "ARKK", "NYSE", true}, // Cboe US
		{"IMO", "ASE", "IMO", "NYSE", true},   // NYSE American
		{"WHATEVER", "XYZ", "WHATEVER", "NYSE", true},
		// A ticker whose suffix is not one this package appends belongs to a
		// venue that cannot be addressed: looked up bare it would resolve to a
		// different security, or to nothing at all.
		{"TL0.F", "FRA", "", "", false},
		{"2330.HK", "HKG", "", "", false},
		{"VTI.MX", "MEX", "", "", false},
		// A suffix that contradicts the exchange reported with it. Believing the
		// exchange would file this so that every later fetch asks for 2330.TWO,
		// which does not exist.
		{"2330.TW", "TWO", "", "", false},
		{"6488.TWO", "TAI", "", "", false},
		// Nothing to go on.
		{"ANY", "", "", "", false},
		{"", "NMS", "", "", false},
		{".TW", "TAI", "", "", false},
	}
	for _, tc := range tests {
		symbol, market, ok := FromTicker(tc.ticker, tc.exchange)
		if symbol != tc.wantSymbol || market != tc.wantMarket || ok != tc.ok {
			t.Errorf("FromTicker(%q, %q) = %q, %q, %v; want %q, %q, %v",
				tc.ticker, tc.exchange, symbol, market, ok, tc.wantSymbol, tc.wantMarket, tc.ok)
		}
	}
}

// FromTicker must be the exact inverse of Ticker, or a symbol added from a
// search result would be looked up under a name the provider does not use.
func TestFromTickerRoundTripsWithTicker(t *testing.T) {
	cases := []struct{ ticker, exchange string }{
		{"2330.TW", "TAI"}, {"6488.TWO", "TWO"}, {"TSLA", "NMS"}, {"TSM", "NYQ"},
		{"VOO", "PCX"}, {"ARKK", "BTS"},
	}
	for _, tc := range cases {
		symbol, market, ok := FromTicker(tc.ticker, tc.exchange)
		if !ok {
			t.Fatalf("FromTicker(%q, %q) failed", tc.ticker, tc.exchange)
		}
		back, ok := Ticker(symbol, market)
		if !ok || back != tc.ticker {
			t.Errorf("round trip of %q gave %q, want the original", tc.ticker, back)
		}
	}
}

const searchBody = `{"quotes":[
	{"symbol":"TSLA","exchange":"NMS","quoteType":"EQUITY","shortname":"Tesla, Inc.","longname":"Tesla, Inc."},
	{"symbol":"TL0.F","exchange":"FRA","quoteType":"EQUITY","shortname":"Tesla Inc."},
	{"symbol":"TSLT","exchange":"BTS","quoteType":"ETF","shortname":"T-REX 2X Long Tesla"},
	{"symbol":"2330.TW","exchange":"TAI","quoteType":"EQUITY","shortname":"TSMC","longname":"Taiwan Semiconductor Manufacturing"},
	{"symbol":"^SOX","exchange":"SNP","quoteType":"INDEX","shortname":"PHLX Semiconductor"}
]}`

func TestSearchKeepsOnlyWhatCanBeModelled(t *testing.T) {
	c := serve(t, http.StatusOK, searchBody)

	results, err := c.Search(context.Background(), "tesla")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	// The foreign listing (FRA) and the index are dropped: offering a row that
	// cannot be added would be worse than not offering it. The Cboe-listed ETF
	// is kept — a US listing is addressable whichever venue carries it, and
	// dropping those is what hid every NYSE Arca ETF from search.
	if len(results) != 3 {
		t.Fatalf("got %d results, want 3: %+v", len(results), results)
	}

	tsla := results[0]
	if tsla.Symbol != "TSLA" || tsla.Market != "NASDAQ" || tsla.Currency != models.CurrencyUSD {
		t.Errorf("TSLA = %+v", tsla)
	}
	if tsla.Name != "Tesla, Inc." {
		t.Errorf("name = %q, want the long name", tsla.Name)
	}

	if tslt := results[1]; tslt.Symbol != "TSLT" || tslt.Market != "NYSE" {
		t.Errorf("Cboe-listed ETF = %+v, want TSLT on NYSE", tslt)
	}

	tsmc := results[2]
	// The stored symbol is bare; the provider's ticker is kept only for display.
	if tsmc.Symbol != "2330" || tsmc.Ticker != "2330.TW" {
		t.Errorf("2330 symbol = %q ticker = %q", tsmc.Symbol, tsmc.Ticker)
	}
	if tsmc.Market != "TWSE" || tsmc.Currency != models.CurrencyTWD {
		t.Errorf("2330 market = %q currency = %q", tsmc.Market, tsmc.Currency)
	}
	if tsmc.Name != "Taiwan Semiconductor Manufacturing" {
		t.Errorf("name = %q, want the long name", tsmc.Name)
	}
}

func TestSearchFallsBackToTheShortName(t *testing.T) {
	const body = `{"quotes":[{"symbol":"TSLT","exchange":"NMS","quoteType":"ETF","shortname":"T-REX 2X Long Tesla"}]}`
	c := serve(t, http.StatusOK, body)

	results, err := c.Search(context.Background(), "tesla")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 || results[0].Name != "T-REX 2X Long Tesla" {
		t.Errorf("got %+v, want the short name used", results)
	}
}

func TestSearchIgnoresABlankQuery(t *testing.T) {
	// No stub is needed: a blank query must not reach the provider at all.
	c := newTestClient("http://127.0.0.1:1")

	results, err := c.Search(context.Background(), "   ")
	if err != nil || results != nil {
		t.Errorf("got %v, %v; want no results and no error", results, err)
	}
}

func TestSearchReportsAProviderFailure(t *testing.T) {
	c := serve(t, http.StatusTooManyRequests, `{}`)

	_, err := c.Search(context.Background(), "tesla")
	if err == nil || !strings.Contains(err.Error(), "429") {
		t.Errorf("error %v should carry the provider's status", err)
	}
}
