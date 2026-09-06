package news

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"stockbook/internal/models"
)

// cnyesMarkets is the set of this system's markets this source covers, and so
// the routing rule in Collect: everything else is served in English by Yahoo.
var cnyesMarkets = map[string]bool{
	"TWSE": true,
	"TPEX": true,
}

// cnyesTaiwanVenues are the venue prefixes cnyes puts on a Taiwanese listing in
// a tagged symbol ("TWS:2330:STOCK").
//
// **The prefix says which country, not which venue, and the difference is
// load-bearing.** TWS reads as the exchange and TWG as Gretai — the old name of
// the over-the-counter market — so the obvious rule is to cross-check them
// against the instrument's own market, the way FromTicker cross-checks an
// exchange against a ticker suffix. Measured against the quote provider, that
// rule is wrong often enough to be useless:
//
//	2330 台積電  is TWSE  and cnyes tags it TWS  ✓
//	3374 精材    is TPEX  and cnyes tags it TWS  ✗
//	6789 采鈺    is TWSE  and cnyes tags it TWG  ✗
//
// Two of three disagree, and the cost is silent: a correct article about a held
// company simply never appears. So the venue is used only to tell a Taiwanese
// listing from a foreign one, and the code alone decides which holding it is.
// That is safe because Taiwanese listing codes are centrally allocated and
// unique across both venues — there is no second 2330 — while the US prefix has
// to stay excluded, since a US holding is served in English by Yahoo and letting
// a Chinese article attach to it as well would double-source exactly those
// holdings and make the feed's language tag mean nothing.
var cnyesTaiwanVenues = map[string]bool{
	"TWS": true,
	"TWG": true,
}

const (
	// cnyesPageSize is the provider's own maximum useful page.
	cnyesPageSize = 30

	// cnyesMaxPages bounds a run. The category feed is an archive going back
	// years; without a ceiling a first sync against an empty table would walk
	// all of it. Five pages is roughly a day and a half of Taiwanese market
	// coverage, which is what a reader opening the page actually wants, and a
	// routine run stops at the watermark long before reaching it.
	cnyesMaxPages = 5

	// cnyesLanguage is what every article from this source is written in.
	cnyesLanguage = "zh-TW"
)

// cnyesResponse is the slice of the category feed this package reads.
type cnyesResponse struct {
	Items struct {
		Data        []cnyesItem `json:"data"`
		CurrentPage int         `json:"current_page"`
		LastPage    int         `json:"last_page"`
	} `json:"items"`
	Message    string `json:"message"`
	StatusCode int    `json:"statusCode"`
}

// cnyesItem is one article.
//
// Market is the attribution and the only field this package trusts for it. The
// sibling `stock` field carries the same codes without saying which venue lists
// them, and was measured to be strictly poorer: over a sampled page it named
// nothing that Market did not, and was empty for six articles Market described.
type cnyesItem struct {
	NewsID    int64  `json:"newsId"`
	Title     string `json:"title"`
	Summary   string `json:"summary"`
	PublishAt int64  `json:"publishAt"`
	Source    string `json:"source"`
	Market    []struct {
		Code   string `json:"code"`
		Name   string `json:"name"`
		Symbol string `json:"symbol"`
	} `json:"market"`
}

// collectCnyes walks the Taiwanese market feed, newest first, and returns the
// articles that name one of the given holdings.
//
// The walk stops at the first article at or older than since, which is the
// newest one already stored. The feed is strictly ordered by publication, so
// reaching material already held means everything below it is held too — the
// same reasoning that lets a history sync start from its last stored session
// rather than re-downloading years.
func (a *Aggregator) collectCnyes(ctx context.Context, held []models.Instrument, since time.Time) ([]Article, SourceResult) {
	result := SourceResult{
		Source: SourceCnyes,
		Scope:  fmt.Sprintf("%d Taiwanese holdings", len(held)),
	}
	for _, item := range held {
		result.Covered = append(result.Covered, item.ID)
	}

	// Index the holdings by listing code, so attribution is a lookup rather than
	// a scan per article. The code is the whole key; see cnyesTaiwanVenues for
	// why the venue is not part of it.
	byCode := make(map[string]string, len(held))
	for _, item := range held {
		if !cnyesMarkets[item.Market] {
			continue
		}
		byCode[strings.ToUpper(item.Symbol)] = item.ID
	}

	articles := []Article{}
	for page := 1; page <= cnyesMaxPages; page++ {
		body, err := a.cnyesPage(ctx, page)
		if err != nil {
			result.Status = "failed"
			result.Error = err.Error()
			result.Fetched = len(articles)
			return articles, result
		}

		caughtUp := false
		for _, item := range body.Items.Data {
			published := time.Unix(item.PublishAt, 0).UTC()
			if !published.After(since) {
				caughtUp = true
				break
			}
			ids := cnyesAttribution(item, byCode)
			if len(ids) == 0 {
				// Either the article names no listed company, or it names none
				// this book holds. Both are ordinary, and neither is worth
				// storing: an article reaches the feed through a holding.
				continue
			}
			articles = append(articles, Article{
				ID:            SourceCnyes + ":" + strconv.FormatInt(item.NewsID, 10),
				Source:        SourceCnyes,
				Title:         strings.TrimSpace(item.Title),
				Summary:       strings.TrimSpace(item.Summary),
				URL:           fmt.Sprintf("https://news.cnyes.com/news/id/%d", item.NewsID),
				Publisher:     cnyesPublisher(item.Source),
				Language:      cnyesLanguage,
				PublishedAt:   published,
				InstrumentIDs: ids,
			})
		}
		if caughtUp || body.Items.CurrentPage >= body.Items.LastPage || len(body.Items.Data) == 0 {
			break
		}
	}

	result.Status = "synced"
	result.Fetched = len(articles)
	return articles, result
}

// cnyesAttribution resolves one article's tags to the holdings it concerns.
//
// This is the rule the whole package exists to enforce, and the narrowest part
// of it: a holding is named only when the provider's own structured tag carries
// its listing code. An article about 精材 that mentions 台積電 in passing is
// attributed to both because cnyes tagged both — that is the provider's
// editorial call to make, and it is a far better one than any string matching
// this code could do on a title.
func cnyesAttribution(item cnyesItem, byCode map[string]string) []string {
	seen := make(map[string]bool)
	ids := []string{}
	for _, tag := range item.Market {
		venue, code, ok := parseCnyesSymbol(tag.Symbol)
		if !ok || !cnyesTaiwanVenues[venue] {
			continue
		}
		id, held := byCode[code]
		if !held || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	return ids
}

// parseCnyesSymbol splits a tagged symbol ("TWS:2330:STOCK") into its venue and
// code. A symbol shaped any other way is refused rather than guessed at.
func parseCnyesSymbol(symbol string) (venue, code string, ok bool) {
	parts := strings.Split(strings.ToUpper(strings.TrimSpace(symbol)), ":")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// cnyesPublisher names who wrote the article. The feed leaves the field empty
// for the site's own reporting, which is most of it.
func cnyesPublisher(source string) string {
	if s := strings.TrimSpace(source); s != "" {
		return s
	}
	return "cnyes"
}

// cnyesPage fetches one page of the Taiwanese market feed.
func (a *Aggregator) cnyesPage(ctx context.Context, page int) (cnyesResponse, error) {
	endpoint := fmt.Sprintf("%s/media/api/v1/newslist/category/tw_stock?limit=%d&page=%d",
		a.cnyesURL, cnyesPageSize, page)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return cnyesResponse{}, err
	}
	req.Header.Set("User-Agent", userAgent)

	res, err := a.http.Do(req)
	if err != nil {
		return cnyesResponse{}, err
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		return cnyesResponse{}, fmt.Errorf("news provider returned %s", res.Status)
	}
	var body cnyesResponse
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		return cnyesResponse{}, fmt.Errorf("decoding news response: %w", err)
	}
	return body, nil
}
