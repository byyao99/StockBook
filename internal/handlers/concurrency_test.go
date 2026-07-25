package handlers_test

import (
	"net/http"
	"sync"
	"testing"
	"time"

	"stockbook/internal/db"
	"stockbook/internal/models"
)

// These tests are only meaningful under `go test -race`, which CI runs.

// Two clients selling the same last share must not both succeed. The check
// ("do I hold enough?") and the write ("take them") are separated by a read, so
// without the compare-and-swap in the position write both would read one share
// held and both would proceed, leaving a negative holding.
func TestConcurrentSellsCannotOversell(t *testing.T) {
	const attempts = 8

	e := setup(t)
	user := e.token(t, "trader", models.RoleUser)
	inst := e.seedInstrument(t, "2330", nil)
	past := time.Now().Add(-2 * time.Hour)

	rec := e.do(t, http.MethodPost, "/api/v1/transactions",
		tradePayload(inst.ID, models.SideBuy, 1, 90000, past), user)
	if rec.Code != http.StatusCreated {
		t.Fatalf("seed buy: got %d (body: %s)", rec.Code, rec.Body.String())
	}

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		codes   = map[int]int{}
		start   = make(chan struct{})
		sellAt  = past.Add(time.Hour)
		payload = tradePayload(inst.ID, models.SideSell, 1, 95000, sellAt)
	)
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start // release everyone at once
			rec := e.do(t, http.MethodPost, "/api/v1/transactions", payload, user)
			mu.Lock()
			codes[rec.Code]++
			mu.Unlock()
		}()
	}
	close(start)
	wg.Wait()

	if codes[http.StatusCreated] != 1 {
		t.Errorf("%d sells succeeded, want exactly 1 (codes: %v)", codes[http.StatusCreated], codes)
	}
	if codes[http.StatusConflict] != attempts-1 {
		t.Errorf("%d sells conflicted, want %d (codes: %v)", codes[http.StatusConflict], attempts-1, codes)
	}

	// Read the holding back through the API: one share bought, one sold, so the
	// position must be flat with no cost left behind and no negative quantity.
	position := readPosition(t, e, user, "2330")
	if position.Quantity != 0 {
		t.Errorf("quantity = %d, want 0", position.Quantity)
	}
	if position.CostBasis != 0 {
		t.Errorf("cost_basis = %d, want 0", position.CostBasis)
	}
	assertMatchesReplay(t, e, user, inst.ID)
}

// Concurrent buys into a holding that does not exist yet race to create the same
// row. The unique index over (user_id, instrument_id) is what keeps them from
// each inserting one; whichever loses retries as a conflict rather than
// producing a duplicate holding.
func TestConcurrentBuysAccumulateExactlyOnce(t *testing.T) {
	const buyers = 8

	e := setup(t)
	user := e.token(t, "trader", models.RoleUser)
	inst := e.seedInstrument(t, "2330", nil)
	past := time.Now().Add(-2 * time.Hour)

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		created int
		start   = make(chan struct{})
		payload = tradePayload(inst.ID, models.SideBuy, 10, 90000, past)
	)
	for i := 0; i < buyers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			rec := e.do(t, http.MethodPost, "/api/v1/transactions", payload, user)
			mu.Lock()
			if rec.Code == http.StatusCreated {
				created++
			}
			mu.Unlock()
		}()
	}
	close(start)
	wg.Wait()

	// However many got in, the holding must account for exactly those and no
	// others — no lost update, no double count.
	position := readPosition(t, e, user, "2330")
	if want := created * 10; position.Quantity != want {
		t.Errorf("quantity = %d, want %d (from %d accepted buys)", position.Quantity, want, created)
	}
	if want := int64(created) * 10 * 90000; position.CostBasis != want {
		t.Errorf("cost_basis = %d, want %d", position.CostBasis, want)
	}
	assertMatchesReplay(t, e, user, inst.ID)
}

// Concurrent deletes of different entries all replay the same position. Whatever
// interleaving occurs, the surviving ledger and the stored position must agree.
func TestConcurrentDeletesLeavePositionConsistent(t *testing.T) {
	e := setup(t)
	user := e.token(t, "trader", models.RoleUser)
	inst := e.seedInstrument(t, "2330", nil)
	past := time.Now().Add(-24 * time.Hour)

	ids := make([]string, 0, 6)
	for i := 0; i < 6; i++ {
		rec := e.do(t, http.MethodPost, "/api/v1/transactions",
			tradePayload(inst.ID, models.SideBuy, 10, int64(90000+i*100), past.Add(time.Duration(i)*time.Hour)), user)
		if rec.Code != http.StatusCreated {
			t.Fatalf("seed buy %d: got %d (body: %s)", i, rec.Code, rec.Body.String())
		}
		var created models.Transaction
		decodeData(t, rec, &created)
		ids = append(ids, created.ID)
	}

	var wg sync.WaitGroup
	start := make(chan struct{})
	for _, id := range ids[:3] { // delete half of them at once
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			<-start
			e.do(t, http.MethodDelete, "/api/v1/transactions/"+id, nil, user)
		}(id)
	}
	close(start)
	wg.Wait()

	assertMatchesReplay(t, e, user, inst.ID)
}

// readPosition fetches one holding through the API by symbol.
func readPosition(t *testing.T, e *testEnv, token, symbol string) db.PositionView {
	t.Helper()
	rec := e.do(t, http.MethodGet, "/api/v1/positions?include_closed=true", nil, token)
	if rec.Code != http.StatusOK {
		t.Fatalf("list positions: got %d (body: %s)", rec.Code, rec.Body.String())
	}
	var positions []db.PositionView
	decodeData(t, rec, &positions)
	for _, p := range positions {
		if p.Symbol == symbol {
			return p
		}
	}
	t.Fatalf("no position for %s in %+v", symbol, positions)
	return db.PositionView{}
}

// assertMatchesReplay is the invariant every concurrent path must preserve: the
// materialized position still equals what replaying the ledger produces.
func assertMatchesReplay(t *testing.T, e *testEnv, token, instrumentID string) {
	t.Helper()
	var userID string
	claims, err := e.am.Verify(token)
	if err != nil {
		t.Fatalf("verify token: %v", err)
	}
	userID = claims.Subject

	replayed, err := e.s.ReplayPosition(userID, instrumentID)
	if err != nil {
		t.Fatalf("ReplayPosition: %v", err)
	}
	stored, err := e.s.GetPosition(userID, instrumentID)
	if err != nil {
		t.Fatalf("GetPosition: %v", err)
	}
	got := models.PositionState{
		Quantity:   stored.Quantity,
		CostBasis:  stored.CostBasis,
		RealizedPL: stored.RealizedPL,
	}
	if got != replayed {
		t.Errorf("materialized %+v != replayed %+v", got, replayed)
	}
}
