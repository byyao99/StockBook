package handlers

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"stockbook/internal/db"
	"stockbook/internal/middleware"
	"stockbook/internal/models"
	"stockbook/internal/news"
	"stockbook/internal/quotes"
)

// NewsCollector is the slice of the news aggregator this handler needs.
// Keeping it an interface lets the tests drive the whole endpoint without a
// network call, exactly as QuoteProvider does for market data.
type NewsCollector interface {
	Collect(ctx context.Context, held []models.Instrument, since time.Time) news.Report
}

// ResearchHandler serves what the companies in a book are doing, as opposed to
// what the book itself has done. It is the only handler here whose content is
// relayed rather than derived from the ledger.
type ResearchHandler struct {
	db     *db.DB
	quotes QuoteProvider
	news   NewsCollector
}

// NewResearchHandler creates a ResearchHandler. Either provider may be nil, in
// which case a sync reports that half as unconfigured rather than failing: the
// stored feed and stored figures are still readable without them.
func NewResearchHandler(s *db.DB, q QuoteProvider, n NewsCollector) *ResearchHandler {
	return &ResearchHandler{db: s, quotes: q, news: n}
}

const (
	// researchTimeout bounds a whole sync. A first run walks several pages of a
	// news firehose and asks for years of reported figures per holding.
	researchTimeout = 5 * time.Minute

	// newsFreshness and fundamentalsFreshness are how long each answer is
	// treated as current. They differ by three orders of magnitude because the
	// things behind them do: a headline is stale within the hour, while a
	// quarterly result is the same number for three months. One button drives
	// both, and these are what stop it costing two full runs on every press.
	newsFreshness         = 30 * time.Minute
	fundamentalsFreshness = 24 * time.Hour

	// fundamentalsYears is how far back reported periods are fetched. Six years
	// is twenty-four quarters — enough to see through a cycle — and the whole
	// answer is a few dozen numbers, so there is nothing to gain by fetching it
	// incrementally the way price history is. A restatement revising an old
	// period is picked up for free as a result.
	fundamentalsYears = 6
)

// periodTypes maps the cadence a caller asks for to the provider's own stamp,
// which is what the rows are keyed by.
var periodTypes = map[string]string{
	"quarterly": models.PeriodQuarter,
	"annual":    models.PeriodYear,
}

// News handles GET /api/v1/research/news: the caller's own merged feed, newest
// first.
//
// The feed is assembled from articles that are themselves shared — a headline
// about 2330 is objective market commentary, not something a reader owns — but
// which of them appears here is decided entirely by what the caller holds, and
// that scoping is in the query rather than applied afterwards. An admin sees no
// more of it than anybody else.
func (h *ResearchHandler) News(c *gin.Context) {
	opts := parseListOptions(c)
	filter := db.NewsFilter{InstrumentID: c.Query("instrument_id")}

	items, total, err := h.db.ListHeldNews(callerID(c), filter, opts)
	if err != nil {
		respondDBError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": items, "pagination": paginationMeta(opts, total)})
}

// Fundamentals handles GET /api/v1/research/fundamentals?instrument_id=&period=.
//
// Reported figures are master data on the same footing as a price, so this is
// not scoped to the caller's holdings: any authenticated user may read them for
// any instrument, exactly as they may read the instrument itself. It is a
// summary rather than a list, so it carries no pagination block.
func (h *ResearchHandler) Fundamentals(c *gin.Context) {
	instrumentID := c.Query("instrument_id")
	if instrumentID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "instrument_id is required"})
		return
	}
	period := c.DefaultQuery("period", "quarterly")
	periodType, ok := periodTypes[period]
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "period must be quarterly or annual"})
		return
	}

	periods, err := h.db.FinancialReport(instrumentID, periodType)
	if err != nil {
		respondDBError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": periods})
}

// newsSyncReport is what one collection run did.
//
// Collected counts the articles the providers returned that concern a holding,
// not the rows written: re-reading a feed that overlaps what is stored is the
// normal case, and the write ignores what it already has. Fresh counts holdings
// whose news was checked recently enough to leave alone, which on a second press
// is all of them.
type newsSyncReport struct {
	Collected int                 `json:"collected"`
	Fresh     int                 `json:"fresh"`
	Sources   []news.SourceResult `json:"sources"`
	Error     string              `json:"error,omitempty"`
}

// factSyncResult is one instrument's outcome in a fundamentals sync, shaped like
// the quote refresh's per-instrument result and for the same reason: a partial
// failure has to name the symbol it happened to.
type factSyncResult struct {
	InstrumentID string          `json:"instrument_id"`
	Symbol       string          `json:"symbol"`
	Ticker       string          `json:"ticker,omitempty"`
	Status       string          `json:"status"` // synced | skipped | failed
	Currency     models.Currency `json:"currency,omitempty"`
	Added        int             `json:"added"`
	Figures      int64           `json:"figures"`
	Error        string          `json:"error,omitempty"`
}

// factsSyncReport is what one fundamentals run did.
type factsSyncReport struct {
	Synced  int              `json:"synced"`
	Failed  int              `json:"failed"`
	Fresh   int              `json:"fresh"`
	Results []factSyncResult `json:"results"`
}

// Sync handles POST /api/v1/research/sync: fetch headlines and reported figures
// for the caller's holdings.
//
// One endpoint drives both because the page has one button, and the two jobs
// have wildly different natural cadences — which is handled by their own
// freshness windows rather than by asking the reader to press twice and know
// which press does what.
//
// It is open to any authenticated user on the same terms as the quote refresh,
// and for the same reasons: what it fetches is public, a run costs the providers
// little because a repeat press is skipped as fresh, and the rate limit on the
// route is what protects the outbound dependency. A partial failure is not an
// error for the run — one source being down must not cost the reader the other's
// headlines.
//
// A run is proportional to the caller's own open holdings rather than to the
// master data. Fetching news for companies nobody owns would reach the providers
// on their behalf for nothing, and closed holdings answer nothing a reader can
// act on.
func (h *ResearchHandler) Sync(c *gin.Context) {
	held, err := h.db.HeldInstruments(callerID(c))
	if err != nil {
		respondDBError(c, err)
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), researchTimeout)
	defer cancel()

	headlines := h.syncNews(ctx, held)
	figures := h.syncFundamentals(ctx, held)

	slog.Info("research synced",
		slog.String(middleware.RequestIDKey, middleware.RequestIDFromContext(c)),
		slog.String("actor_id", callerID(c)),
		slog.Int("holdings", len(held)),
		slog.Int("articles", headlines.Collected),
		slog.Int("instruments_synced", figures.Synced),
		slog.Int("instruments_failed", figures.Failed),
	)

	c.JSON(http.StatusOK, gin.H{"data": gin.H{
		"holdings":     len(held),
		"news":         headlines,
		"fundamentals": figures,
	}})
}

// syncNews collects headlines for the holdings whose news is due a look.
func (h *ResearchHandler) syncNews(ctx context.Context, held []models.Instrument) newsSyncReport {
	report := newsSyncReport{Sources: []news.SourceResult{}}
	if h.news == nil {
		report.Error = "news collection is not configured"
		return report
	}

	due := make([]models.Instrument, 0, len(held))
	for _, item := range held {
		if isFresh(item.NewsCheckedAt, newsFreshness) {
			report.Fresh++
			continue
		}
		due = append(due, item)
	}
	if len(due) == 0 {
		return report
	}

	// The watermark the Taiwanese firehose walks down to. It is derived from
	// what is stored rather than from a bookkeeping row, so it cannot drift out
	// of step with the data it describes.
	since, err := h.db.LatestNewsAt(news.SourceCnyes)
	if err != nil {
		report.Error = err.Error()
		return report
	}

	collected := h.news.Collect(ctx, due, since)
	report.Sources = collected.Results

	items, mentions := newsRows(collected.Articles)
	if err := h.db.SaveNews(items, mentions); err != nil {
		report.Error = err.Error()
		return report
	}
	report.Collected = len(items)

	// Only holdings a successful source spoke for are stamped. A source that
	// failed leaves its holdings unstamped so the next press retries them
	// immediately, rather than suppressing them for the freshness window — the
	// same rule that leaves a failed quote fetch's timestamps alone.
	checked := []string{}
	for _, result := range collected.Results {
		if result.Status == "synced" {
			checked = append(checked, result.Covered...)
		}
	}
	if err := h.db.MarkNewsChecked(checked, time.Now().UTC()); err != nil {
		report.Error = err.Error()
	}
	return report
}

// newsRows flattens collected articles into the rows to store, folding
// duplicates.
//
// The same article genuinely arrives twice: a piece filed under both AAPL and
// MSFT appears in each ticker's feed with one identifier, and both copies name a
// holding. Writing it twice would be refused by the key; writing only the first
// would drop the second holding's attribution. Folding here keeps one article
// row and one mention per holding it names.
func newsRows(articles []news.Article) ([]models.NewsItem, []models.NewsMention) {
	items := []models.NewsItem{}
	mentions := []models.NewsMention{}
	seenItem := map[string]bool{}
	seenMention := map[string]bool{}

	for _, article := range articles {
		if !seenItem[article.ID] {
			seenItem[article.ID] = true
			items = append(items, models.NewsItem{
				ID:          article.ID,
				Source:      article.Source,
				Title:       article.Title,
				Summary:     article.Summary,
				URL:         article.URL,
				Publisher:   article.Publisher,
				Language:    article.Language,
				PublishedAt: article.PublishedAt,
			})
		}
		for _, instrumentID := range article.InstrumentIDs {
			key := article.ID + "\x00" + instrumentID
			if seenMention[key] {
				continue
			}
			seenMention[key] = true
			mentions = append(mentions, models.NewsMention{
				NewsID:       article.ID,
				InstrumentID: instrumentID,
			})
		}
	}
	return items, mentions
}

// syncFundamentals refreshes reported figures for the holdings due a look.
func (h *ResearchHandler) syncFundamentals(ctx context.Context, held []models.Instrument) factsSyncReport {
	report := factsSyncReport{Results: []factSyncResult{}}
	if h.quotes == nil {
		return report
	}

	for _, item := range held {
		if isFresh(item.FundamentalsCheckedAt, fundamentalsFreshness) {
			report.Fresh++
			continue
		}
		result := h.syncFundamentalsOne(ctx, item)
		switch result.Status {
		case "synced":
			report.Synced++
		case "failed":
			report.Failed++
		}
		report.Results = append(report.Results, result)
	}
	return report
}

// syncFundamentalsOne brings one instrument's reported figures up to date,
// turning every failure into a reportable result rather than aborting the run.
func (h *ResearchHandler) syncFundamentalsOne(ctx context.Context, item models.Instrument) factSyncResult {
	result := factSyncResult{InstrumentID: item.ID, Symbol: item.Symbol}

	ticker, ok := quotes.Ticker(item.Symbol, item.Market)
	if !ok {
		result.Status = "skipped"
		result.Error = "no data source for market " + item.Market
		return result
	}
	result.Ticker = ticker

	to := time.Now().UTC()
	from := to.AddDate(-fundamentalsYears, 0, 0)
	fundamentals, err := h.quotes.Fundamentals(ctx, ticker,
		[]quotes.Period{quotes.Quarterly, quotes.Annual}, from, to)
	if err != nil {
		result.Status = "failed"
		result.Error = err.Error()
		return result
	}
	if len(fundamentals.Figures) == 0 {
		// The provider answered and had nothing. Stamping it as checked is
		// right: asking again in a minute would get the same silence, and a
		// company that files no figures the provider carries is a standing
		// condition rather than a transient failure.
		if err := h.db.MarkFundamentalsChecked(item.ID, time.Now().UTC()); err != nil {
			result.Status = "failed"
			result.Error = err.Error()
			return result
		}
		result.Status = "skipped"
		result.Error = "provider reports no financials for " + ticker
		return result
	}

	// Note what is deliberately *not* here: a check that the reporting currency
	// matches the instrument's. A quote or a price series in the wrong currency
	// is refused, because adopting it would reinterpret every cost basis behind
	// it. These figures are never added to a cost basis or a market value, so a
	// company reporting in a currency its shares do not trade in — an ADR, the
	// ordinary case — is a thing to label, not to reject. Storing what the
	// provider said is what lets the label be true.
	result.Currency = fundamentals.Currency

	facts := make([]models.FinancialFact, 0, len(fundamentals.Figures))
	for _, figure := range fundamentals.Figures {
		facts = append(facts, models.FinancialFact{
			Metric:     figure.Metric,
			PeriodType: figure.PeriodType,
			AsOfDate:   figure.AsOfDate,
			Value:      figure.Value,
			Currency:   fundamentals.Currency,
		})
	}
	if err := h.db.SaveFinancialFacts(item.ID, facts); err != nil {
		result.Status = "failed"
		result.Error = err.Error()
		return result
	}

	total, err := h.db.CountFinancialFacts(item.ID)
	if err != nil {
		result.Status = "failed"
		result.Error = err.Error()
		return result
	}
	if err := h.db.MarkFundamentalsChecked(item.ID, time.Now().UTC()); err != nil {
		result.Status = "failed"
		result.Error = err.Error()
		return result
	}

	result.Status = "synced"
	result.Added = len(facts)
	result.Figures = total
	return result
}

// isFresh reports whether a source was consulted recently enough to leave alone.
// A nil stamp is never fresh: it means the question has not been asked yet.
func isFresh(checkedAt *time.Time, window time.Duration) bool {
	return checkedAt != nil && time.Since(*checkedAt) < window
}
