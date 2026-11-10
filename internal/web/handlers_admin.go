package web

import (
	"errors"
	"net/http"

	"github.com/shtmthw/ponnoKhoj/internal/domain"
	"github.com/shtmthw/ponnoKhoj/internal/moderation"
	"github.com/shtmthw/ponnoKhoj/internal/store/db"
)

// adminAuditLimit is how many of the latest audit entries the admin page shows.
const adminAuditLimit = 20

// Admin view models.
type (
	// AdminReportVM is one open report.
	AdminReportVM struct {
		Target    string
		TargetID  int64
		ShopID    int64
		ShopName  string
		ProductEn string
		Price     domain.Money
		Reason    string
		Note      string
	}
	// AdminProposalVM is one proposed product.
	AdminProposalVM struct {
		db.ListProposedProductsRow
		Quantity domain.Quantity
	}
	// AdminFlaggedVM is one anomaly-flagged listing.
	AdminFlaggedVM struct {
		ID        int64
		ProductID int64
		NameEn    string
		NameBn    string
		ShopID    int64
		ShopName  string
		Price     domain.Money
	}
	// AdminVM is the admin page's view model.
	AdminVM struct {
		PendingShops []db.ListShopsByStatusRow
		Reports      []AdminReportVM
		Proposals    []AdminProposalVM
		Flagged      []AdminFlaggedVM
		Sources      []db.ListSourcesRow
		Audit        []db.ListAuditRow
	}
)

func (s *Server) handleAdmin(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var (
		vm  AdminVM
		err error
	)
	if vm.PendingShops, err = s.d.Store.ListShopsByStatus(ctx, db.ListShopsByStatusParams{Status: string(domain.ShopPending), MaxResults: 100}); err != nil {
		s.serverError(w, r, err)
		return
	}
	reports, err := s.d.Store.ListOpenReports(ctx, 100)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	for _, rep := range reports {
		row := AdminReportVM{ShopID: rep.TargetShopID, ShopName: rep.TargetShopName, ProductEn: rep.ProductEn, Price: domain.Money(rep.PricePoisha), Reason: rep.Reason, Note: rep.Note}
		if rep.ListingID != nil {
			row.Target, row.TargetID = moderation.TargetListing, *rep.ListingID
		} else if rep.ShopID != nil {
			row.Target, row.TargetID = moderation.TargetShop, *rep.ShopID
		}
		vm.Reports = append(vm.Reports, row)
	}
	proposals, err := s.d.Store.ListProposedProducts(ctx, 100)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	for _, p := range proposals {
		vm.Proposals = append(vm.Proposals, AdminProposalVM{ListProposedProductsRow: p, Quantity: domain.Quantity{Value: p.Quantity, Unit: domain.Unit(p.Unit)}})
	}
	flagged, err := s.d.Store.ListFlaggedListings(ctx, 100)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	for _, f := range flagged {
		vm.Flagged = append(vm.Flagged, AdminFlaggedVM{ID: f.ID, ProductID: f.ProductID, NameEn: f.NameEn, NameBn: f.NameBn, ShopID: f.ShopID, ShopName: f.ShopName, Price: domain.Money(f.PricePoisha)})
	}
	if vm.Sources, err = s.d.Store.ListSources(ctx); err != nil {
		s.serverError(w, r, err)
		return
	}
	if vm.Audit, err = s.d.Store.ListAudit(ctx, adminAuditLimit); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "admin.html", s.page(r, s.localizer(r).T("admin.title"), vm))
}

// adminDone finishes an admin action: back to the queue, or an error page.
func (s *Server) adminDone(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, moderation.ErrNotFound):
		redirectFlash(w, r, "/admin", "flash.err.gone")
	case errors.Is(err, moderation.ErrInvalidReport):
		s.renderError(w, r, http.StatusBadRequest, "error.bad_request")
	case errors.Is(err, moderation.ErrForbidden):
		s.renderError(w, r, http.StatusForbidden, "error.forbidden")
	case err != nil:
		s.serverError(w, r, err)
	default:
		redirectFlash(w, r, "/admin", "flash.admin_done")
	}
}

func (s *Server) handleAdminShopStatus(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok || !parseForm(w, r) {
		s.renderError(w, r, http.StatusBadRequest, "error.bad_request")
		return
	}
	s.adminDone(w, r, s.d.Moderation.SetShopStatus(r.Context(), *s.currentUser(r), id, domain.ShopStatus(r.PostForm.Get("status"))))
}

func (s *Server) handleAdminResolve(w http.ResponseWriter, r *http.Request) {
	if !parseForm(w, r) {
		s.renderError(w, r, http.StatusBadRequest, "error.bad_request")
		return
	}
	s.adminDone(w, r, s.d.Moderation.ResolveReports(r.Context(), *s.currentUser(r), r.PostForm.Get("target"), formID(r, "id"), r.PostForm.Get("uphold") == "1"))
}

func (s *Server) handleAdminAnomaly(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok || !parseForm(w, r) {
		s.renderError(w, r, http.StatusBadRequest, "error.bad_request")
		return
	}
	s.adminDone(w, r, s.d.Moderation.ReviewAnomaly(r.Context(), *s.currentUser(r), id, r.PostForm.Get("hide") == "1"))
}

func (s *Server) handleAdminProduct(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok || !parseForm(w, r) {
		s.renderError(w, r, http.StatusBadRequest, "error.bad_request")
		return
	}
	s.adminDone(w, r, s.d.Moderation.ReviewProduct(r.Context(), *s.currentUser(r), id, r.PostForm.Get("approve") == "1"))
}
