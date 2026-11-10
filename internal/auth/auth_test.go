package auth

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHashAndVerifyPassword(t *testing.T) {
	t.Parallel()
	hash, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "$argon2id$v=19$m=19456,t=2,p=1$") {
		t.Errorf("hash %q does not carry the documented parameters", hash)
	}
	if ok, err := VerifyPassword("correct horse battery", hash); err != nil || !ok {
		t.Errorf("correct password rejected: %v %v", ok, err)
	}
	if ok, err := VerifyPassword("wrong horse battery", hash); err != nil || ok {
		t.Errorf("wrong password accepted: %v %v", ok, err)
	}
	other, _ := HashPassword("correct horse battery")
	if other == hash {
		t.Error("two hashes of the same password are identical (salt not random)")
	}
}

func TestVerifyPasswordMalformed(t *testing.T) {
	t.Parallel()
	good, _ := HashPassword("correct horse battery")
	parts := strings.Split(good, "$")
	bad := []string{
		"", "plaintext", "$argon2i$v=19$m=19456,t=2,p=1$c2FsdHNhbHQ$aGFzaGhhc2hoYXNoaGFzaA",
		"$argon2id$v=18$m=19456,t=2,p=1$" + parts[4] + "$" + parts[5],
		"$argon2id$v=19$m=x,t=2,p=1$" + parts[4] + "$" + parts[5],
		"$argon2id$v=19$m=4294967295,t=2,p=1$" + parts[4] + "$" + parts[5],
		"$argon2id$v=19$m=19456,t=99,p=1$" + parts[4] + "$" + parts[5],
		"$argon2id$v=19$m=19456,t=2,p=1$!!!$" + parts[5],
		"$argon2id$v=19$m=19456,t=2,p=1$" + parts[4] + "$!!!",
		"$argon2id$v=19$m=19456,t=2,p=1$" + parts[4] + "$YQ",
	}
	for _, h := range bad {
		if ok, err := VerifyPassword("x", h); err == nil || ok {
			t.Errorf("VerifyPassword accepted malformed hash %q", h)
		}
	}
}

func TestCheckPasswordPolicy(t *testing.T) {
	t.Parallel()
	if err := CheckPasswordPolicy("short"); !errors.Is(err, ErrPasswordTooShort) {
		t.Errorf("short: %v", err)
	}
	if err := CheckPasswordPolicy("দশটিঅক্ষরেরপাস"); err != nil {
		t.Errorf("ten-rune Bangla password rejected: %v", err)
	}
	if err := CheckPasswordPolicy(strings.Repeat("a", MaxPasswordBytes+1)); !errors.Is(err, ErrPasswordTooLong) {
		t.Errorf("long: %v", err)
	}
	if err := CheckPasswordPolicy(strings.Repeat("a", MaxPasswordBytes)); err != nil {
		t.Errorf("max length rejected: %v", err)
	}
}

func TestLockoutFor(t *testing.T) {
	t.Parallel()
	tests := map[int]time.Duration{
		0: 0, 4: 0, 5: time.Minute, 6: 2 * time.Minute, 7: 4 * time.Minute,
		10: 32 * time.Minute, 11: time.Hour, 50: time.Hour,
	}
	for n, want := range tests {
		if got := LockoutFor(n); got != want {
			t.Errorf("LockoutFor(%d) = %v, want %v", n, got, want)
		}
	}
}

func TestNormalizeEmail(t *testing.T) {
	t.Parallel()
	if got, err := NormalizeEmail("  Vendor@Example.COM "); err != nil || got != "vendor@example.com" {
		t.Errorf("NormalizeEmail = %q, %v", got, err)
	}
	for _, bad := range []string{"", "nope", "a@b", "Name <a@b.com>", "a@b.com, c@d.com", "@b.com", strings.Repeat("a", 250) + "@b.com"} {
		if _, err := NormalizeEmail(bad); !errors.Is(err, ErrInvalidEmail) {
			t.Errorf("NormalizeEmail(%q) err = %v", bad, err)
		}
	}
}

func TestTokens(t *testing.T) {
	t.Parallel()
	token, hash, err := newToken()
	if err != nil {
		t.Fatal(err)
	}
	if len(hash) != 32 {
		t.Fatalf("hash length %d", len(hash))
	}
	got, ok := hashToken(token)
	if !ok || string(got) != string(hash) {
		t.Error("hashToken does not match newToken")
	}
	for _, bad := range []string{"", "short", "not base64 at all!!", token + "A"} {
		if _, ok := hashToken(bad); ok {
			t.Errorf("hashToken accepted %q", bad)
		}
	}
	token2, _, _ := newToken()
	if token2 == token {
		t.Error("tokens repeat")
	}
}

func TestCookies(t *testing.T) {
	t.Parallel()
	for _, secure := range []bool{true, false} {
		s := &Service{cfg: Config{SessionTTL: time.Hour, CookieSecure: secure}}
		rec := httptest.NewRecorder()
		s.SetSessionCookie(rec, "tok")
		c := rec.Result().Cookies()[0]
		wantName := "pk_session"
		if secure {
			wantName = "__Host-pk_session"
		}
		if c.Name != wantName || !c.HttpOnly || c.Secure != secure || c.SameSite != http.SameSiteLaxMode || c.Path != "/" || c.MaxAge != 3600 {
			t.Errorf("secure=%v: cookie = %+v", secure, c)
		}
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		if s.TokenFromRequest(req) != "" {
			t.Error("token without cookie")
		}
		req.AddCookie(c)
		if s.TokenFromRequest(req) != "tok" {
			t.Error("token not read back")
		}
		rec = httptest.NewRecorder()
		s.ClearSessionCookie(rec)
		if got := rec.Result().Cookies()[0]; got.MaxAge >= 0 || got.Name != wantName {
			t.Errorf("clear cookie = %+v", got)
		}
	}
	if !(User{Role: "admin"}).IsAdmin() || (User{Role: "vendor"}).IsAdmin() {
		t.Error("IsAdmin wrong")
	}
}
