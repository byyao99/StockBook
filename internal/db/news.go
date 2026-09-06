package db

import (
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"stockbook/internal/models"
)

// NewsView is one article shaped for the feed: the stored row plus the symbols
// of the caller's holdings it was tagged with.
//
// Symbols is what makes a merged feed readable — a headline with no ticker
// beside it is just a headline, and the whole point of this list is that every
// item on it concerns something the reader owns. It carries only holdings they
// actually hold, so an article tagged with three companies of which they own one
// shows one badge.
type NewsView struct {
	models.NewsItem
	Symbols []string `json:"symbols"`
}

// NewsFilter narrows the feed to one holding.
type NewsFilter struct {
	InstrumentID string
}

// newsSortColumns maps the sort keys clients may use to qualified column names.
// The query spans four tables, so qualification is required as well as being the
// injection guard: a key absent from this map cannot reach SQL at all.
var newsSortColumns = map[string]string{
	"published_at": "news_items.published_at",
	"source":       "news_items.source",
}

// newsOrderClause builds a safe ORDER BY fragment for the joined feed query.
// A feed is read newest-first, so unlike every other list here the default
// direction is descending.
func newsOrderClause(opts ListOptions) string {
	col, ok := newsSortColumns[opts.Sort]
	if !ok {
		col = "news_items.published_at"
	}
	dir := "desc"
	switch strings.ToLower(opts.Order) {
	case "asc":
		dir = "asc"
	case "desc":
		dir = "desc"
	}
	return col + " " + dir
}

// SaveNews stores articles and their attributions, ignoring any already held.
//
// The conflict clause does nothing rather than updating, which is the opposite
// of how a daily close is written and right for the opposite reason: a session's
// close is revised by the provider after the fact, while a published headline is
// finished. Re-reading a feed that overlaps what is already stored is the normal
// case — it is how the walk knows it has caught up — so the collision is
// expected rather than exceptional.
//
// Writing nothing is not an error: a run that found no new articles is the
// ordinary outcome of pressing the button twice.
func (d *DB) SaveNews(items []models.NewsItem, mentions []models.NewsMention) error {
	if len(items) == 0 {
		return nil
	}
	if err := d.db.Clauses(clause.OnConflict{DoNothing: true}).
		CreateInBatches(items, 200).Error; err != nil {
		return err
	}
	if len(mentions) == 0 {
		return nil
	}
	return d.db.Clauses(clause.OnConflict{DoNothing: true}).
		CreateInBatches(mentions, 200).Error
}

// LatestNewsAt returns the publication time of the newest article held from one
// source, or the zero time when none is.
//
// This is the watermark an incremental collection walks down to, and it is the
// stored data itself rather than a separate bookkeeping row — the same trick
// LatestStoredClose plays.
//
// The newest row is read through the model rather than as a bare
// MAX(published_at), for the reason EarliestTradedAt is: the pure-Go SQLite
// driver hands an aggregate back as the raw value it stored, which will not scan
// into a time.Time, and over an empty table it hands back a NULL that will not
// scan at all. An empty table is the ordinary case on a first run and has to
// read as "nothing stored" rather than as a failure — one that would otherwise
// abort the collection before it started.
func (d *DB) LatestNewsAt(source string) (time.Time, error) {
	var newest models.NewsItem
	err := d.db.Where("source = ?", source).
		Order("published_at desc").First(&newest).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, err
	}
	return newest.PublishedAt.UTC(), nil
}

// heldNewsQuery is the shared join from articles to the caller's open holdings.
//
// The scoping lives here rather than being applied afterwards, like every other
// personal read in this package. The articles themselves are shared objective
// data, but which of them reaches a reader is decided entirely by what that
// reader owns, so an admin gets no more of this feed than anybody else.
//
// A closed holding drops out. News about a company sold last year answers
// nothing a reader can act on, and "If you had never sold" is where that
// question already lives.
func (d *DB) heldNewsQuery(userID string, filter NewsFilter) *gorm.DB {
	q := d.db.Model(&models.NewsItem{}).
		Joins("JOIN news_mentions ON news_mentions.news_id = news_items.id").
		Joins("JOIN positions ON positions.instrument_id = news_mentions.instrument_id").
		Where("positions.user_id = ? AND positions.quantity > 0", userID)
	if filter.InstrumentID != "" {
		q = q.Where("news_mentions.instrument_id = ?", filter.InstrumentID)
	}
	return q
}

// ListHeldNews returns a page of articles concerning userID's open holdings,
// newest first, plus the total count.
//
// One article can be tagged with several holdings, so both the page and the
// count are over distinct articles: a piece naming three stocks a reader owns is
// one row on their feed carrying three badges, not three rows.
func (d *DB) ListHeldNews(userID string, filter NewsFilter, opts ListOptions) ([]NewsView, int64, error) {
	var total int64
	if err := d.heldNewsQuery(userID, filter).
		Distinct("news_items.id").Count(&total).Error; err != nil {
		return nil, 0, err
	}

	items := []models.NewsItem{}
	q := d.heldNewsQuery(userID, filter).
		Select("DISTINCT news_items.*").
		Order(newsOrderClause(opts)).
		Offset(opts.Offset)
	if opts.Limit > 0 {
		q = q.Limit(opts.Limit)
	}
	if err := q.Find(&items).Error; err != nil {
		return nil, 0, err
	}
	if len(items) == 0 {
		return []NewsView{}, total, nil
	}

	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	symbols, err := d.heldMentionSymbols(userID, ids)
	if err != nil {
		return nil, 0, err
	}

	views := make([]NewsView, 0, len(items))
	for _, item := range items {
		views = append(views, NewsView{NewsItem: item, Symbols: symbols[item.ID]})
	}
	return views, total, nil
}

// heldMentionSymbols returns, per article, the symbols of the caller's holdings
// it was tagged with.
//
// This is a second query rather than a join onto the page because the
// relationship is one-to-many: folding it into the page query would multiply the
// rows back out and undo the DISTINCT the count was taken over.
func (d *DB) heldMentionSymbols(userID string, newsIDs []string) (map[string][]string, error) {
	type mentionRow struct {
		NewsID string
		Symbol string
	}
	rows := []mentionRow{}
	err := d.db.Model(&models.NewsMention{}).
		Select("news_mentions.news_id AS news_id, instruments.symbol AS symbol").
		Joins("JOIN instruments ON instruments.id = news_mentions.instrument_id").
		Joins("JOIN positions ON positions.instrument_id = news_mentions.instrument_id").
		Where("positions.user_id = ? AND positions.quantity > 0", userID).
		Where("news_mentions.news_id IN ?", newsIDs).
		Order("instruments.symbol asc").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}

	byNews := make(map[string][]string, len(newsIDs))
	for _, row := range rows {
		byNews[row.NewsID] = append(byNews[row.NewsID], row.Symbol)
	}
	return byNews, nil
}

// HeldInstruments returns the instruments userID currently holds shares in, in
// symbol order.
//
// This is what a collection run is proportional to. Fetching news for the whole
// master data would reach the providers on behalf of companies nobody owns, and
// fetching it for closed holdings would fill the feed with companies the reader
// has already left — the same reasoning that keeps a history sync to instruments
// that have actually been traded.
func (d *DB) HeldInstruments(userID string) ([]models.Instrument, error) {
	items := []models.Instrument{}
	err := d.db.Model(&models.Instrument{}).
		Joins("JOIN positions ON positions.instrument_id = instruments.id").
		Where("positions.user_id = ? AND positions.quantity > 0", userID).
		Order("instruments.symbol asc").
		Find(&items).Error
	return items, err
}
