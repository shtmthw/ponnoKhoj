package sources

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/shtmthw/ponnoKhoj/internal/search"
)

// Source is one online shop integration. It is one of two kinds:
//   - a Searcher answers free-text queries (the self-hosted demo shop);
//   - a PageReader reads the known product pages of catalog products, for
//     real shops whose robots.txt forbids automated use of their search.
type Source interface {
	ID() string
}

// Searcher is a source that can search the shop by text.
type Searcher interface {
	Source
	// Search returns the offers the shop shows for a query, plus how many
	// products on the page were skipped as unusable.
	Search(ctx context.Context, q search.Query) (ParseResult, error)
}

// PageReader is a source that reads individual product pages.
type PageReader interface {
	Source
	// Owns reports whether a product page URL belongs to this shop.
	Owns(rawURL string) bool
	// ReadPage fetches one product page and returns its offers.
	ReadPage(ctx context.Context, rawURL string) (ParseResult, error)
}

// Descriptor is the registry entry for a source. Every source also has a
// compliance entry in docs/sources.md, recording the robots.txt and terms
// review behind its Compliance value.
type Descriptor struct {
	ID           string
	Name         string
	BaseURL      string // the shop's origin; empty for the demo shop (configured)
	AccessMethod string // official_api | affiliate_feed | structured_data | html | demo
	Compliance   string // approved | pending_review | forbidden | demo
}

// CanEnable reports whether the compliance decision allows turning the
// source on. Unclear or forbidden sources can never be enabled by a flag.
func (d Descriptor) CanEnable() bool {
	return d.Compliance == "approved" || d.Compliance == "demo"
}

// Source IDs.
const (
	DemoShopID = "demoshop"
	StarTechID = "startech"
	TechlandID = "techland"
)

// realShopInterval spaces requests to a real shop: one every five seconds,
// well under what a person browsing generates. Neither approved shop's
// robots.txt sets a Crawl-delay.
const realShopInterval = 5 * time.Second

// Catalog lists every source ponnoKhoj knows, with its compliance decision
// (reviewed 2026-11-10, details in docs/sources.md). All sources are
// disabled unless named in SOURCES_ENABLED, and only approved (or demo)
// sources can be named there.
var Catalog = []Descriptor{
	{ID: DemoShopID, Name: "ponnoKhoj Demo Shop (fictional)", AccessMethod: "demo", Compliance: "demo"},
	// robots.txt allows product pages and disallows /product/search; the
	// terms (warranty/sales rules) say nothing about automated access.
	// Read through product pages only, via schema.org microdata.
	{ID: StarTechID, Name: "Star Tech", BaseURL: "https://www.startech.com.bd", AccessMethod: "structured_data", Compliance: "approved"},
	// robots.txt allows product pages and disallows every URL with a query
	// string; the terms cover pricing, orders and warranty only. Read
	// through product pages only, via schema.org JSON-LD.
	{ID: TechlandID, Name: "Tech Land BD", BaseURL: "https://www.techlandbd.com", AccessMethod: "structured_data", Compliance: "approved"},
	// The terms forbid "data mining, robots, scrapers ... data gathering and
	// extraction tools" and systematic retrieval into a database without
	// written permission.
	{ID: "ryans", Name: "Ryans Computers", BaseURL: "https://www.ryans.com", AccessMethod: "html", Compliance: "forbidden"},
	// The terms page answered 503 during review and robots.txt asks for a
	// 20-second crawl delay; needs a completed review and owner approval.
	{ID: "ultratech", Name: "Ultra Technology", BaseURL: "https://www.ultratech.com.bd", AccessMethod: "structured_data", Compliance: "pending_review"},
}

// ProductPageSource reads a real shop's product pages (robots.txt checked
// on every fetch by the Fetcher) and extracts the schema.org offer.
type ProductPageSource struct {
	id      string
	origin  *url.URL
	fetcher *Fetcher
}

// NewProductPageSource builds a page reader for the shop at baseURL.
func NewProductPageSource(id, baseURL string, fetcher *Fetcher) (*ProductPageSource, error) {
	origin, err := url.Parse(baseURL)
	if err != nil || origin.Scheme != "https" || origin.Host == "" {
		return nil, fmt.Errorf("sources: %s: base URL must be https", id)
	}
	return &ProductPageSource{id: id, origin: origin, fetcher: fetcher}, nil
}

// ID implements Source.
func (s *ProductPageSource) ID() string { return s.id }

// Owns implements PageReader.
func (s *ProductPageSource) Owns(rawURL string) bool {
	u, err := url.Parse(rawURL)
	return err == nil && u.Scheme == s.origin.Scheme && strings.EqualFold(u.Host, s.origin.Host)
}

// ReadPage implements PageReader.
func (s *ProductPageSource) ReadPage(ctx context.Context, rawURL string) (ParseResult, error) {
	if !s.Owns(rawURL) {
		return ParseResult{}, fmt.Errorf("source %s: %w: %s is not on %s", s.id, ErrBadURL, rawURL, s.origin.Host)
	}
	page, err := url.Parse(rawURL)
	if err != nil {
		return ParseResult{}, ErrBadURL
	}
	body, err := s.fetcher.Get(ctx, rawURL)
	if err != nil {
		return ParseResult{}, fmt.Errorf("source %s: %w", s.id, err)
	}
	res, err := ParseProductPage(body, page)
	if err != nil {
		return ParseResult{}, fmt.Errorf("source %s: parse: %w", s.id, err)
	}
	return res, nil
}

// StructuredDataSource searches a shop through its on-site search page and
// reads the schema.org JSON-LD that page embeds.
type StructuredDataSource struct {
	id      string
	baseURL string
	// searchPath is appended to baseURL; "{query}" is replaced by the
	// URL-escaped query text.
	searchPath string
	fetcher    *Fetcher
}

// NewStructuredDataSource builds a JSON-LD source.
func NewStructuredDataSource(id, baseURL, searchPath string, fetcher *Fetcher) *StructuredDataSource {
	return &StructuredDataSource{id: id, baseURL: strings.TrimRight(baseURL, "/"), searchPath: searchPath, fetcher: fetcher}
}

// ID implements Source.
func (s *StructuredDataSource) ID() string { return s.id }

// Search implements Source.
func (s *StructuredDataSource) Search(ctx context.Context, q search.Query) (ParseResult, error) {
	text := q.Raw
	if text == "" {
		text = q.Text
	}
	target := s.baseURL + strings.ReplaceAll(s.searchPath, "{query}", url.QueryEscape(text))
	base, err := url.Parse(target)
	if err != nil {
		return ParseResult{}, ErrBadURL
	}
	body, err := s.fetcher.Get(ctx, target)
	if err != nil {
		return ParseResult{}, fmt.Errorf("source %s: %w", s.id, err)
	}
	res, err := ParseStructuredData(body, base)
	if err != nil {
		return ParseResult{}, fmt.Errorf("source %s: parse: %w", s.id, err)
	}
	return res, nil
}

// RegistryConfig carries what the registry needs from configuration.
type RegistryConfig struct {
	Enabled         func(id string) bool
	ContactURL      string
	DemoShopBaseURL string
}

// Registry holds the descriptors and the instantiated, enabled sources.
type Registry struct {
	descriptors []Descriptor
	enabled     map[string]Source
	baseURLs    map[string]string
}

// NewRegistry instantiates every source that is both flagged on and allowed
// by its compliance decision. Flags naming unknown or non-enableable sources
// are an error, so a typo cannot silently disable a source or, worse, be
// mistaken for an approval.
func NewRegistry(cfg RegistryConfig, flagged []string) (*Registry, error) {
	r := &Registry{descriptors: Catalog, enabled: map[string]Source{}, baseURLs: map[string]string{}}
	known := map[string]Descriptor{}
	for _, d := range Catalog {
		known[d.ID] = d
	}
	for _, id := range flagged {
		d, ok := known[id]
		if !ok {
			return nil, fmt.Errorf("sources: SOURCES_ENABLED names unknown source %q", id)
		}
		if !d.CanEnable() {
			return nil, fmt.Errorf("sources: source %q cannot be enabled: compliance status is %q (see docs/sources.md)", id, d.Compliance)
		}
	}
	for _, d := range Catalog {
		if !cfg.Enabled(d.ID) || !d.CanEnable() {
			continue
		}
		switch d.ID {
		case DemoShopID:
			fetcher := NewFetcher(FetcherConfig{ContactURL: cfg.ContactURL, MinInterval: 200 * time.Millisecond}) // our own demo shop
			r.enabled[d.ID] = NewStructuredDataSource(d.ID, cfg.DemoShopBaseURL, "/search?q={query}", fetcher)
			r.baseURLs[d.ID] = cfg.DemoShopBaseURL
		default:
			// Each shop gets its own fetcher: no shared cookies, limits or
			// breaker state across shops.
			fetcher := NewFetcher(FetcherConfig{ContactURL: cfg.ContactURL, MinInterval: realShopInterval})
			src, err := NewProductPageSource(d.ID, d.BaseURL, fetcher)
			if err != nil {
				return nil, err
			}
			r.enabled[d.ID] = src
			r.baseURLs[d.ID] = d.BaseURL
		}
	}
	return r, nil
}

// Descriptors lists all known sources.
func (r *Registry) Descriptors() []Descriptor { return r.descriptors }

// IsEnabled reports whether a source is instantiated.
func (r *Registry) IsEnabled(id string) bool { _, ok := r.enabled[id]; return ok }

// BaseURL returns the configured base URL of an enabled source ("" otherwise).
func (r *Registry) BaseURL(id string) string { return r.baseURLs[id] }

// SearchersEnabled counts the enabled sources that can search by text.
func (r *Registry) SearchersEnabled() int {
	n := 0
	for _, s := range r.enabled {
		if _, ok := s.(Searcher); ok {
			n++
		}
	}
	return n
}

// Enabled returns the enabled sources in catalog order.
func (r *Registry) Enabled() []Source {
	out := make([]Source, 0, len(r.enabled))
	for _, d := range r.descriptors {
		if s, ok := r.enabled[d.ID]; ok {
			out = append(out, s)
		}
	}
	return out
}
