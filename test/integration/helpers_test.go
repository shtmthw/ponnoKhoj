//go:build integration

package integration

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shtmthw/ponnoKhoj/internal/app"
	"github.com/shtmthw/ponnoKhoj/internal/httpx"
	"github.com/shtmthw/ponnoKhoj/internal/search"
	"github.com/shtmthw/ponnoKhoj/internal/store"
	"github.com/shtmthw/ponnoKhoj/internal/store/db"
)

func insertProduct(ctx context.Context, st *store.Store, catID int64, nameEn, nameBn, size string) (int64, error) {
	qty := search.ParseQuery(size).Quantity
	if qty == nil {
		return 0, fmt.Errorf("unparsable size %q", size)
	}
	return st.InsertProduct(ctx, db.InsertProductParams{
		CategoryID: catID, NameEn: nameEn, NameBn: nameBn, Unit: string(qty.Unit), Quantity: qty.Value,
		SearchKey: search.ProductKey(nameEn, nameBn, "", "", qty), Status: "active",
	})
}

func insertAlias(ctx context.Context, st *store.Store, productID int64, alias string) error {
	return st.InsertProductAlias(ctx, db.InsertProductAliasParams{ProductID: productID, Alias: alias, AliasKey: search.AliasKey(alias)})
}

func itoa(n int64) string { return fmt.Sprint(n) }

// recorder is a minimal http.ResponseWriter for calling handlers directly.
type recorder struct {
	header http.Header
	body   strings.Builder
	status int
}

func newRecorder() *recorder { return &recorder{header: http.Header{}, status: http.StatusOK} }

func (r *recorder) Header() http.Header         { return r.header }
func (r *recorder) Write(b []byte) (int, error) { return r.body.Write(b) }
func (r *recorder) WriteHeader(code int)        { r.status = code }

func mustRequest(t *testing.T, path string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, path, nil)
	if err != nil {
		t.Fatal(err)
	}
	return req
}

// startUnmigrated boots the HTTP handlers on a database that has not been
// migrated (startApp cannot: it syncs online sources, which needs the schema).
func startUnmigrated(t *testing.T, dbURL string) *testApp {
	t.Helper()
	a := &testApp{ready: &httpx.Readiness{}}
	rt, err := app.NewRuntime(context.Background(), testConfig(t, dbURL, nil), io.Discard, "integration-test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { rt.Close(context.Background()) })
	a.rt = rt
	handler, _, err := rt.Handlers(a.ready)
	if err != nil {
		t.Fatal(err)
	}
	a.server = httptest.NewServer(handler)
	t.Cleanup(a.server.Close)
	return a
}

// uploadCSV posts a multipart form with one CSV file, as the bulk form does.
func uploadCSV(t *testing.T, b *browser, path, content string) (int, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("csv", "prices.csv")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(fw, content); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, b.base+path, &buf)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Origin", b.base)
	code, body, _ := b.do(req)
	return code, body
}
