package news

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"stockbook/internal/models"
	"stockbook/internal/quotes"
)

// yahooLanguage is what this source's headlines are written in. Yahoo serves the
// same English wire for a Taiwanese ticker as for a US one, which is why this
// source is not used for Taiwanese holdings.
const yahooLanguage = "en"

// rssFeed is the slice of Yahoo's per-symbol RSS this package reads.
//
// This is a different endpoint from the JSON one the quotes package uses, and
// the reason is worth recording: the search endpoint accepts a `newsCount`
// parameter that looks like exactly the right thing and is not. Asked for news
// about 2330.TW it answered with Deutsche Telekom, Argan and Toyota — a global
// firehose, not that symbol's news. This feed is genuinely per-symbol.
type rssFeed struct {
	Channel struct {
		Items []rssItem `xml:"item"`
	} `xml:"channel"`
}

type rssItem struct {
	Title       string `xml:"title"`
	Link        string `xml:"link"`
	GUID        string `xml:"guid"`
	PubDate     string `xml:"pubDate"`
	Description string `xml:"description"`
}

// collectYahoo fetches one instrument's headlines.
//
// Attribution needs no tag here because the request itself is the tag: the feed
// was asked for by this instrument's ticker, so everything in it is what the
// provider files under that symbol. As with cnyes, that editorial call is the
// provider's — a feed for AAPL carries the occasional piece about Nvidia, and
// second-guessing which of them "really" concern Apple is exactly the title
// matching this package refuses to do.
func (a *Aggregator) collectYahoo(ctx context.Context, item models.Instrument) ([]Article, SourceResult) {
	result := SourceResult{Source: SourceYahoo, Scope: item.Symbol, Covered: []string{item.ID}}

	ticker, ok := quotes.Ticker(item.Symbol, item.Market)
	if !ok {
		result.Status = "skipped"
		result.Error = "no news source for market " + item.Market
		return nil, result
	}
	result.Scope = ticker

	feed, err := a.yahooFeed(ctx, ticker)
	if err != nil {
		result.Status = "failed"
		result.Error = err.Error()
		return nil, result
	}

	articles := make([]Article, 0, len(feed.Channel.Items))
	for _, entry := range feed.Channel.Items {
		guid := strings.TrimSpace(entry.GUID)
		link := strings.TrimSpace(entry.Link)
		published, ok := parsePubDate(entry.PubDate)
		if guid == "" || !isWebURL(link) || !ok {
			// An item missing its identity, its link, or its date cannot be
			// stored without inventing one of them. Dropping it is the same
			// choice a null price bar gets.
			continue
		}
		articles = append(articles, Article{
			ID:            SourceYahoo + ":" + guid,
			Source:        SourceYahoo,
			Title:         strings.TrimSpace(entry.Title),
			Summary:       strings.TrimSpace(entry.Description),
			URL:           link,
			Publisher:     publisherFromURL(link),
			Language:      yahooLanguage,
			PublishedAt:   published,
			InstrumentIDs: []string{item.ID},
		})
	}

	result.Status = "synced"
	result.Fetched = len(articles)
	return articles, result
}

// yahooFeed fetches and parses one symbol's RSS.
func (a *Aggregator) yahooFeed(ctx context.Context, ticker string) (rssFeed, error) {
	endpoint := fmt.Sprintf("%s/rss/2.0/headline?s=%s&region=US&lang=en-US",
		a.yahooURL, url.QueryEscape(ticker))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return rssFeed{}, err
	}
	req.Header.Set("User-Agent", userAgent)

	res, err := a.http.Do(req)
	if err != nil {
		return rssFeed{}, err
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		return rssFeed{}, fmt.Errorf("news provider returned %s for %s", res.Status, ticker)
	}
	var feed rssFeed
	if err := xml.NewDecoder(res.Body).Decode(&feed); err != nil {
		return rssFeed{}, fmt.Errorf("decoding news feed for %s: %w", ticker, err)
	}
	return feed, nil
}

// pubDateFormats are the shapes RSS dates arrive in. The feed uses a numeric
// zone in practice, but the named-zone form is equally legal RSS and costs one
// line to accept.
var pubDateFormats = []string{time.RFC1123Z, time.RFC1123}

// parsePubDate reads an RSS publication date, reporting failure rather than
// substituting now: a headline stamped with the moment it was fetched would sort
// to the top of the feed forever.
func parsePubDate(raw string) (time.Time, bool) {
	value := strings.TrimSpace(raw)
	for _, layout := range pubDateFormats {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UTC(), true
		}
	}
	return time.Time{}, false
}

// isWebURL reports whether a link is one a browser can be handed safely.
//
// This is the one place a provider's own string reaches an href, and it is the
// only field here that is not either computed by this system or rendered as
// escaped text. A title or a summary can say anything and is still just text;
// a link is an instruction. Vue does not sanitize a bound href, so a feed
// serving `javascript:...` would run it on click — the reader would have to be
// unlucky and the provider compromised, but the check costs one function and
// this whole package exists on the premise that what the providers say cannot
// be verified.
//
// Anything that is not plain http(s) is dropped rather than rewritten: there is
// no correct repair for a link that is not a link.
func isWebURL(link string) bool {
	parsed, err := url.Parse(link)
	if err != nil || parsed.Host == "" {
		return false
	}
	return parsed.Scheme == "http" || parsed.Scheme == "https"
}

// publisherFromURL names who published an article from where it is hosted.
//
// The feed carries no publisher field, and the host is the one factual answer
// available — "foxbusiness.com" rather than a guess at a masthead. Yahoo's own
// syndication host is named plainly for the same reason.
func publisherFromURL(link string) string {
	parsed, err := url.Parse(link)
	if err != nil || parsed.Host == "" {
		return ""
	}
	return strings.TrimPrefix(strings.ToLower(parsed.Host), "www.")
}
