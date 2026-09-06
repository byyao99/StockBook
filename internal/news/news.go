// Package news collects headlines about the companies a book holds.
//
// It is the first thing in this system that is relayed rather than derived. A
// price can be checked before it is stored — against the currency on file,
// against the market the instrument trades on — and a position can be replayed
// from the ledger that produced it. A headline can be checked against nothing.
//
// What replaces that check is a single rule: **an article is attached to a
// holding only when the provider itself tagged it with that holding's code.**
// Never by finding the company's name in the title, and never by a keyword
// search — both were tried against the live sources and both returned confident
// nonsense (a query for 2330 came back with Ethereum analysis; Yahoo's own
// per-symbol news array for 2330.TW came back with Deutsche Telekom). Tagging is
// imperfect in the other direction: the sources leave plenty of articles
// untagged, so a holding shows fewer headlines than exist. That is the trade
// this package chooses. A missing headline costs the reader nothing; one
// attributed to the wrong company is the same class of harm as a fee silently
// read as zero.
//
// Two providers, split by market, because no single one covers both well:
// Taiwanese listings are served in Chinese by cnyes, US listings in English by
// Yahoo. Both are undocumented and unversioned. As in the quotes package,
// nothing outside this package knows where any of it comes from.
package news

import (
	"context"
	"net/http"
	"time"

	"stockbook/internal/models"
)

// Source names, also stored on each row so a feed can say where an item came
// from and so an incremental fetch can find its own watermark.
const (
	SourceCnyes = "cnyes"
	SourceYahoo = "yahoo"
)

// Article is one headline, already attributed to the holdings it concerns.
//
// ID is prefixed with the source ("cnyes:6591330") rather than being the
// provider's bare identifier: the two providers number their articles
// independently, and nothing guarantees they will not collide.
//
// Language is carried because the feed mixes them. A reader deciding whether to
// open a link wants to know which one they are about to get, and it cannot be
// inferred from the holding — a Taiwanese company's news arrives in both.
//
// InstrumentIDs is the attribution, resolved here rather than by the caller so
// that the rule in the package comment has exactly one implementation.
type Article struct {
	ID            string
	Source        string
	Title         string
	Summary       string
	URL           string
	Publisher     string
	Language      string
	PublishedAt   time.Time
	InstrumentIDs []string
}

// SourceResult is one provider's outcome in a collection run, shaped like the
// quote refresh's per-instrument result and for the same reason: a run that
// half worked has to say which half, or the counts are not actionable.
//
// Scope says what was asked for — a ticker, or the set of Taiwanese holdings the
// firehose was scanned for — because the two providers are queried on entirely
// different terms and a bare source name would not distinguish them.
type SourceResult struct {
	Source  string `json:"source"`
	Scope   string `json:"scope"`
	Status  string `json:"status"` // synced | skipped | failed
	Fetched int    `json:"fetched"`
	Error   string `json:"error,omitempty"`
	// Covered names the holdings this result speaks for, so the caller can
	// stamp exactly those as checked. It is deliberately not serialized: it
	// answers a bookkeeping question, and the wire already says which holdings
	// were involved in the terms a reader cares about. The split between the two
	// providers lives in this package, so recomputing it outside would mean
	// exporting the routing table for no other reason.
	Covered []string `json:"-"`
}

// Report is everything one collection run gathered, plus what happened.
type Report struct {
	Articles []Article
	Results  []SourceResult
}

// Aggregator fetches from both providers and owns the routing between them.
type Aggregator struct {
	http     *http.Client
	cnyesURL string
	yahooURL string
}

const (
	defaultCnyesURL = "https://api.cnyes.com"
	defaultYahooURL = "https://feeds.finance.yahoo.com"

	// userAgent is browser-like for the same reason the quotes package's is:
	// both providers answer 403 to clients without one.
	userAgent = "Mozilla/5.0 (compatible; StockBook/1.0)"
)

// NewAggregator returns an Aggregator with a sensible per-request timeout.
func NewAggregator() *Aggregator {
	return &Aggregator{
		http:     &http.Client{Timeout: 15 * time.Second},
		cnyesURL: defaultCnyesURL,
		yahooURL: defaultYahooURL,
	}
}

// newTestAggregator points an Aggregator at stub servers; used by the tests so
// no network call is ever made.
func newTestAggregator(cnyesURL, yahooURL string) *Aggregator {
	return &Aggregator{
		http:     &http.Client{Timeout: 5 * time.Second},
		cnyesURL: cnyesURL,
		yahooURL: yahooURL,
	}
}

// Collect gathers headlines for the given holdings published after since.
//
// since is the watermark, not a filter on what is interesting: the Taiwanese
// firehose is walked newest-first and stops once it reaches material already
// stored, so a routine run costs one page rather than the whole archive.
//
// A provider failing is never an error for the run. One source being down must
// not cost the reader the other one's headlines, so every failure becomes a
// reportable result and collection continues — the contract the quote refresh
// already established.
func (a *Aggregator) Collect(ctx context.Context, held []models.Instrument, since time.Time) Report {
	report := Report{}

	// Taiwanese listings first: one firehose request covers all of them, so it
	// is both the cheaper call and the one most likely to be worth making.
	taiwanese := make([]models.Instrument, 0, len(held))
	foreign := make([]models.Instrument, 0, len(held))
	for _, item := range held {
		if cnyesMarkets[item.Market] {
			taiwanese = append(taiwanese, item)
			continue
		}
		foreign = append(foreign, item)
	}

	if len(taiwanese) > 0 {
		articles, result := a.collectCnyes(ctx, taiwanese, since)
		report.Articles = append(report.Articles, articles...)
		report.Results = append(report.Results, result)
	}
	for _, item := range foreign {
		articles, result := a.collectYahoo(ctx, item)
		report.Articles = append(report.Articles, articles...)
		report.Results = append(report.Results, result)
	}
	return report
}
