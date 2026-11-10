package vendor

import (
	"errors"
	"strings"
	"testing"

	"github.com/shtmthw/ponnoKhoj/internal/geo"
)

func TestParseListingCSV(t *testing.T) {
	t.Parallel()
	in := "\ufeffProduct_ID, Price ,in_stock\n" +
		"1,405,yes\n" +
		"2,\"1,250.50\",no\n" +
		"৩,৳ ১২০,হ্যাঁ\n" +
		"\n" +
		"4,85\n" +
		"5,90,\n" +
		"abc,100,yes\n" +
		"6,free,yes\n" +
		"7,100,perhaps\n" +
		"1,410,yes\n" +
		"8\n" +
		"9,100,yes,extra\n" +
		"0,100,yes\n" +
		"10,৳ 1,200 - ৳ 1,500,yes\n"
	rows, errs, err := ParseListingCSV(strings.NewReader(in), 100)
	if err != nil {
		t.Fatal(err)
	}
	wantRows := []CSVRow{
		{Line: 2, ProductID: 1, Price: 40500, InStock: true},
		{Line: 3, ProductID: 2, Price: 125050, InStock: false},
		{Line: 4, ProductID: 3, Price: 12000, InStock: true},
		{Line: 6, ProductID: 4, Price: 8500, InStock: true},
		{Line: 7, ProductID: 5, Price: 9000, InStock: true},
	}
	if len(rows) != len(wantRows) {
		t.Fatalf("rows = %+v", rows)
	}
	for i, w := range wantRows {
		if rows[i] != w {
			t.Errorf("row %d = %+v, want %+v", i, rows[i], w)
		}
	}
	wantErrs := []RowError{
		{Line: 8, Code: RowBadProduct, Detail: "abc"},
		{Line: 9, Code: RowBadPrice, Detail: "free"},
		{Line: 10, Code: RowBadStock, Detail: "perhaps"},
		{Line: 11, Code: RowDuplicate, Detail: "1"},
		{Line: 12, Code: RowBadColumns, Detail: "1"},
		{Line: 13, Code: RowBadColumns, Detail: "4"},
		{Line: 14, Code: RowBadProduct, Detail: "0"},
		{Line: 15, Code: RowBadColumns, Detail: "5"}, // an unquoted price with commas splits into extra columns
	}
	if len(errs) != len(wantErrs) {
		t.Fatalf("errs = %+v", errs)
	}
	for i, w := range wantErrs {
		if errs[i] != w {
			t.Errorf("error %d = %+v, want %+v", i, errs[i], w)
		}
	}
}

func TestParseListingCSVFatal(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, in string
		maxRows  int
		want     error
	}{
		{"empty", "", 10, ErrCSVEmpty},
		{"wrong header", "id,cost\n1,2\n", 10, ErrCSVHeader},
		{"header with extra column", "product_id,price,in_stock,notes\n", 10, ErrCSVHeader},
		{"data without header", "1,405,yes\n", 10, ErrCSVHeader},
		{"too many rows", "product_id,price\n1,10\n2,10\n3,10\n", 2, ErrCSVTooManyRow},
		{"malformed quoting", "product_id,price\n1,\"10\n", 10, ErrCSVMalformed},
		{"malformed header", "\"product_id,price\n", 10, ErrCSVMalformed},
	}
	for _, tc := range tests {
		if _, _, err := ParseListingCSV(strings.NewReader(tc.in), tc.maxRows); !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", tc.name, err, tc.want)
		}
	}
	// Exactly at the row limit is fine; a header-only file has no rows.
	if rows, _, err := ParseListingCSV(strings.NewReader("product_id,price\n1,10\n2,10\n"), 2); err != nil || len(rows) != 2 {
		t.Errorf("at the limit: %v, %v", rows, err)
	}
	if rows, errs, err := ParseListingCSV(strings.NewReader("product_id,price\n"), 2); err != nil || len(rows) != 0 || len(errs) != 0 {
		t.Errorf("header only: %v, %v, %v", rows, errs, err)
	}
	// Bad values are clipped before being echoed back.
	_, errs, _ := ParseListingCSV(strings.NewReader("product_id,price\n"+strings.Repeat("x", 500)+",10\n"), 5)
	if len(errs) != 1 || len([]rune(errs[0].Detail)) > 41 {
		t.Errorf("unclipped detail: %+v", errs)
	}
}

func FuzzParseListingCSV(f *testing.F) {
	for _, s := range []string{"product_id,price,in_stock\n1,405,yes\n", "product_id,price\n১,৳ ১২০\n", "", "\ufeffproduct_id,price\n", "product_id,price\n\"", "a,b\n1,2", "product_id,price\n1,1e9\n-1,5\n9223372036854775808,5\n", "product_id,price\r\n1,5\r\n\r\n"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		rows, errs, err := ParseListingCSV(strings.NewReader(in), 50)
		if len(rows) > 50 {
			t.Fatalf("%d rows exceed the limit", len(rows))
		}
		if err != nil && errors.Is(err, ErrCSVTooManyRow) && rows != nil {
			t.Fatal("rows returned together with the too-many-rows error")
		}
		seen := map[int64]bool{}
		for _, r := range rows {
			if r.ProductID <= 0 || !r.Price.Valid() || r.Line < 2 {
				t.Fatalf("invalid row accepted: %+v", r)
			}
			if seen[r.ProductID] {
				t.Fatalf("duplicate product %d accepted", r.ProductID)
			}
			seen[r.ProductID] = true
		}
		for _, e := range errs {
			if e.Code == "" || e.Line < 1 {
				t.Fatalf("bad row error: %+v", e)
			}
		}
	})
}

func TestShopInputValidate(t *testing.T) {
	t.Parallel()
	valid := func() ShopInput {
		return ShopInput{Name: "  Rahim   Store ", Description: "  Groceries and   household items.\r\n\r\n\r\nNear the station. ", Address: "12 Station Road", AreaID: 5, Location: geo.Point{Lat: 22.33, Lng: 91.83}, Phone: "+880 1700-000000", OpeningHours: "9–9"}
	}
	in := valid()
	if err := in.Validate(); err != nil {
		t.Fatalf("valid input rejected: %v", err)
	}
	if in.Name != "Rahim Store" {
		t.Errorf("name not normalized: %q", in.Name)
	}
	if in.Description != "Groceries and household items.\n\nNear the station." {
		t.Errorf("description not normalized: %q", in.Description)
	}
	tests := []struct {
		name   string
		mutate func(*ShopInput)
		field  string
	}{
		{"empty name", func(s *ShopInput) { s.Name = "   " }, "name"},
		{"long name", func(s *ShopInput) { s.Name = strings.Repeat("ন", 121) }, "name"},
		{"short description", func(s *ShopInput) { s.Description = "tiny" }, "description"},
		{"long description", func(s *ShopInput) { s.Description = strings.Repeat("দ", 1001) }, "description"},
		{"empty address", func(s *ShopInput) { s.Address = "" }, "address"},
		{"long address", func(s *ShopInput) { s.Address = strings.Repeat("a", 301) }, "address"},
		{"no area", func(s *ShopInput) { s.AreaID = 0 }, "area"},
		{"no pin", func(s *ShopInput) { s.Location = geo.Point{} }, "location"},
		{"pin out of range", func(s *ShopInput) { s.Location = geo.Point{Lat: 95, Lng: 91} }, "location"},
		{"phone with letters", func(s *ShopInput) { s.Phone = "call me" }, "phone"},
		{"long phone", func(s *ShopInput) { s.Phone = strings.Repeat("1", 31) }, "phone"},
		{"long hours", func(s *ShopInput) { s.OpeningHours = strings.Repeat("x", 201) }, "opening_hours"},
	}
	for _, tc := range tests {
		in := valid()
		tc.mutate(&in)
		err := in.Validate()
		var ve *ValidationError
		if !errors.As(err, &ve) || ve.Field != tc.field {
			t.Errorf("%s: err = %v, want a validation error on %s", tc.name, err, tc.field)
		}
	}
	// Multi-line input cannot smuggle line breaks into a one-line field.
	in = valid()
	in.Address = "line one\nline two"
	if err := in.Validate(); err != nil || strings.Contains(in.Address, "\n") {
		t.Errorf("address = %q, %v", in.Address, err)
	}
	if got := (&ValidationError{Field: "price", Code: "range"}).Error(); got != "vendor: invalid price (range)" {
		t.Errorf("error text = %q", got)
	}
}
