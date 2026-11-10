package moderation

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/shtmthw/ponnoKhoj/internal/auth"
	"github.com/shtmthw/ponnoKhoj/internal/domain"
	"github.com/shtmthw/ponnoKhoj/internal/store"
	"github.com/shtmthw/ponnoKhoj/internal/store/db"
)

// Errors returned by the moderation service.
var (
	ErrNotFound      = errors.New("moderation: not found")
	ErrInvalidReport = errors.New("moderation: invalid report")
	ErrForbidden     = errors.New("moderation: admin role required")
)

// Target kinds a report or review can point at.
const (
	TargetListing = "listing"
	TargetShop    = "shop"
)

// Audit writes one audit-log row inside the caller's transaction.
func Audit(ctx context.Context, q *db.Queries, actorID *int64, action, entityType string, entityID int64, detail map[string]any) error {
	if detail == nil {
		detail = map[string]any{}
	}
	raw, err := json.Marshal(detail)
	if err != nil {
		return fmt.Errorf("moderation: encode audit detail: %w", err)
	}
	if err := q.InsertAudit(ctx, db.InsertAuditParams{
		ActorID: actorID, Action: action, EntityType: entityType, EntityID: entityID, Detail: raw,
	}); err != nil {
		return fmt.Errorf("moderation: write audit log: %w", err)
	}
	return nil
}

// Service implements reports and admin review.
type Service struct {
	store     *store.Store
	threshold int
	secret    []byte
}

// NewService builds the moderation service. secret keys the reporter hash;
// threshold is how many distinct open reports auto-hide a target.
func NewService(st *store.Store, threshold int, secret string) *Service {
	return &Service{store: st, threshold: threshold, secret: []byte(secret)}
}

// ReporterHash anonymizes a reporter: HMAC-SHA256 of the client IP. It lets
// the system count distinct reporters without storing who they are.
func (s *Service) ReporterHash(clientIP string) []byte {
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(clientIP))
	return mac.Sum(nil)
}

// ReportResult says what a report did.
type ReportResult struct {
	// Duplicate is true when this reporter already has an open report on
	// the target; nothing was recorded.
	Duplicate bool
	// Hidden is true when the target is now auto-hidden pending review.
	Hidden bool
}

// Report records a shopper's flag on a listing or shop. Once the number of
// distinct open reports reaches the threshold, the target is auto-hidden
// until an admin reviews it.
func (s *Service) Report(ctx context.Context, target string, id int64, reason domain.ReportReason, note, clientIP string) (ReportResult, error) {
	note = strings.TrimSpace(note)
	if !domain.ValidReportReason(reason) || utf8.RuneCountInString(note) > 500 || id <= 0 {
		return ReportResult{}, ErrInvalidReport
	}
	params := db.CreateReportParams{Reason: string(reason), Note: note, ReporterHash: s.ReporterHash(clientIP)}
	switch target {
	case TargetListing:
		params.ListingID = &id
	case TargetShop:
		params.ShopID = &id
	default:
		return ReportResult{}, ErrInvalidReport
	}
	var res ReportResult
	err := s.store.InTx(ctx, func(q *db.Queries) error {
		// Make sure the target exists (a clean 404 instead of an FK error).
		if target == TargetListing {
			if _, err := q.GetListing(ctx, id); err != nil {
				return notFoundOr(err)
			}
		} else if _, err := q.GetShop(ctx, id); err != nil {
			return notFoundOr(err)
		}
		if _, err := q.CreateReport(ctx, params); err != nil {
			if store.IsNotFound(err) { // ON CONFLICT DO NOTHING: already reported
				res.Duplicate = true
				return nil
			}
			return fmt.Errorf("moderation: create report: %w", err)
		}
		var open int64
		var err error
		if target == TargetListing {
			open, err = q.CountOpenListingReports(ctx, &id)
		} else {
			open, err = q.CountOpenShopReports(ctx, &id)
		}
		if err != nil {
			return fmt.Errorf("moderation: count reports: %w", err)
		}
		if open < int64(s.threshold) {
			return nil
		}
		res.Hidden = true
		if target == TargetListing {
			err = q.SetListingAutoHidden(ctx, db.SetListingAutoHiddenParams{ID: id, AutoHidden: true})
		} else {
			err = q.SetShopAutoHidden(ctx, db.SetShopAutoHiddenParams{ID: id, AutoHidden: true})
		}
		if err != nil {
			return fmt.Errorf("moderation: auto-hide: %w", err)
		}
		return Audit(ctx, q, nil, "auto_hide", target, id, map[string]any{"open_reports": open, "threshold": s.threshold})
	})
	return res, err
}

func notFoundOr(err error) error {
	if store.IsNotFound(err) {
		return ErrNotFound
	}
	return fmt.Errorf("moderation: load target: %w", err)
}

func requireAdmin(u auth.User) error {
	if !u.IsAdmin() {
		return ErrForbidden
	}
	return nil
}

// SetShopStatus verifies, suspends or re-queues a shop.
func (s *Service) SetShopStatus(ctx context.Context, admin auth.User, shopID int64, status domain.ShopStatus) error {
	if err := requireAdmin(admin); err != nil {
		return err
	}
	if status != domain.ShopPending && status != domain.ShopVerified && status != domain.ShopSuspended {
		return ErrInvalidReport
	}
	return s.store.InTx(ctx, func(q *db.Queries) error {
		n, err := q.SetShopStatus(ctx, db.SetShopStatusParams{ID: shopID, Status: string(status)})
		if err != nil {
			return fmt.Errorf("moderation: set shop status: %w", err)
		}
		if n == 0 {
			return ErrNotFound
		}
		return Audit(ctx, q, &admin.ID, "shop_"+string(status), TargetShop, shopID, nil)
	})
}

// ResolveReports closes every open report on a target. Upholding keeps a
// listing hidden or suspends a shop; dismissing makes the target visible again.
func (s *Service) ResolveReports(ctx context.Context, admin auth.User, target string, id int64, uphold bool) error {
	if err := requireAdmin(admin); err != nil {
		return err
	}
	status := "dismissed"
	if uphold {
		status = "upheld"
	}
	return s.store.InTx(ctx, func(q *db.Queries) error {
		var (
			n   int64
			err error
		)
		switch target {
		case TargetListing:
			if n, err = q.ResolveReportsForListing(ctx, db.ResolveReportsForListingParams{Status: status, ResolvedBy: &admin.ID, ListingID: &id}); err == nil && n > 0 {
				err = q.SetListingAutoHidden(ctx, db.SetListingAutoHiddenParams{ID: id, AutoHidden: uphold})
			}
		case TargetShop:
			if n, err = q.ResolveReportsForShop(ctx, db.ResolveReportsForShopParams{Status: status, ResolvedBy: &admin.ID, ShopID: &id}); err == nil && n > 0 {
				if uphold {
					_, err = q.SetShopStatus(ctx, db.SetShopStatusParams{ID: id, Status: string(domain.ShopSuspended)})
				}
				if err == nil {
					err = q.SetShopAutoHidden(ctx, db.SetShopAutoHiddenParams{ID: id, AutoHidden: false})
				}
			}
		default:
			return ErrInvalidReport
		}
		if err != nil {
			return fmt.Errorf("moderation: resolve reports: %w", err)
		}
		if n == 0 {
			return ErrNotFound
		}
		return Audit(ctx, q, &admin.ID, "reports_"+status, target, id, map[string]any{"reports": n})
	})
}

// ReviewAnomaly clears a listing's price-anomaly flag. With hide=true the
// listing is also hidden (the price was wrong); otherwise it becomes normal
// estimation evidence again.
func (s *Service) ReviewAnomaly(ctx context.Context, admin auth.User, listingID int64, hide bool) error {
	if err := requireAdmin(admin); err != nil {
		return err
	}
	return s.store.InTx(ctx, func(q *db.Queries) error {
		n, err := q.ReviewListingAnomaly(ctx, db.ReviewListingAnomalyParams{ID: listingID, Hide: hide})
		if err != nil {
			return fmt.Errorf("moderation: review anomaly: %w", err)
		}
		if n == 0 {
			return ErrNotFound
		}
		action := "anomaly_approved"
		if hide {
			action = "anomaly_rejected"
		}
		return Audit(ctx, q, &admin.ID, action, TargetListing, listingID, nil)
	})
}

// ReviewProduct approves or rejects a vendor-proposed product.
func (s *Service) ReviewProduct(ctx context.Context, admin auth.User, productID int64, approve bool) error {
	if err := requireAdmin(admin); err != nil {
		return err
	}
	status := "rejected"
	if approve {
		status = "active"
	}
	return s.store.InTx(ctx, func(q *db.Queries) error {
		n, err := q.SetProductStatus(ctx, db.SetProductStatusParams{ID: productID, Status: status})
		if err != nil {
			return fmt.Errorf("moderation: review product: %w", err)
		}
		if n == 0 {
			return ErrNotFound
		}
		return Audit(ctx, q, &admin.ID, "product_"+status, "product", productID, nil)
	})
}
