package quotes

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"stockbook/internal/models"
)

// Period is the reporting cadence to ask for. The provider names its series by
// cadence rather than taking it as a parameter, so this picks the prefix.
type Period string

const (
	// Quarterly series come back stamped "3M".
	Quarterly Period = "quarterly"
	// Annual series come back stamped "12M".
	Annual Period = "annual"
)

// fundamentalMetrics maps this system's metric names to the provider's, which
// differ ("revenue" vs "TotalRevenue") and are prefixed by the cadence. One
// table drives both the request and the decoding of the response, so a metric
// cannot be asked for under one name and read back under another.
var fundamentalMetrics = map[string]string{
	models.MetricRevenue:    "TotalRevenue",
	models.MetricNetIncome:  "NetIncome",
	models.MetricDilutedEPS: "DilutedEPS",
}

// Figure is one reported number for one closed reporting period.
//
// AsOfDate is the period end as YYYY-MM-DD, for the same reasons DailyClose.Date
// is a string: a fiscal period belongs to a calendar, not to an instant, and as
// a string it sorts chronologically and compares exactly.
//
// Value is int64 minor units like every other amount in the system. Revenue is
// large — TSMC's quarter is ~1.27e12 TWD, or 1.27e14 hundredths — but that is
// three orders of magnitude inside the 9.0e15 that a float64 carries exactly, so
// the conversion through the provider's float is lossless at any scale a listed
// company reaches.
type Figure struct {
	Metric     string
	PeriodType string // 3M | 12M, the provider's own stamp
	AsOfDate   string
	Value      int64
}

// Fundamentals is a run of reported figures for one ticker, with the currency
// they were reported in.
//
// Currency is the company's *reporting* currency, which is not necessarily the
// currency its shares trade in — an ADR is the ordinary case. Unlike a quote or
// a price series, a mismatch here is not a corruption to refuse: these numbers
// are never added to a cost basis or a market value, so the only thing a caller
// owes them is a label. Carrying it is what makes that label possible.
type Fundamentals struct {
	Currency models.Currency
	Figures  []Figure
}

// timeseriesResponse is the envelope of the fundamentals endpoint. Each result
// is left raw because the array holding the figures is keyed by the series name
// itself ("quarterlyTotalRevenue"), which no static struct can name in advance.
type timeseriesResponse struct {
	Timeseries struct {
		Result []json.RawMessage `json:"result"`
		Error  *struct {
			Code        string `json:"code"`
			Description string `json:"description"`
		} `json:"error"`
	} `json:"timeseries"`
}

// figureEntry is one period's cell in a series.
//
// ReportedValue.Raw is a *float64 for the same reason a chart's closes are: the
// provider reports a period it has no figure for as a null rather than omitting
// it, and reading that as 0.0 would record a company as having earned nothing.
type figureEntry struct {
	AsOfDate      string `json:"asOfDate"`
	PeriodType    string `json:"periodType"`
	CurrencyCode  string `json:"currencyCode"`
	ReportedValue struct {
		Raw *float64 `json:"raw"`
	} `json:"reportedValue"`
}

// Fundamentals fetches reported revenue, net income and diluted EPS for one
// ticker over [from, to], at every requested cadence.
//
// This is a different endpoint from the chart the prices come from, but the same
// provider and the same ticker vocabulary, which is why it lives here: nothing
// outside this package still needs to know where any of it comes from. It needs
// no cookie or crumb, unlike the quoteSummary endpoint that carries the earnings
// calendar — which is why there is no next-earnings-date anywhere in the system.
//
// A period the provider reports as null is skipped rather than read as zero, and
// a window with no closed periods in it comes back empty with no error: nothing
// arrived, but nothing is wrong.
func (c *Client) Fundamentals(ctx context.Context, ticker string, periods []Period, from, to time.Time) (Fundamentals, error) {
	if to.Before(from) {
		return Fundamentals{}, fmt.Errorf("fundamentals window ends (%s) before it starts (%s)",
			to.Format(time.DateOnly), from.Format(time.DateOnly))
	}
	if len(periods) == 0 {
		return Fundamentals{}, nil
	}

	// The series names the provider will answer with, and the reverse lookup
	// used to read them back. Built together so the two cannot drift.
	//
	// Both cadences go in one request because the endpoint takes a list, and
	// the response is a few dozen numbers either way — asking twice would double
	// the traffic to buy nothing.
	types := make([]string, 0, len(fundamentalMetrics)*len(periods))
	metricOf := make(map[string]string, len(fundamentalMetrics)*len(periods))
	for _, period := range periods {
		for metric, suffix := range fundamentalMetrics {
			name := string(period) + suffix
			types = append(types, name)
			metricOf[name] = metric
		}
	}
	sort.Strings(types) // a stable request URL, which keeps the tests readable

	query := url.Values{}
	query.Set("symbol", ticker)
	query.Set("type", strings.Join(types, ","))
	query.Set("period1", fmt.Sprint(from.Unix()))
	query.Set("period2", fmt.Sprint(to.Unix()))
	query.Set("merge", "false")

	results, err := c.timeseries(ctx, ticker, query.Encode())
	if err != nil {
		return Fundamentals{}, err
	}

	out := Fundamentals{}
	reported := ""
	for _, raw := range results {
		name, entries, err := decodeSeries(raw)
		if err != nil {
			return Fundamentals{}, fmt.Errorf("decoding fundamentals for %s: %w", ticker, err)
		}
		metric, known := metricOf[name]
		if !known {
			// A series we did not ask for. Ignoring it is right: the provider
			// occasionally answers with more than the request named, and none
			// of it is anything this system models.
			continue
		}
		for _, entry := range entries {
			if entry == nil || entry.ReportedValue.Raw == nil || entry.AsOfDate == "" {
				continue
			}
			if code := strings.ToUpper(strings.TrimSpace(entry.CurrencyCode)); code != "" {
				if reported != "" && code != reported {
					return Fundamentals{}, fmt.Errorf(
						"provider reports %s in more than one currency (%s and %s)",
						ticker, reported, code)
				}
				reported = code
			}
			out.Figures = append(out.Figures, Figure{
				Metric:     metric,
				PeriodType: entry.PeriodType,
				AsOfDate:   entry.AsOfDate,
				Value:      toMinorUnits(*entry.ReportedValue.Raw),
			})
		}
	}

	if len(out.Figures) == 0 {
		return Fundamentals{}, nil
	}
	currency, ok := models.CanonicalCurrency(reported)
	if !ok {
		return Fundamentals{}, fmt.Errorf("unsupported reporting currency %q for %s", reported, ticker)
	}
	out.Currency = currency
	return out, nil
}

// timeseries performs one call to the fundamentals endpoint and returns the raw
// series it carries.
//
// The error handling mirrors chart()'s deliberately: decode the body first and
// believe the payload's own error object over the HTTP status, because the
// provider explains a bad symbol in a 404 body, and name the ticker in every
// message since a wrong market is the usual cause of a lookup coming back empty.
func (c *Client) timeseries(ctx context.Context, ticker, query string) ([]json.RawMessage, error) {
	endpoint := fmt.Sprintf("%s/ws/fundamentals-timeseries/v1/finance/timeseries/%s?%s",
		c.baseURL, url.PathEscape(ticker), query)

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

	var body timeseriesResponse
	decodeErr := json.NewDecoder(res.Body).Decode(&body)

	if body.Timeseries.Error != nil {
		desc := strings.TrimSpace(body.Timeseries.Error.Description)
		if desc == "" {
			desc = body.Timeseries.Error.Code
		}
		return nil, fmt.Errorf("%w for %s: %s", ErrNoQuote, ticker, desc)
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fundamentals provider returned %s for %s", res.Status, ticker)
	}
	if decodeErr != nil {
		return nil, fmt.Errorf("decoding fundamentals response: %w", decodeErr)
	}
	return body.Timeseries.Result, nil
}

// decodeSeries reads one result object: its series name, from the meta block,
// and the entries stored under a key of that same name.
func decodeSeries(raw json.RawMessage) (string, []*figureEntry, error) {
	var head struct {
		Meta struct {
			Type []string `json:"type"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return "", nil, err
	}
	if len(head.Meta.Type) == 0 {
		// A result that does not say what it is cannot be attributed to a
		// metric, so there is nothing to read from it.
		return "", nil, nil
	}
	name := head.Meta.Type[0]

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return "", nil, err
	}
	series, ok := fields[name]
	if !ok {
		return name, nil, nil
	}
	var entries []*figureEntry
	if err := json.Unmarshal(series, &entries); err != nil {
		return "", nil, err
	}
	return name, entries, nil
}
