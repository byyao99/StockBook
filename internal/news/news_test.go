package news

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"stockbook/internal/models"
)

// These tests never touch the network: both providers are served from stubs.

// The clock these fixtures are written around.
var (
	newest = time.Date(2026, 8, 29, 6, 0, 0, 0, time.UTC)
	older  = time.Date(2026, 8, 28, 6, 0, 0, 0, time.UTC)
	oldest = time.Date(2026, 8, 27, 6, 0, 0, 0, time.UTC)
)

func instrument(id, symbol, market string) models.Instrument {
	return models.Instrument{ID: id, Symbol: symbol, Market: market}
}

// cnyesPage renders one page of the Taiwanese feed. Each item is a title and the
// tagged symbols the provider attached to it, in its own "TWS:2330:STOCK" form.
func cnyesPage(current, last int, items ...string) string {
	return fmt.Sprintf(`{"items":{"data":[%s],"current_page":%d,"last_page":%d},"message":"成功","statusCode":200}`,
		strings.Join(items, ","), current, last)
}

func cnyesItemJSON(id int64, title string, at time.Time, symbols ...string) string {
	tags := make([]string, 0, len(symbols))
	for _, s := range symbols {
		code := strings.Split(s, ":")[1]
		tags = append(tags, fmt.Sprintf(`{"code":%q,"name":"x","symbol":%q}`, code, s))
	}
	return fmt.Sprintf(`{"newsId":%d,"title":%q,"summary":"s","publishAt":%d,"source":"","market":[%s]}`,
		id, title, at.Unix(), strings.Join(tags, ","))
}

// rssFeedXML renders a Yahoo per-symbol feed.
func rssFeedXML(items ...string) string {
	return `<?xml version="1.0" encoding="UTF-8"?><rss version="2.0"><channel>` +
		strings.Join(items, "") + `</channel></rss>`
}

func rssItemXML(guid, title, link string, at time.Time) string {
	return fmt.Sprintf(
		`<item><title>%s</title><link>%s</link><guid isPermaLink="false">%s</guid><pubDate>%s</pubDate><description>d</description></item>`,
		title, link, guid, at.Format(time.RFC1123Z))
}

// stubs serves both providers. pages is the cnyes feed by page number; feeds is
// the Yahoo RSS by ticker.
func stubs(t *testing.T, pages map[int]string, feeds map[string]string) *Aggregator {
	t.Helper()
	cnyes := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		body, ok := pages[atoi(page)]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(cnyes.Close)

	yahoo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := feeds[r.URL.Query().Get("s")]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(yahoo.Close)

	return newTestAggregator(cnyes.URL, yahoo.URL)
}

func atoi(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	return n
}

func idsOf(articles []Article, id string) []string {
	for _, a := range articles {
		if a.ID == id {
			return a.InstrumentIDs
		}
	}
	return nil
}

// The load-bearing test of this package. An article reaches a holding only
// because the provider tagged it with that holding's code — never because the
// company's name appears in the title, and never because the article merely
// arrived in the same feed.
func TestArticleIsAttributedOnlyToTaggedCodes(t *testing.T) {
	// One real article, tagged by cnyes with three listings and a US ADR.
	page := cnyesPage(1, 1, cnyesItemJSON(6591330,
		"〈熱門股〉精材今明年營運旺 三大法人同步敲進周漲26%", newest,
		"TWS:3374:STOCK", "TWS:2330:STOCK", "TWG:6789:STOCK", "USS:TSM:STOCK"))
	a := stubs(t, map[int]string{1: page}, nil)

	held := []models.Instrument{
		instrument("id-2330", "2330", "TWSE"), // tagged, held
		instrument("id-6789", "6789", "TPEX"), // tagged, held, on the other venue
		instrument("id-2454", "2454", "TWSE"), // held, not tagged
	}

	report := a.Collect(context.Background(), held, time.Time{})
	if len(report.Articles) != 1 {
		t.Fatalf("got %d articles, want 1", len(report.Articles))
	}

	got := report.Articles[0].InstrumentIDs
	want := map[string]bool{"id-2330": true, "id-6789": true}
	if len(got) != len(want) {
		t.Fatalf("attributed to %v, want exactly %v", got, want)
	}
	for _, id := range got {
		if !want[id] {
			t.Errorf("attributed to %s, which the provider never tagged", id)
		}
	}
}

// A held instrument whose code the provider did not tag gets nothing, even
// though the article was in the feed it was scanned from. Under-reporting is the
// failure this package chooses.
func TestAnUntaggedHoldingGetsNothingFromAnArticleInItsFeed(t *testing.T) {
	page := cnyesPage(1, 1, cnyesItemJSON(1, "台積電法說會", newest, "TWS:2330:STOCK"))
	a := stubs(t, map[int]string{1: page}, nil)

	report := a.Collect(context.Background(),
		[]models.Instrument{instrument("id-2454", "2454", "TWSE")}, time.Time{})

	if len(report.Articles) != 0 {
		t.Fatalf("got %d articles for an untagged holding, want none: %+v",
			len(report.Articles), report.Articles)
	}
}

// An article the provider tagged with nothing at all is dropped rather than
// attached to the holdings that happened to be searched for.
func TestAnUntaggedArticleIsDropped(t *testing.T) {
	page := cnyesPage(1, 1, cnyesItemJSON(2, "新創迎黃金期！產創條例修正3大亮點", newest))
	a := stubs(t, map[int]string{1: page}, nil)

	report := a.Collect(context.Background(),
		[]models.Instrument{instrument("id-2330", "2330", "TWSE")}, time.Time{})

	if len(report.Articles) != 0 {
		t.Fatalf("an untagged article was attributed anyway: %+v", report.Articles)
	}
}

// The venue prefix says which country, not which venue. cnyes disagrees with the
// quote provider about which Taiwanese market a listing trades on often enough
// to be useless — 精材 is TPEX and tagged TWS, 采鈺 is TWSE and tagged TWG — and
// cross-checking it silently drops correct articles about held companies. The
// listing code alone is the match, which is safe because Taiwanese codes are
// unique across both venues.
func TestATaiwanTagMatchesTheCodeWhicheverVenueItNames(t *testing.T) {
	page := cnyesPage(1, 1, cnyesItemJSON(3, "台積電法說會", newest, "TWG:2330:STOCK"))
	a := stubs(t, map[int]string{1: page}, nil)

	report := a.Collect(context.Background(),
		[]models.Instrument{instrument("id-2330", "2330", "TWSE")}, time.Time{})

	if len(report.Articles) != 1 {
		t.Fatalf("a mislabelled venue lost the article: %+v", report.Articles)
	}
	if ids := report.Articles[0].InstrumentIDs; len(ids) != 1 || ids[0] != "id-2330" {
		t.Errorf("attributed to %v, want [id-2330]", ids)
	}
}

// The country half of the prefix still matters. A US holding is served in
// English by Yahoo, so letting a Chinese article attach to it as well would
// double-source exactly those holdings and make the language tag mean nothing.
func TestAUSTagDoesNotReachAHolding(t *testing.T) {
	page := cnyesPage(1, 1, cnyesItemJSON(4, "台積電 ADR", newest, "USS:TSM:STOCK"))
	a := stubs(t, map[int]string{1: page}, nil)

	report := a.Collect(context.Background(), []models.Instrument{
		instrument("id-2330", "2330", "TWSE"),
		instrument("id-tsm", "TSM", "NYSE"),
	}, time.Time{})

	for _, article := range report.Articles {
		if article.Source == SourceCnyes {
			t.Errorf("a US tag reached a holding through the Chinese source: %+v", article)
		}
	}
}

// The walk stops once it reaches material already stored. The feed is ordered by
// publication, so meeting the watermark means everything below it is held too.
func TestTheWalkStopsAtTheWatermark(t *testing.T) {
	pages := map[int]string{
		1: cnyesPage(1, 3,
			cnyesItemJSON(10, "new", newest, "TWS:2330:STOCK"),
			cnyesItemJSON(11, "already stored", older, "TWS:2330:STOCK"),
			cnyesItemJSON(12, "older still", oldest, "TWS:2330:STOCK"),
		),
		2: cnyesPage(2, 3, cnyesItemJSON(13, "page two", oldest, "TWS:2330:STOCK")),
	}
	a := stubs(t, pages, nil)

	report := a.Collect(context.Background(),
		[]models.Instrument{instrument("id-2330", "2330", "TWSE")}, older)

	if len(report.Articles) != 1 {
		t.Fatalf("got %d articles, want only the one newer than the watermark: %+v",
			len(report.Articles), report.Articles)
	}
	if report.Articles[0].ID != "cnyes:10" {
		t.Errorf("kept %s, want cnyes:10", report.Articles[0].ID)
	}
}

// A US holding is served in English by Yahoo, attributed by the request itself:
// the feed was asked for by that instrument's ticker.
func TestUSHoldingsAreServedByTicker(t *testing.T) {
	feeds := map[string]string{
		"AAPL": rssFeedXML(rssItemXML("guid-1", "Apple raises prices",
			"https://www.foxbusiness.com/technology/apple", newest)),
	}
	a := stubs(t, nil, feeds)

	report := a.Collect(context.Background(),
		[]models.Instrument{instrument("id-aapl", "AAPL", "NASDAQ")}, time.Time{})

	if len(report.Articles) != 1 {
		t.Fatalf("got %d articles, want 1: %+v", len(report.Articles), report.Articles)
	}
	got := report.Articles[0]
	if got.ID != "yahoo:guid-1" {
		t.Errorf("id = %q, want yahoo:guid-1", got.ID)
	}
	if ids := got.InstrumentIDs; len(ids) != 1 || ids[0] != "id-aapl" {
		t.Errorf("attributed to %v, want [id-aapl]", ids)
	}
	if got.Language != yahooLanguage {
		t.Errorf("language = %q, want %q", got.Language, yahooLanguage)
	}
	// The feed carries no publisher, and the host is the one factual answer.
	if got.Publisher != "foxbusiness.com" {
		t.Errorf("publisher = %q, want foxbusiness.com", got.Publisher)
	}
}

// A link is the one provider string this system hands to a browser rather than
// rendering as escaped text, so it is the one that has to be checked. Anything
// that is not plain http(s) is dropped: there is no correct repair for a link
// that is not a link.
func TestAHeadlineWithANonWebLinkIsDropped(t *testing.T) {
	for _, link := range []string{
		"javascript:alert(1)",
		"data:text/html;base64,PHNjcmlwdD5hbGVydCgxKTwvc2NyaXB0Pg==",
		"file:///etc/passwd",
		"",
	} {
		feeds := map[string]string{
			"AAPL": rssFeedXML(rssItemXML("guid-x", "t", link, newest)),
		}
		a := stubs(t, nil, feeds)

		report := a.Collect(context.Background(),
			[]models.Instrument{instrument("id-aapl", "AAPL", "NASDAQ")}, time.Time{})

		if len(report.Articles) != 0 {
			t.Errorf("a %q link was stored: %+v", link, report.Articles)
		}
	}
}

// A headline whose date cannot be read is dropped rather than stamped with now,
// which would sort it to the top of the feed forever.
func TestAnUndatedHeadlineIsDroppedRatherThanStampedNow(t *testing.T) {
	feeds := map[string]string{
		"AAPL": rssFeedXML(
			`<item><title>t</title><link>https://example.com/a</link><guid>g</guid><pubDate>whenever</pubDate></item>`,
		),
	}
	a := stubs(t, nil, feeds)

	report := a.Collect(context.Background(),
		[]models.Instrument{instrument("id-aapl", "AAPL", "NASDAQ")}, time.Time{})

	if len(report.Articles) != 0 {
		t.Fatalf("an undated headline was kept: %+v", report.Articles)
	}
}

// One provider being down must not cost the reader the other's headlines.
func TestOneSourceFailingDoesNotStopTheOther(t *testing.T) {
	// No cnyes page is staged, so the firehose 404s.
	feeds := map[string]string{
		"AAPL": rssFeedXML(rssItemXML("guid-2", "Apple", "https://example.com/a", newest)),
	}
	a := stubs(t, map[int]string{}, feeds)

	report := a.Collect(context.Background(), []models.Instrument{
		instrument("id-2330", "2330", "TWSE"),
		instrument("id-aapl", "AAPL", "NASDAQ"),
	}, time.Time{})

	if len(report.Articles) != 1 || idsOf(report.Articles, "yahoo:guid-2") == nil {
		t.Fatalf("the working source's article was lost: %+v", report.Articles)
	}

	var failed, synced int
	for _, r := range report.Results {
		switch r.Status {
		case "failed":
			failed++
		case "synced":
			synced++
		}
	}
	if failed != 1 || synced != 1 {
		t.Errorf("results = %+v, want one failed and one synced", report.Results)
	}
}

// A failed source names the holdings it could not speak for, so the caller knows
// not to stamp them as checked.
func TestAFailedSourceStillNamesWhatItCovered(t *testing.T) {
	a := stubs(t, map[int]string{}, nil)

	report := a.Collect(context.Background(),
		[]models.Instrument{instrument("id-2330", "2330", "TWSE")}, time.Time{})

	if len(report.Results) != 1 {
		t.Fatalf("got %d results, want 1", len(report.Results))
	}
	result := report.Results[0]
	if result.Status != "failed" {
		t.Fatalf("status = %q, want failed", result.Status)
	}
	if len(result.Covered) != 1 || result.Covered[0] != "id-2330" {
		t.Errorf("covered = %v, want [id-2330]", result.Covered)
	}
}

// A market with no news source is skipped by name rather than failing, so the
// reader is told why a holding has no headlines.
func TestAnUnsupportedMarketIsSkippedWithAReason(t *testing.T) {
	a := stubs(t, nil, nil)

	report := a.Collect(context.Background(),
		[]models.Instrument{instrument("id-x", "XYZ", "OTHER")}, time.Time{})

	if len(report.Results) != 1 || report.Results[0].Status != "skipped" {
		t.Fatalf("results = %+v, want one skipped", report.Results)
	}
	if !strings.Contains(report.Results[0].Error, "OTHER") {
		t.Errorf("reason %q does not name the market", report.Results[0].Error)
	}
}
