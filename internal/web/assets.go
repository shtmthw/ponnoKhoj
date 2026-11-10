package web

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
)

// The only JavaScript in the repository lives under static/: the vendored,
// pinned HTMX and Leaflet builds (static/vendor, checksums in
// docs/architecture.md) and one small hand-written glue file, app.js, that
// starts Leaflet maps (see ADR 0008).
//
//go:embed static
var staticFS embed.FS

// Assets serves embedded static files. Each file is reachable under a
// content-hashed name ("app.3f2a9c1b.css"), served with a one-year immutable
// cache header, and under its plain name with a short cache (needed for the
// images Leaflet's stylesheet references relatively).
type Assets struct {
	files  fs.FS
	hashed map[string]string // logical path -> hashed path
	plain  map[string]string // hashed path -> logical path
}

// NewAssets hashes every embedded static file.
func NewAssets() (*Assets, error) {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		return nil, fmt.Errorf("web: static fs: %w", err)
	}
	a := &Assets{files: sub, hashed: map[string]string{}, plain: map[string]string{}}
	err = fs.WalkDir(sub, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := fs.ReadFile(sub, p)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		ext := path.Ext(p)
		hashedPath := strings.TrimSuffix(p, ext) + "." + hex.EncodeToString(sum[:6]) + ext
		a.hashed[p] = hashedPath
		a.plain[hashedPath] = p
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("web: hash static files: %w", err)
	}
	return a, nil
}

// Path returns the public, content-hashed URL of a static file. An unknown
// name panics: it is a programming error caught by the template tests.
func (a *Assets) Path(name string) string {
	h, ok := a.hashed[name]
	if !ok {
		panic("web: unknown static asset " + name)
	}
	return "/static/" + h
}

// ServeHTTP serves /static/<path>.
func (a *Assets) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/static/")
	cache := "public, max-age=3600"
	if logical, ok := a.plain[name]; ok {
		name = logical
		cache = "public, max-age=31536000, immutable"
	} else if _, ok := a.hashed[name]; !ok {
		http.NotFound(w, r)
		return
	}
	data, err := fs.ReadFile(a.files, name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	ctype := mime.TypeByExtension(path.Ext(name))
	if ctype == "" {
		ctype = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Cache-Control", cache)
	_, _ = w.Write(data) //nolint:gosec // G705: bytes of an embedded static file looked up by exact name, served with nosniff
}
