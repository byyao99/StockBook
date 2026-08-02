package quotes

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Timestamps for the 09:00 Taipei open of three consecutive sessions, which is
// how the provider stamps a Taiwan daily bar:
//
//	1751331600 = 2025-07-01T01:00:00Z
//	1751418000 = 2025-07-02T01:00:00Z
//	1751504400 = 2025-07-03T01:00:00Z
const twHistoryBody = `{"chart":{"result":[{
	"meta":{"symbol":"2330.TW","currency":"TWD"},
	"timestamp":[1751331600,1751418000,1751504400],
	"indicators":{"quote":[{"close":[1000.0,1010.5,995.0]}]}
}],"error":null}}`

func day(t *testing.T, s string) time.Time {
	t.Helper()
	d, err := time.Parse(time.DateOnly, s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return d
}

func TestHistoryParsesASeries(t *testing.T) {
	c := serve(t, http.StatusOK, twHistoryBody)

	got, err := c.History(context.Background(), "2330.TW",
		day(t, "2025-07-01"), day(t, "2025-07-03"))
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if got.Currency != "TWD" {
		t.Errorf("currency %q, want TWD", got.Currency)
	}

	want := []DailyClose{
		{Date: "2025-07-01", Close: 100000},
		{Date: "2025-07-02", Close: 101050},
		{Date: "2025-07-03", Close: 99500},
	}
	if len(got.Closes) != len(want) {
		t.Fatalf("got %d closes, want %d: %+v", len(got.Closes), len(want), got.Closes)
	}
	for i, w := range want {
		if got.Closes[i] != w {
			t.Errorf("close %d = %+v, want %+v", i, got.Closes[i], w)
		}
	}
}

// A missing bar comes back as a null rather than being omitted. Reading it as
// 0.0 would record the stock as worthless for a day, which every consumer of
// this series would then believe.
func TestHistorySkipsNullBars(t *testing.T) {
	const body = `{"chart":{"result":[{
		"meta":{"symbol":"2330.TW","currency":"TWD"},
		"timestamp":[1751331600,1751418000,1751504400],
		"indicators":{"quote":[{"close":[1000.0,null,995.0]}]}
	}],"error":null}}`
	c := serve(t, http.StatusOK, body)

	got, err := c.History(context.Background(), "2330.TW",
		day(t, "2025-07-01"), day(t, "2025-07-03"))
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(got.Closes) != 2 {
		t.Fatalf("got %d closes, want 2 (the null skipped): %+v", len(got.Closes), got.Closes)
	}
	for _, c := range got.Closes {
		if c.Close == 0 {
			t.Errorf("a zero close survived: %+v", c)
		}
		if c.Date == "2025-07-02" {
			t.Errorf("the null bar was recorded: %+v", c)
		}
	}
}

// A window with no sessions in it is not a failure — a top-up spanning only a
// weekend is perfectly ordinary and simply finds nothing.
func TestHistoryAcceptsAnEmptySeries(t *testing.T) {
	const body = `{"chart":{"result":[{
		"meta":{"symbol":"2330.TW","currency":"TWD"},
		"timestamp":[],"indicators":{"quote":[{"close":[]}]}
	}],"error":null}}`
	c := serve(t, http.StatusOK, body)

	got, err := c.History(context.Background(), "2330.TW",
		day(t, "2025-07-05"), day(t, "2025-07-06"))
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(got.Closes) != 0 {
		t.Errorf("got %d closes, want none", len(got.Closes))
	}
}

// The upper bound names a whole day. Sending it as a bare instant would ask for
// midnight and drop the session it belongs to — the same trap a bare `to` date
// springs on the reports.
func TestHistoryAsksForTheWholeOfTheClosingDay(t *testing.T) {
	var query url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(twHistoryBody))
	}))
	t.Cleanup(srv.Close)
	c := newTestClient(srv.URL)

	from, to := day(t, "2025-07-01"), day(t, "2025-07-03")
	if _, err := c.History(context.Background(), "2330.TW", from, to); err != nil {
		t.Fatalf("History: %v", err)
	}

	if got, want := query.Get("period1"), strconv.FormatInt(from.Unix(), 10); got != want {
		t.Errorf("period1 = %q, want %q (the start of %s)", got, want, from.Format(time.DateOnly))
	}
	// period2 is exclusive at the provider, so it has to reach past the close of
	// the last day asked for.
	want := strconv.FormatInt(to.Add(24*time.Hour).Unix(), 10)
	if got := query.Get("period2"); got != want {
		t.Errorf("period2 = %q, want %q (the end of %s)", got, want, to.Format(time.DateOnly))
	}
	if got := query.Get("interval"); got != "1d" {
		t.Errorf("interval = %q, want 1d", got)
	}
}

// An inverted window is a caller bug, and the provider answers one with
// something meaningless rather than an error, so it is refused here.
func TestHistoryRefusesAnInvertedWindow(t *testing.T) {
	c := serve(t, http.StatusOK, twHistoryBody)
	_, err := c.History(context.Background(), "2330.TW",
		day(t, "2025-07-03"), day(t, "2025-07-01"))
	if err == nil {
		t.Fatal("expected an error for a window that ends before it starts")
	}
}

// The provider's own wording is the only actionable diagnostic for a bad
// ticker, exactly as on the quote path.
func TestHistorySurfacesTheProvidersOwnMessage(t *testing.T) {
	const body = `{"chart":{"result":null,"error":
		{"code":"Not Found","description":"No data found, symbol may be delisted"}}}`
	c := serve(t, http.StatusNotFound, body)

	_, err := c.History(context.Background(), "NOPE.TW",
		day(t, "2025-07-01"), day(t, "2025-07-03"))
	if !errors.Is(err, ErrNoQuote) {
		t.Fatalf("got %v, want ErrNoQuote", err)
	}
	if !strings.Contains(err.Error(), "NOPE.TW") ||
		!strings.Contains(err.Error(), "may be delisted") {
		t.Errorf("error should name the ticker and quote the provider: %v", err)
	}
}

// A series in a currency this system does not model must not be adopted: every
// cost basis recorded against the instrument is denominated in the one on file.
func TestHistoryRejectsAnUnsupportedCurrency(t *testing.T) {
	const body = `{"chart":{"result":[{
		"meta":{"symbol":"VOW.DE","currency":"EUR"},
		"timestamp":[1751331600],"indicators":{"quote":[{"close":[100.0]}]}
	}],"error":null}}`
	c := serve(t, http.StatusOK, body)

	_, err := c.History(context.Background(), "VOW.DE",
		day(t, "2025-07-01"), day(t, "2025-07-03"))
	if err == nil || !strings.Contains(err.Error(), "EUR") {
		t.Fatalf("got %v, want an error naming the currency", err)
	}
}

// barDate decides which session a bar belongs to, and it holds only because
// every market this system addresses opens after midnight UTC.
func TestBarDateNamesTheTradingSession(t *testing.T) {
	tests := []struct {
		name string
		unix int64
		want string
	}{
		// 09:00 Taipei = 01:00 UTC the same day.
		{"taiwan open", 1751331600, "2025-07-01"},
		// 09:30 New York in summer = 13:30 UTC the same day.
		{"new york open", 1751376600, "2025-07-01"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := barDate(tc.unix); got != tc.want {
				t.Errorf("barDate(%d) = %q, want %q", tc.unix, got, tc.want)
			}
		})
	}
}
