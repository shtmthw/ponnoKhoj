package vendor

import (
	"encoding/csv"
	"errors"
	"io"
	"strconv"
	"strings"

	"golang.org/x/text/unicode/norm"

	"github.com/shtmthw/ponnoKhoj/internal/domain"
	"github.com/shtmthw/ponnoKhoj/internal/sources"
)

// CSVRow is one validated row of a bulk listing upload.
type CSVRow struct {
	Line      int // 1-based line number in the file, for error messages
	ProductID int64
	Price     domain.Money
	InStock   bool
}

// RowError is a per-row validation problem. Code is a stable identifier the
// UI translates; Detail is the offending value.
type RowError struct {
	Line   int
	Code   string
	Detail string
}

// Row error codes.
const (
	RowBadColumns  = "bad_columns"
	RowBadProduct  = "bad_product_id"
	RowBadPrice    = "bad_price"
	RowBadStock    = "bad_in_stock"
	RowDuplicate   = "duplicate_product"
	RowUnknownProd = "unknown_product"
	RowNotAllowed  = "product_not_allowed"
)

// Fatal CSV errors: the whole file is rejected.
var (
	ErrCSVEmpty      = errors.New("vendor: the CSV file is empty")
	ErrCSVHeader     = errors.New("vendor: the first line must be the header product_id,price[,in_stock]")
	ErrCSVTooManyRow = errors.New("vendor: the CSV file has too many rows")
	ErrCSVMalformed  = errors.New("vendor: the file is not valid CSV")
)

// ParseListingCSV parses a bulk upload with the header
// "product_id,price[,in_stock]". Prices accept the same human formats as
// online prices ("1250", "1,250.50", "৳ ১,২৫০"). It returns the valid rows,
// the per-row errors, and a fatal error when the file as a whole is unusable.
// The caller is responsible for capping the reader's size.
func ParseListingCSV(r io.Reader, maxRows int) ([]CSVRow, []RowError, error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1 // validated per row so one short row does not kill the file
	cr.TrimLeadingSpace = true
	cr.ReuseRecord = true

	header, err := cr.Read()
	if errors.Is(err, io.EOF) {
		return nil, nil, ErrCSVEmpty
	}
	if err != nil {
		return nil, nil, ErrCSVMalformed
	}
	if len(header) < 2 || len(header) > 3 ||
		cell(header[0]) != "product_id" || cell(header[1]) != "price" ||
		(len(header) == 3 && cell(header[2]) != "in_stock") {
		return nil, nil, ErrCSVHeader
	}

	var (
		rows []CSVRow
		errs []RowError
		seen = map[int64]bool{}
		n    int
	)
	for {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return rows, errs, ErrCSVMalformed
		}
		// FieldPos is only valid after a successful Read.
		line, _ := cr.FieldPos(0)
		if len(rec) == 1 && strings.TrimSpace(rec[0]) == "" {
			continue // blank line
		}
		n++
		if n > maxRows {
			return nil, nil, ErrCSVTooManyRow
		}
		if len(rec) < 2 || len(rec) > 3 {
			errs = append(errs, RowError{Line: line, Code: RowBadColumns, Detail: strconv.Itoa(len(rec))})
			continue
		}
		id, err := strconv.ParseInt(domain.ToASCIIDigits(strings.TrimSpace(rec[0])), 10, 64)
		if err != nil || id <= 0 {
			errs = append(errs, RowError{Line: line, Code: RowBadProduct, Detail: clip(rec[0])})
			continue
		}
		price, err := sources.ParsePrice(rec[1])
		if err != nil {
			errs = append(errs, RowError{Line: line, Code: RowBadPrice, Detail: clip(rec[1])})
			continue
		}
		inStock := true
		if len(rec) == 3 && strings.TrimSpace(rec[2]) != "" {
			v, ok := parseBool(rec[2])
			if !ok {
				errs = append(errs, RowError{Line: line, Code: RowBadStock, Detail: clip(rec[2])})
				continue
			}
			inStock = v
		}
		if seen[id] {
			errs = append(errs, RowError{Line: line, Code: RowDuplicate, Detail: strconv.FormatInt(id, 10)})
			continue
		}
		seen[id] = true
		rows = append(rows, CSVRow{Line: line, ProductID: id, Price: price, InStock: inStock})
	}
	return rows, errs, nil
}

// cell normalizes a header cell: BOM stripped, trimmed, lower-cased.
func cell(s string) string {
	return strings.ToLower(strings.TrimSpace(strings.TrimPrefix(s, "\ufeff")))
}

func parseBool(s string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(norm.NFKC.String(s))) {
	case "true", "yes", "y", "1", "হ্যাঁ", "আছে":
		return true, true
	case "false", "no", "n", "0", "না", "নেই":
		return false, true
	}
	return false, false
}

// clip bounds how much of a bad value is echoed back.
func clip(s string) string {
	s = strings.ToValidUTF8(strings.TrimSpace(s), "?")
	if rs := []rune(s); len(rs) > 40 {
		return string(rs[:40]) + "…"
	}
	return s
}
