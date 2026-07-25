package db

import (
	"errors"
	"time"

	"gorm.io/gorm"

	"stockbook/internal/models"
)

// CreateInstrument persists a new instrument. The caller supplies the ID and a
// normalized (uppercased) symbol. It returns ErrSymbolTaken if the symbol is
// already in use.
func (d *DB) CreateInstrument(item models.Instrument) (models.Instrument, error) {
	if err := d.db.Create(&item).Error; err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return models.Instrument{}, ErrSymbolTaken
		}
		return models.Instrument{}, err
	}
	return item, nil
}

// GetInstrument returns an instrument by ID, or ErrNotFound.
func (d *DB) GetInstrument(id string) (models.Instrument, error) {
	var item models.Instrument
	if err := d.db.First(&item, "id = ?", id).Error; err != nil {
		return models.Instrument{}, translate(err)
	}
	return item, nil
}

// GetInstrumentBySymbol returns an instrument by its symbol, or ErrNotFound.
func (d *DB) GetInstrumentBySymbol(symbol string) (models.Instrument, error) {
	var item models.Instrument
	if err := d.db.First(&item, "symbol = ?", symbol).Error; err != nil {
		return models.Instrument{}, translate(err)
	}
	return item, nil
}

// InstrumentFilter narrows a ListInstruments query. Zero-value fields are
// ignored. Market matches exactly; Q matches a substring of either the symbol
// or the name.
type InstrumentFilter struct {
	Market string
	Q      string
}

// ListInstruments returns a page of instruments matching filter, plus the total
// count of matches. Defaults to symbol order.
func (d *DB) ListInstruments(opts ListOptions, filter InstrumentFilter) ([]models.Instrument, int64, error) {
	apply := func(q *gorm.DB) *gorm.DB {
		if filter.Market != "" {
			q = q.Where("market = ?", filter.Market)
		}
		if filter.Q != "" {
			like := "%" + filter.Q + "%"
			q = q.Where("symbol LIKE ? OR name LIKE ?", like, like)
		}
		return q
	}

	var total int64
	if err := apply(d.db.Model(&models.Instrument{})).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	items := []models.Instrument{}
	q := apply(d.db.Model(&models.Instrument{})).
		Order(orderClause(opts, instrumentSortColumns, "symbol", "asc")).
		Offset(opts.Offset)
	if opts.Limit > 0 {
		q = q.Limit(opts.Limit)
	}
	if err := q.Find(&items).Error; err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

// UpdateInstrument overwrites an instrument's descriptive fields. The quote is
// not touched here — see UpdateInstrumentPrice. It returns ErrNotFound if no
// such instrument exists, or ErrSymbolTaken if the new symbol collides.
//
// Changing the currency once trades exist is refused with ErrCurrencyLocked:
// every historical price and cost basis was recorded in the old currency, and
// reinterpreting them wholesale would corrupt the book silently. The check and
// the write share a transaction so a trade cannot be entered between them.
func (d *DB) UpdateInstrument(id string, item models.Instrument) (models.Instrument, error) {
	err := d.db.Transaction(func(tx *gorm.DB) error {
		var existing models.Instrument
		if err := tx.First(&existing, "id = ?", id).Error; err != nil {
			return translate(err)
		}
		if item.Currency != existing.Currency {
			var traded int64
			if err := tx.Model(&models.Transaction{}).
				Where("instrument_id = ?", id).Count(&traded).Error; err != nil {
				return err
			}
			if traded > 0 {
				return ErrCurrencyLocked
			}
		}

		res := tx.Model(&models.Instrument{}).Where("id = ?", id).Updates(map[string]any{
			"symbol":   item.Symbol,
			"name":     item.Name,
			"market":   item.Market,
			"currency": item.Currency,
		})
		if res.Error != nil {
			if errors.Is(res.Error, gorm.ErrDuplicatedKey) {
				return ErrSymbolTaken
			}
			return res.Error
		}
		return nil
	})
	if err != nil {
		return models.Instrument{}, err
	}
	return d.GetInstrument(id)
}

// QuoteUpdate carries a new quote and the two timestamps that describe it.
//
// AsOf is when the price was good for — a fetched quote passes the market
// timestamp the provider reported, so the staleness shown in the UI reflects the
// price's own age rather than when we happened to ask. CheckedAt is when the
// provider was last asked, which is what decides whether asking again is worth
// an outbound call. They are genuinely different: a Friday closing price fetched
// on Monday is a day old to a reader and a moment old to the fetcher.
//
// Both default to now when nil, which is right for a hand-entered quote.
type QuoteUpdate struct {
	Price     *int64
	AsOf      *time.Time
	CheckedAt *time.Time
}

// UpdateInstrumentPrice sets (or clears, when Price is nil) an instrument's
// quote and its timestamps. Clearing the price clears both stamps, so a nil
// quote never carries a misleading "as of" time.
func (d *DB) UpdateInstrumentPrice(id string, q QuoteUpdate) (models.Instrument, error) {
	price := q.Price
	updates := map[string]any{
		"last_price":       price,
		"price_updated_at": nil,
		"quote_checked_at": nil,
	}
	if price != nil {
		asOf := time.Now()
		if q.AsOf != nil {
			asOf = *q.AsOf
		}
		checkedAt := time.Now()
		if q.CheckedAt != nil {
			checkedAt = *q.CheckedAt
		}
		updates["price_updated_at"] = &asOf
		updates["quote_checked_at"] = &checkedAt
	}
	res := d.db.Model(&models.Instrument{}).Where("id = ?", id).Updates(updates)
	if res.Error != nil {
		return models.Instrument{}, res.Error
	}
	if res.RowsAffected == 0 {
		return models.Instrument{}, ErrNotFound
	}
	return d.GetInstrument(id)
}

// SetInstrumentCurrency records a currency on an instrument that has none yet.
// It is used to adopt the currency a quote provider reports during backfill, and
// refuses to overwrite a currency that is already set.
func (d *DB) SetInstrumentCurrency(id string, currency models.Currency) error {
	res := d.db.Model(&models.Instrument{}).
		Where("id = ? AND (currency IS NULL OR currency = '')", id).
		Update("currency", currency)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrCurrencyLocked
	}
	return nil
}

// DeleteInstrument removes an instrument. It refuses with ErrInstrumentInUse
// when any transaction references it: deleting would orphan ledger rows whose
// symbol snapshot cannot reconstruct a position. It returns ErrNotFound if no
// such instrument exists.
//
// The reference check and the delete share one transaction so a transaction
// inserted concurrently cannot slip in between them.
func (d *DB) DeleteInstrument(id string) error {
	return d.db.Transaction(func(tx *gorm.DB) error {
		var referencing int64
		if err := tx.Model(&models.Transaction{}).Where("instrument_id = ?", id).Count(&referencing).Error; err != nil {
			return err
		}
		if referencing > 0 {
			return ErrInstrumentInUse
		}
		// Positions with no transactions behind them are empty shells; clear
		// them out so a re-created instrument does not inherit a stale holding.
		if err := tx.Delete(&models.Position{}, "instrument_id = ?", id).Error; err != nil {
			return err
		}
		res := tx.Delete(&models.Instrument{}, "id = ?", id)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return ErrNotFound
		}
		return nil
	})
}
