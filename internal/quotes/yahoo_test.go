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
	"regularMarketPrice":313.03,"regularMarketTime":1784923201}}],"error":null}}`

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
