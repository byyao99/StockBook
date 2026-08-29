package db

import (
	"gorm.io/gorm/clause"

	"stockbook/internal/models"
)

// FeeProfiles returns the fee profiles a user has saved, in no particular
// order. A user who has never opened the settings page has none, which is not
// an error: models.DefaultFeeProfiles supplies the values in that case and the
// handler merges them at read time.
//
// The absence of a row is meaningful and must survive to the caller. A stored
// rate of zero is a user telling us their broker charges no commission; a
// missing row is a user who has not said anything. Filling defaults in here
// would erase that difference.
func (d *DB) FeeProfiles(userID string) ([]models.FeeProfile, error) {
	var profiles []models.FeeProfile
	if err := d.db.Where("user_id = ?", userID).Find(&profiles).Error; err != nil {
		return nil, err
	}
	return profiles, nil
}

// SaveFeeProfiles writes a user's fee profiles, replacing any already held for
// the same keys and leaving the rest alone.
//
// The upsert is what lets the settings form save a subset. Inserting blindly
// would fail on the composite key the second time anything is saved, and
// deleting the user's rows first would turn a partial save into a silent reset
// of everything not included in it.
//
// Writing nothing is not an error; a form submitted with nothing changed has
// nothing to say.
func (d *DB) SaveFeeProfiles(userID string, profiles []models.FeeProfile) error {
	if len(profiles) == 0 {
		return nil
	}
	rows := make([]models.FeeProfile, 0, len(profiles))
	for _, p := range profiles {
		p.UserID = userID
		rows = append(rows, p)
	}
	return d.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "user_id"}, {Name: "key"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"rate_ppm", "min_fee", "sell_tax_ppm", "discount_bps", "updated_at",
		}),
	}).Create(&rows).Error
}
