// Package web serves the server-rendered HTML UI: Go html/template pages
// enhanced with HTMX, bilingual (Bangla and English) message catalogs, and
// embedded static assets with content-hashed filenames.
package web

import (
	"embed"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/shtmthw/ponnoKhoj/internal/domain"
)

//go:embed catalogs/*.json
var catalogFS embed.FS

// Languages the UI is available in. Bangla is the default: the target market
// is Bangladesh.
const (
	LangBn = "bn"
	LangEn = "en"
)

// Catalog holds the translated messages of every language.
type Catalog struct {
	messages map[string]map[string]string
}

// LoadCatalog reads the embedded message catalogs.
func LoadCatalog() (*Catalog, error) {
	c := &Catalog{messages: map[string]map[string]string{}}
	for _, lang := range []string{LangBn, LangEn} {
		raw, err := catalogFS.ReadFile("catalogs/" + lang + ".json")
		if err != nil {
			return nil, fmt.Errorf("web: read %s catalog: %w", lang, err)
		}
		var m map[string]string
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, fmt.Errorf("web: parse %s catalog: %w", lang, err)
		}
		c.messages[lang] = m
	}
	return c, nil
}

// Keys returns the message keys of one language.
func (c *Catalog) Keys(lang string) []string {
	keys := make([]string, 0, len(c.messages[lang]))
	for k := range c.messages[lang] {
		keys = append(keys, k)
	}
	return keys
}

// Localizer renders messages, numbers, money, distances and ages in one
// language. Templates receive it as .L.
type Localizer struct {
	Lang    string
	catalog *Catalog
	now     func() time.Time
}

// For returns the localizer of a language (Bangla for anything unknown).
func (c *Catalog) For(lang string) *Localizer {
	if lang != LangEn {
		lang = LangBn
	}
	return &Localizer{Lang: lang, catalog: c, now: time.Now}
}

// Bangla reports whether the language is Bangla.
func (l *Localizer) Bangla() bool { return l.Lang == LangBn }

// Other returns the language the toggle switches to.
func (l *Localizer) Other() string {
	if l.Bangla() {
		return LangEn
	}
	return LangBn
}

// T translates a message key. Arguments fill fmt verbs in the message. A
// missing key renders as the key itself, which tests treat as a failure.
func (l *Localizer) T(key string, args ...any) string {
	msg, ok := l.catalog.messages[l.Lang][key]
	if !ok {
		return key
	}
	if len(args) == 0 {
		return msg
	}
	return fmt.Sprintf(msg, args...)
}

// Num renders an integer with the language's digits.
func (l *Localizer) Num(n any) string {
	s := fmt.Sprint(n)
	if l.Bangla() {
		return domain.ToBanglaDigits(s)
	}
	return s
}

// Money renders an amount as ৳ with the language's digits.
func (l *Localizer) Money(m domain.Money) string { return m.Format(l.Bangla()) }

// Qty renders a quantity ("5 kg" / "৫ কেজি").
func (l *Localizer) Qty(q domain.Quantity) string { return q.Format(l.Bangla()) }

// Name picks the product, shop or area name for the language.
func (l *Localizer) Name(en, bn string) string {
	if l.Bangla() && bn != "" {
		return bn
	}
	if en != "" {
		return en
	}
	return bn
}

// Dist renders a distance in meters as "850 m" or "2.4 km".
func (l *Localizer) Dist(meters float64) string {
	var s string
	if meters < 1000 {
		s = strconv.Itoa(int(math.Round(meters/10)*10)) + " " + l.T("unit.m")
	} else {
		s = strconv.FormatFloat(math.Round(meters/100)/10, 'f', 1, 64) + " " + l.T("unit.km")
	}
	if l.Bangla() {
		return domain.ToBanglaDigits(s)
	}
	return s
}

// Ago renders how long ago t was, coarsely ("today", "3 days ago").
func (l *Localizer) Ago(t time.Time) string {
	d := l.now().Sub(t)
	switch {
	case d < time.Hour:
		return l.T("ago.just_now")
	case d < 24*time.Hour:
		return l.T("ago.hours", l.Num(int(d.Hours())))
	default:
		return l.T("ago.days", l.Num(int(d.Hours()/24)))
	}
}

// Date renders a date as YYYY-MM-DD with the language's digits, in
// Bangladesh time.
func (l *Localizer) Date(t time.Time) string {
	return l.Num(t.In(dhaka).Format("2006-01-02 15:04"))
}

var dhaka = func() *time.Location {
	loc, err := time.LoadLocation("Asia/Dhaka")
	if err != nil {
		return time.FixedZone("BST", 6*3600) // tzdata is embedded; this is a belt-and-braces fallback
	}
	return loc
}()

// langCookie remembers the language choice. It holds no personal data.
const langCookie = "pk_lang"

// languageOf picks the request's language: explicit cookie, then the
// browser's Accept-Language, then Bangla.
func languageOf(r *http.Request) string {
	if c, err := r.Cookie(langCookie); err == nil && (c.Value == LangBn || c.Value == LangEn) {
		return c.Value
	}
	for part := range strings.SplitSeq(r.Header.Get("Accept-Language"), ",") {
		tag := strings.ToLower(strings.TrimSpace(strings.SplitN(part, ";", 2)[0]))
		switch {
		case tag == "bn" || strings.HasPrefix(tag, "bn-"):
			return LangBn
		case tag == "en" || strings.HasPrefix(tag, "en-"):
			return LangEn
		}
	}
	return LangBn
}
