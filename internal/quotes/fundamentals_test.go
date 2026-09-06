package quotes

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"stockbook/internal/models"
)

// These tests never touch the network: they serve recorded payloads from a stub,
// using the same helper the quote tests do.

// tsmcFundamentalsBody is a trimmed real response. It carries two series under
// dynamically named keys, a null period, and the currency on every entry.
const tsmcFundamentalsBody = `{
  "timeseries": {
    "result": [
      {
        "meta": {"symbol": ["2330.TW"], "type": ["quarterlyTotalRevenue"]},
        "timestamp": [1751241600, 1759190400, 1767139200],
        "quarterlyTotalRevenue": [
          {"dataId": 20100, "asOfDate": "2025-06-30", "periodType": "3M", "currencyCode": "TWD",
           "reportedValue": {"raw": 933792000000.0, "fmt": "933.79B"}},
          null,
          {"dataId": 20100, "asOfDate": "2025-12-31", "periodType": "3M", "currencyCode": "TWD",
           "reportedValue": {"raw": 1046090449000.0, "fmt": "1.05T"}}
        ]
      },
      {
        "meta": {"symbol": ["2330.TW"], "type": ["quarterlyDilutedEPS"]},
        "timestamp": [1751241600],
        "quarterlyDilutedEPS": [
          {"dataId": 29000, "asOfDate": "2025-06-30", "periodType": "3M", "currencyCode": "TWD",
           "reportedValue": {"raw": 15.36, "fmt": "15.36"}}
        ]
      }
    ],
    "error": null
  }
}`

// noFundamentalsBody is how the provider refuses a symbol it does not know.
const noFundamentalsBody = `{
  "timeseries": {"result": [], "error": {"code": "Not Found", "description": "No fundamentals data found for any of the summaryTypes=quarterlyTotalRevenue"}}
}`

func window() (time.Time, time.Time) {
	return time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
}

func TestFundamentalsReadsSeriesKeyedByTheirOwnName(t *testing.T) {
	c := serve(t, 200, tsmcFundamentalsBody)
	from, to := window()

	got, err := c.Fundamentals(context.Background(), "2330.TW", []Period{Quarterly}, from, to)
	if err != nil {
		t.Fatalf("Fundamentals: %v", err)
	}
	if got.Currency != models.CurrencyTWD {
		t.Errorf("currency = %q, want TWD", got.Currency)
	}

	// The array holding each series is keyed by the series name itself, so
	// reading it at all is the thing under test. Three entries arrived; one was
	// null and must not have become a figure.
	if len(got.Figures) != 3 {
		t.Fatalf("got %d figures, want 3: %+v", len(got.Figures), got.Figures)
	}

	byKey := map[string]int64{}
	for _, f := range got.Figures {
		byKey[f.Metric+"@"+f.AsOfDate] = f.Value
	}
	// 933,792,000,000 TWD is 93,379,200,000,000 hundredths — exact, and three
	// orders of magnitude inside what a float64 carries without loss.
	if v := byKey[models.MetricRevenue+"@2025-06-30"]; v != 93379200000000 {
		t.Errorf("Q2 revenue = %d, want 93379200000000", v)
	}
	// The provider's name for the metric ("quarterlyDilutedEPS") is translated
	// to this system's, and 15.36 becomes 1536 hundredths like every other
	// amount.
	if v := byKey[models.MetricDilutedEPS+"@2025-06-30"]; v != 1536 {
		t.Errorf("Q2 diluted EPS = %d, want 1536", v)
	}
}

// A period the provider reports as null is unknown, not zero. Recording a
// company as having earned nothing that quarter is exactly the kind of thing
// every reader downstream would believe.
func TestFundamentalsSkipsNullPeriodsRatherThanReadingThemAsZero(t *testing.T) {
	c := serve(t, 200, tsmcFundamentalsBody)
	from, to := window()

	got, err := c.Fundamentals(context.Background(), "2330.TW", []Period{Quarterly}, from, to)
	if err != nil {
		t.Fatalf("Fundamentals: %v", err)
	}
	for _, f := range got.Figures {
		if f.Value == 0 {
			t.Errorf("a null period was read as a zero figure: %+v", f)
		}
		if f.AsOfDate == "2025-09-30" {
			t.Errorf("the null period was stored anyway: %+v", f)
		}
	}
}

// Both cadences go out in one request. Asking twice would double the traffic to
// buy nothing, since the response is a few dozen numbers either way.
func TestFundamentalsAsksForEveryCadenceInOneRequest(t *testing.T) {
	var asked string
	c := serveCapturing(t, tsmcFundamentalsBody, &asked)
	from, to := window()

	if _, err := c.Fundamentals(context.Background(), "2330.TW",
		[]Period{Quarterly, Annual}, from, to); err != nil {
		t.Fatalf("Fundamentals: %v", err)
	}
	for _, want := range []string{
		"quarterlyTotalRevenue", "quarterlyNetIncome", "quarterlyDilutedEPS",
		"annualTotalRevenue", "annualNetIncome", "annualDilutedEPS",
	} {
		if !strings.Contains(asked, want) {
			t.Errorf("request did not ask for %s: %s", want, asked)
		}
	}
}

// The payload's own error object is believed over the HTTP status, and the
// provider's wording is passed through — it is the only actionable diagnostic
// for a symbol filed under the wrong market.
func TestFundamentalsPassesThroughTheProvidersOwnWording(t *testing.T) {
	c := serve(t, 404, noFundamentalsBody)
	from, to := window()

	_, err := c.Fundamentals(context.Background(), "2330.TWO", []Period{Quarterly}, from, to)
	if !errors.Is(err, ErrNoQuote) {
		t.Fatalf("error = %v, want it to wrap ErrNoQuote", err)
	}
	if !strings.Contains(err.Error(), "2330.TWO") {
		t.Errorf("error does not name the ticker: %v", err)
	}
	if !strings.Contains(err.Error(), "No fundamentals data found") {
		t.Errorf("error dropped the provider's wording: %v", err)
	}
}

// A company reporting in something this system cannot model is refused rather
// than filed under a currency it does not use.
func TestFundamentalsRefusesAnUnsupportedReportingCurrency(t *testing.T) {
	body := strings.ReplaceAll(tsmcFundamentalsBody, `"TWD"`, `"EUR"`)
	c := serve(t, 200, body)
	from, to := window()

	_, err := c.Fundamentals(context.Background(), "SAP.DE", []Period{Quarterly}, from, to)
	if err == nil || !strings.Contains(err.Error(), "EUR") {
		t.Fatalf("error = %v, want it to name the unsupported currency", err)
	}
}

// A window the provider has no closed periods in comes back empty with no
// error: nothing arrived, but nothing is wrong.
func TestFundamentalsEmptyWindowIsNotAnError(t *testing.T) {
	c := serve(t, 200, `{"timeseries": {"result": [], "error": null}}`)
	from, to := window()

	got, err := c.Fundamentals(context.Background(), "2330.TW", []Period{Quarterly}, from, to)
	if err != nil {
		t.Fatalf("Fundamentals: %v", err)
	}
	if len(got.Figures) != 0 {
		t.Errorf("got %d figures, want none", len(got.Figures))
	}
}

func TestFundamentalsRefusesAnInvertedWindow(t *testing.T) {
	c := serve(t, 200, tsmcFundamentalsBody)
	from, to := window()

	if _, err := c.Fundamentals(context.Background(), "2330.TW", []Period{Quarterly}, to, from); err == nil {
		t.Fatal("an inverted window was accepted")
	}
}
