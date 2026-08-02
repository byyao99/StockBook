package handlers

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"stockbook/internal/db"
	"stockbook/internal/middleware"
	"stockbook/internal/models"
	"stockbook/internal/quotes"
)

// syncResult is one instrument's outcome in a history sync, shaped like
// refreshResult for the same reason: a partial failure has to name the symbol it
// happened to, or the count is not actionable.
type syncResult struct {
	InstrumentID string `json:"instrument_id"`
	Symbol       string `json:"symbol"`
	Ticker       string `json:"ticker,omitempty"`
	Status       string `json:"status"` // synced | skipped | failed
	// From and To are the window actually requested, which is normally much
	// narrower than the instrument's whole history — seeing it is how a caller
	// tells an incremental top-up from a first full download.
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`
	// Added counts the sessions this call wrote; Sessions is how many the
	// instrument holds afterwards. A run that adds nothing but already holds
	// years is healthy, and only the second number says so.
	Added    int    `json:"added"`
	Sessions int64  `json:"sessions"`
	Error    string `json:"error,omitempty"`
}

const (
	// syncTimeout bounds a whole history sync. It is longer than a quote
	// refresh's because a first run downloads years for every instrument, where
	// a refresh is one small response each.
	syncTimeout = 5 * time.Minute

	// historyOverlap is how far back a top-up reaches before the last session
	// already stored. The provider revises a close occasionally, and the most
	// recent bar can be provisional while a session is still open, so the last
	// few days are refetched rather than trusted. The upsert makes that free.
	historyOverlap = 5 * 24 * time.Hour
)

// SyncHistory handles POST /api/v1/instruments/sync-history for any
// authenticated user: it downloads the daily closing prices needed to value the
// book on any past date, and reports the outcome per instrument.
//
// It is open on the same terms as the quote refresh, and for the same reason.
// Historical closes are shared, objective market data that no caller owns, and
// every retrospective figure — an equity curve, a drawdown, a comparison against
// an index — is dead without them. Gating it behind an admin would leave
// everyone else looking at empty charts.
//
// **Only instruments that have been traded are fetched, and only from the day
// they were first traded.** Prices before the first trade value nothing, and an
// instrument nobody holds has nothing to plot; skipping both is what keeps a run
// proportional to the book rather than to the master data. Because instruments
// are shared, that first-trade date spans every user's ledger rather than the
// caller's — which leaks no ledger detail, since the resulting prices are public
// market data either way.
//
// A run is incremental: an instrument already holding history is topped up from
// a few days before its last session rather than downloaded again. A partial
// failure is not an error for the run, exactly as in a quote refresh.
func (h *InstrumentHandler) SyncHistory(c *gin.Context) {
	if h.quotes == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "quote fetching is not configured"})
		return
	}

	items, _, err := h.db.ListInstruments(db.ListOptions{Limit: maxLimit}, db.InstrumentFilter{})
	if err != nil {
		respondDBError(c, err)
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), syncTimeout)
	defer cancel()

	results := make([]syncResult, 0, len(items))
	synced, failed, skipped := 0, 0, 0
	for _, item := range items {
		result := h.syncOne(ctx, item)
		switch result.Status {
		case "synced":
			synced++
		case "failed":
			failed++
		default:
			// An untraded instrument is the common case in a fresh book and
			// there is nothing to do about it, so it is counted and left out of
			// the results rather than filling them.
			skipped++
			continue
		}
		results = append(results, result)
	}

	slog.Info("price history synced",
		slog.String(middleware.RequestIDKey, middleware.RequestIDFromContext(c)),
		slog.String("actor_id", callerID(c)),
		slog.Int("synced", synced),
		slog.Int("failed", failed),
		slog.Int("skipped", skipped),
		slog.Int("total", len(items)),
	)

	c.JSON(http.StatusOK, gin.H{"data": gin.H{
		"synced":  synced,
		"failed":  failed,
		"skipped": skipped,
		"results": results,
	}})
}

// syncOne brings one instrument's history up to date, turning every failure into
// a reportable result rather than aborting the run.
func (h *InstrumentHandler) syncOne(ctx context.Context, item models.Instrument) syncResult {
	result := syncResult{InstrumentID: item.ID, Symbol: item.Symbol}

	firstTraded, err := h.db.EarliestTradedAt(item.ID)
	if err != nil {
		result.Status = "failed"
		result.Error = err.Error()
		return result
	}
	if firstTraded == "" {
		result.Status = "skipped"
		result.Error = "never traded, so no history is needed"
		return result
	}

	ticker, ok := quotes.Ticker(item.Symbol, item.Market)
	if !ok {
		result.Status = "skipped"
		result.Error = "no quote source for market " + item.Market
		return result
	}
	result.Ticker = ticker

	from, err := h.syncFrom(item.ID, firstTraded)
	if err != nil {
		result.Status = "failed"
		result.Error = err.Error()
		return result
	}
	to := time.Now().UTC()
	if from.After(to) {
		// A book whose only trade is dated in the future. There is no history to
		// fetch yet, and asking for an inverted window would be an error.
		result.Status = "skipped"
		result.Error = "first trade is in the future"
		return result
	}
	result.From, result.To = from.Format(time.DateOnly), to.Format(time.DateOnly)

	history, err := h.quotes.History(ctx, ticker, from, to)
	if err != nil {
		result.Status = "failed"
		result.Error = err.Error()
		return result
	}

	// The same rule the quote refresh follows: a series in a currency the
	// instrument is not recorded in would reinterpret every cost basis behind
	// it, so it is a failure rather than a price.
	if item.Currency != "" && history.Currency != item.Currency {
		result.Status = "failed"
		result.Error = fmt.Sprintf("provider quotes %s in %s but the instrument is recorded in %s",
			item.Symbol, history.Currency, item.Currency)
		return result
	}

	rows := make([]models.DailyClose, 0, len(history.Closes))
	for _, close := range history.Closes {
		rows = append(rows, models.DailyClose{Date: close.Date, Close: close.Close})
	}
	if err := h.db.SaveDailyCloses(item.ID, rows); err != nil {
		result.Status = "failed"
		result.Error = err.Error()
		return result
	}

	sessions, err := h.db.CountDailyCloses(item.ID)
	if err != nil {
		result.Status = "failed"
		result.Error = err.Error()
		return result
	}

	result.Status = "synced"
	result.Added = len(rows)
	result.Sessions = sessions
	return result
}

// syncFrom picks the day to fetch from: a few days before the last session
// already stored, or the first trade when there is none.
//
// The overlap is deliberate. The most recent bar can still be provisional and
// the provider revises a close now and then, so the last few days are refetched
// and overwritten rather than trusted — which costs nothing, because the write
// is an upsert keyed on the session.
func (h *InstrumentHandler) syncFrom(instrumentID, firstTraded string) (time.Time, error) {
	first, err := time.Parse(time.DateOnly, firstTraded)
	if err != nil {
		return time.Time{}, err
	}

	latest, err := h.db.LatestStoredClose(instrumentID)
	if err != nil {
		return time.Time{}, err
	}
	if latest == "" {
		return first, nil
	}
	stored, err := time.Parse(time.DateOnly, latest)
	if err != nil {
		return time.Time{}, err
	}
	from := stored.Add(-historyOverlap)
	if from.Before(first) {
		return first, nil
	}
	return from, nil
}
