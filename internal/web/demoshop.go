package web

import (
	_ "embed" // demoshop.json
	"encoding/json"
	"html/template"
	"net/http"
	"strings"

	"github.com/shtmthw/ponnoKhoj/internal/search"
)

// The demo shop is a fictional online shop served by ponnoKhoj itself. It
// exists so the whole online pipeline (robots.txt, polite fetching, JSON-LD
// parsing, price parsing, matching, caching) can run end to end without
// touching any third-party site. Its items and prices are invented.
//
//go:embed demoshop.json
var demoShopJSON []byte

type demoItem struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
	// Offer is emitted verbatim as the schema.org "offers" value, so the
	// fixture can exercise every price shape the parser must handle.
	Offer json.RawMessage `json:"offer"`
}

type demoShop struct {
	publicBase string
	items      []demoItem
}

func newDemoShop(publicBaseURL string) *demoShop {
	var items []demoItem
	if err := json.Unmarshal(demoShopJSON, &items); err != nil {
		panic("web: demoshop.json is invalid: " + err.Error()) // embedded at build time; covered by tests
	}
	return &demoShop{publicBase: strings.TrimRight(publicBaseURL, "/"), items: items}
}

// match returns the items sharing at least half of the query's words.
func (d *demoShop) match(query string) []demoItem {
	words := strings.Fields(search.ParseQuery(query).Key)
	if len(words) == 0 {
		return nil
	}
	var out []demoItem
	for _, it := range d.items {
		title := " " + search.ParseQuery(it.Name).Key + " "
		hits := 0
		for _, w := range words {
			if strings.Contains(title, " "+w+" ") {
				hits++
			}
		}
		if hits*2 >= len(words) {
			out = append(out, it)
		}
	}
	return out
}

var demoShopTmpl = template.Must(template.New("demoshop").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>{{.Title}} — ponnoKhoj Demo Shop (fictional)</title>
<script type="application/ld+json">{{.JSONLD}}</script></head>
<body><h1>ponnoKhoj Demo Shop</h1>
<p><strong>This shop is fictional.</strong> It is part of ponnoKhoj's demo data; nothing here is for sale and the prices are invented.</p>
<ul>{{range .Items}}<li><a href="{{$.Base}}/demo-shop/p/{{.Slug}}">{{.Name}}</a></li>{{end}}</ul>
</body></html>`))

func (d *demoShop) render(w http.ResponseWriter, title string, items []demoItem) {
	elements := make([]map[string]any, 0, len(items))
	for i, it := range items {
		elements = append(elements, map[string]any{
			"@type": "ListItem", "position": i + 1,
			"item": map[string]any{
				"@type": "Product", "name": it.Name,
				"url": d.publicBase + "/demo-shop/p/" + it.Slug, "offers": it.Offer,
			},
		})
	}
	ld, err := json.Marshal(map[string]any{"@context": "https://schema.org", "@type": "ItemList", "itemListElement": elements})
	if err != nil {
		http.Error(w, "demo shop error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	// template.JS marks the JSON as safe script content; json.Marshal has
	// already escaped <, > and & so it cannot break out of the element.
	_ = demoShopTmpl.Execute(w, map[string]any{"Title": title, "Items": items, "Base": d.publicBase, "JSONLD": template.JS(ld)}) //nolint:gosec // see comment above
}

func (d *demoShop) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	d.render(w, "Search: "+q, d.match(q))
}

func (d *demoShop) handleProduct(w http.ResponseWriter, r *http.Request) {
	for _, it := range d.items {
		if it.Slug == r.PathValue("slug") {
			d.render(w, it.Name, []demoItem{it})
			return
		}
	}
	http.NotFound(w, r)
}
