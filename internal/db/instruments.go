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
func (d *DB) UpdateInstrument(id string, item models.Instrument) (models.Instrument, error) {
	res := d.db.Model(&models.Instrument{}).Where("id = ?", id).Updates(map[string]any{
		"symbol": item.Symbol,
		"name":   item.Name,
		"market": item.Market,
	})
	if res.Error != nil {
		if errors.Is(res.Error, gorm.ErrDuplicatedKey) {
			return models.Instrument{}, ErrSymbolTaken
		}
		return models.Instrument{}, res.Error
	}
	if res.RowsAffected == 0 {
		return models.Instrument{}, ErrNotFound
	}
	return d.GetInstrument(id)
}

// UpdateInstrumentPrice sets (or clears, when price is nil) an instrument's
// quote and stamps when it was set. Clearing the price also clears the stamp, so
// a nil quote never carries a misleading "as of" time.
func (d *DB) UpdateInstrumentPrice(id string, price *int64) (models.Instrument, error) {
	updates := map[string]any{"last_price": price, "price_updated_at": nil}
	if price != nil {
		now := time.Now()
		updates["price_updated_at"] = &now
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
