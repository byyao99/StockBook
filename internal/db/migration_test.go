package db

import (
	"path/filepath"
	"testing"

	"stockbook/internal/models"
)

// The conversion from whole shares to scaled units must happen exactly once.
// Nothing in the data says whether it already has — 100 is a plausible number of
// whole shares and a plausible 0.0001 of one — so running it twice would
// multiply every holding by a million with no way back.
func TestShareScalingRunsExactlyOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "scale.db")

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	user := seedUser(t, s, "legacy")
	inst := seedInstrument(t, s, "2330")
	mustRecord(t, s, user.ID, inst.ID, entry{models.SideBuy, 100, 10_000, 0, 0})

	before := storedState(t, s, user.ID, inst.ID)
	if before.Quantity != shares(100) {
		t.Fatalf("quantity %d, want %d", before.Quantity, shares(100))
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Reopening runs every startup backfill again, as a restart does.
	again, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { again.Close() })

	after := storedState(t, again, user.ID, inst.ID)
	if after != before {
		t.Errorf("reopening changed the position: %+v, was %+v", after, before)
	}
}

// A book written before the change holds whole shares, and every one of them
// has to be multiplied — the ledger and the positions cache together, since a
// replay would read the very numbers being corrected.
func TestShareScalingConvertsALegacyBook(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")

	// Write a book, then put it back the way it looked before scaling existed.
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	user := seedUser(t, s, "legacy")
	inst := seedInstrument(t, s, "2330")
	mustRecord(t, s, user.ID, inst.ID, entry{models.SideBuy, 100, 10_000, 0, 0})

	if err := s.db.Model(&models.Transaction{}).Where("1 = 1").
		Update("quantity", 100).Error; err != nil {
		t.Fatalf("unscale transactions: %v", err)
	}
	if err := s.db.Model(&models.Position{}).Where("1 = 1").
		Update("quantity", 100).Error; err != nil {
		t.Fatalf("unscale positions: %v", err)
	}
	if err := s.db.Where("key = ?", models.SharesScaledKey).
		Delete(&models.SchemaMeta{}).Error; err != nil {
		t.Fatalf("clear marker: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	converted, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { converted.Close() })

	got := storedState(t, converted, user.ID, inst.ID)
	if got.Quantity != shares(100) {
		t.Errorf("position quantity %d, want %d", got.Quantity, shares(100))
	}
	var tx models.Transaction
	if err := converted.db.First(&tx).Error; err != nil {
		t.Fatalf("read transaction: %v", err)
	}
	if tx.Quantity != shares(100) {
		t.Errorf("ledger quantity %d, want %d", tx.Quantity, shares(100))
	}
}
