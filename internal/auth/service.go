package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"github.com/shtmthw/ponnoKhoj/internal/domain"
	"github.com/shtmthw/ponnoKhoj/internal/store"
	"github.com/shtmthw/ponnoKhoj/internal/store/db"
)

// User is an authenticated account.
type User struct {
	ID          int64
	Email       string
	DisplayName string
	Role        domain.Role
}

// IsAdmin reports whether the user has the admin role.
func (u User) IsAdmin() bool { return u.Role == domain.RoleAdmin }

// Service errors. Login deliberately returns one error for "no such user"
// and "wrong password" so accounts cannot be enumerated.
var (
	ErrInvalidEmail       = errors.New("auth: invalid email address")
	ErrInvalidName        = errors.New("auth: name must be 1 to 100 characters")
	ErrEmailTaken         = errors.New("auth: an account with this email already exists")
	ErrInvalidCredentials = errors.New("auth: wrong email or password")
	ErrLocked             = errors.New("auth: too many failed attempts; try again later")
	ErrNoSession          = errors.New("auth: not signed in")
)

// Lockout policy: after LockoutThreshold consecutive failures the account is
// locked for a backoff that doubles with each further failure, capped at
// LockoutMax.
const (
	LockoutThreshold = 5
	LockoutBase      = time.Minute
	LockoutMax       = time.Hour
)

// LockoutFor returns how long an account is locked after its n-th
// consecutive failed login (0 below the threshold).
func LockoutFor(failures int) time.Duration {
	if failures < LockoutThreshold {
		return 0
	}
	d := LockoutBase
	for i := LockoutThreshold; i < failures && d < LockoutMax; i++ {
		d *= 2
	}
	return min(d, LockoutMax)
}

// NormalizeEmail lower-cases and validates an email address.
func NormalizeEmail(email string) (string, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if len(email) > 254 {
		return "", ErrInvalidEmail
	}
	addr, err := mail.ParseAddress(email)
	if err != nil || addr.Address != email || !strings.Contains(email[strings.LastIndex(email, "@")+1:], ".") {
		return "", ErrInvalidEmail
	}
	return email, nil
}

// Config configures sessions.
type Config struct {
	SessionTTL   time.Duration
	CookieSecure bool
}

// Service implements sign-up, login, session lookup and logout.
type Service struct {
	store *store.Store
	cfg   Config
	now   func() time.Time
	// dummyHash is verified when the email is unknown so that the response
	// time does not reveal whether an account exists.
	dummyHash string
}

// NewService builds the auth service.
func NewService(st *store.Store, cfg Config) (*Service, error) {
	dummy, err := HashPassword("ponnokhoj-dummy-password")
	if err != nil {
		return nil, err
	}
	return &Service{store: st, cfg: cfg, now: time.Now, dummyHash: dummy}, nil
}

// SignUp creates a vendor account.
func (s *Service) SignUp(ctx context.Context, email, password, displayName string) (User, error) {
	return s.createUser(ctx, email, password, displayName, domain.RoleVendor, false)
}

// CreateUser creates an account with an explicit role (seeding, CLI).
func (s *Service) CreateUser(ctx context.Context, email, password, displayName string, role domain.Role, isDemo bool) (User, error) {
	return s.createUser(ctx, email, password, displayName, role, isDemo)
}

func (s *Service) createUser(ctx context.Context, email, password, displayName string, role domain.Role, isDemo bool) (User, error) {
	email, err := NormalizeEmail(email)
	if err != nil {
		return User{}, err
	}
	displayName = strings.TrimSpace(displayName)
	if displayName == "" || len([]rune(displayName)) > 100 {
		return User{}, ErrInvalidName
	}
	if err := CheckPasswordPolicy(password); err != nil {
		return User{}, err
	}
	hash, err := HashPassword(password)
	if err != nil {
		return User{}, err
	}
	id, err := s.store.CreateUser(ctx, db.CreateUserParams{
		Email: email, PasswordHash: hash, DisplayName: displayName, Role: string(role), IsDemo: isDemo,
	})
	if store.IsNotFound(err) { // ON CONFLICT DO NOTHING returned no row
		return User{}, ErrEmailTaken
	}
	if err != nil {
		return User{}, fmt.Errorf("auth: create user: %w", err)
	}
	return User{ID: id, Email: email, DisplayName: displayName, Role: role}, nil
}

// Login verifies credentials and opens a session, returning its token.
func (s *Service) Login(ctx context.Context, email, password string) (string, User, error) {
	email, err := NormalizeEmail(email)
	if err != nil || len(password) > MaxPasswordBytes {
		_, _ = VerifyPassword(password[:min(len(password), MaxPasswordBytes)], s.dummyHash)
		return "", User{}, ErrInvalidCredentials
	}
	row, err := s.store.GetUserByEmail(ctx, email)
	if store.IsNotFound(err) {
		_, _ = VerifyPassword(password, s.dummyHash)
		return "", User{}, ErrInvalidCredentials
	}
	if err != nil {
		return "", User{}, fmt.Errorf("auth: load user: %w", err)
	}
	now := s.now()
	if row.LockedUntil != nil && row.LockedUntil.After(now) {
		return "", User{}, ErrLocked
	}
	ok, err := VerifyPassword(password, row.PasswordHash)
	if err != nil {
		return "", User{}, fmt.Errorf("auth: verify: %w", err)
	}
	if !ok {
		var lockedUntil *time.Time
		if d := LockoutFor(int(row.FailedLogins) + 1); d > 0 {
			t := now.Add(d)
			lockedUntil = &t
		}
		if err := s.store.RecordLoginFailure(ctx, db.RecordLoginFailureParams{ID: row.ID, LockedUntil: lockedUntil}); err != nil {
			return "", User{}, fmt.Errorf("auth: record failure: %w", err)
		}
		return "", User{}, ErrInvalidCredentials
	}
	if row.FailedLogins > 0 || row.LockedUntil != nil {
		if err := s.store.ResetLoginFailures(ctx, row.ID); err != nil {
			return "", User{}, fmt.Errorf("auth: reset failures: %w", err)
		}
	}
	token, hash, err := newToken()
	if err != nil {
		return "", User{}, err
	}
	if err := s.store.CreateSession(ctx, db.CreateSessionParams{
		UserID: row.ID, TokenHash: hash, ExpiresAt: now.Add(s.cfg.SessionTTL),
	}); err != nil {
		return "", User{}, fmt.Errorf("auth: create session: %w", err)
	}
	return token, User{ID: row.ID, Email: row.Email, DisplayName: row.DisplayName, Role: domain.Role(row.Role)}, nil
}

// Authenticate resolves a session token to its user.
func (s *Service) Authenticate(ctx context.Context, token string) (User, error) {
	hash, ok := hashToken(token)
	if !ok {
		return User{}, ErrNoSession
	}
	row, err := s.store.GetSessionUser(ctx, hash)
	if store.IsNotFound(err) {
		return User{}, ErrNoSession
	}
	if err != nil {
		return User{}, fmt.Errorf("auth: load session: %w", err)
	}
	return User{ID: row.ID, Email: row.Email, DisplayName: row.DisplayName, Role: domain.Role(row.Role)}, nil
}

// Logout deletes the session for token, if any.
func (s *Service) Logout(ctx context.Context, token string) error {
	hash, ok := hashToken(token)
	if !ok {
		return nil
	}
	if err := s.store.DeleteSession(ctx, hash); err != nil {
		return fmt.Errorf("auth: delete session: %w", err)
	}
	return nil
}

// Session tokens are 32 random bytes, base64url-encoded for the cookie. Only
// the SHA-256 of the token is stored, so a database leak does not leak
// usable sessions.
const tokenBytes = 32

func newToken() (token string, hash []byte, err error) {
	raw := make([]byte, tokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, fmt.Errorf("auth: token: %w", err)
	}
	sum := sha256.Sum256(raw)
	return base64.RawURLEncoding.EncodeToString(raw), sum[:], nil
}

func hashToken(token string) ([]byte, bool) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(raw) != tokenBytes {
		return nil, false
	}
	sum := sha256.Sum256(raw)
	return sum[:], true
}

// CookieName returns the session cookie name. With Secure cookies the
// "__Host-" prefix pins the cookie to this host over HTTPS.
func (s *Service) CookieName() string {
	if s.cfg.CookieSecure {
		return "__Host-pk_session"
	}
	return "pk_session"
}

// SetSessionCookie writes the session cookie: HttpOnly, SameSite=Lax, and
// Secure unless explicitly disabled for plain-HTTP local development.
func (s *Service) SetSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // G124: Secure follows COOKIE_SECURE, which config.Load forces on in prod
		Name: s.CookieName(), Value: token, Path: "/",
		MaxAge:   int(s.cfg.SessionTTL.Seconds()),
		HttpOnly: true, Secure: s.cfg.CookieSecure, SameSite: http.SameSiteLaxMode,
	})
}

// ClearSessionCookie expires the session cookie.
func (s *Service) ClearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // G124: Secure follows COOKIE_SECURE, which config.Load forces on in prod
		Name: s.CookieName(), Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: s.cfg.CookieSecure, SameSite: http.SameSiteLaxMode,
	})
}

// TokenFromRequest reads the session token from the cookie ("" when absent).
func (s *Service) TokenFromRequest(r *http.Request) string {
	c, err := r.Cookie(s.CookieName())
	if err != nil {
		return ""
	}
	return c.Value
}
