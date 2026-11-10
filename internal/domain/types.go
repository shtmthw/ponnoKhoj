package domain

import (
	"time"
)

// Mode is a price source the shopper can filter by.
type Mode string

// Search modes, matching the README's filtering-mode switch.
const (
	ModeOnline   Mode = "online"
	ModeLocal    Mode = "local"
	ModeEstimate Mode = "estimate"
	ModeAll      Mode = "all"
)

// ParseMode validates a mode string; the empty string means ModeAll.
func ParseMode(s string) (Mode, bool) {
	switch Mode(s) {
	case "":
		return ModeAll, true
	case ModeOnline, ModeLocal, ModeEstimate, ModeAll:
		return Mode(s), true
	}
	return "", false
}

// ShopStatus is a shop's verification state.
type ShopStatus string

// Shop verification states.
const (
	ShopPending   ShopStatus = "pending"
	ShopVerified  ShopStatus = "verified"
	ShopSuspended ShopStatus = "suspended"
)

// Role is a user account role. Admin is a role, not a separate system.
type Role string

// Account roles.
const (
	RoleVendor Role = "vendor"
	RoleAdmin  Role = "admin"
)

// Freshness is the computed age state of a listing.
type Freshness string

// Freshness states.
const (
	Fresh  Freshness = "fresh"
	Stale  Freshness = "stale"
	Hidden Freshness = "hidden"
)

// FreshnessPolicy holds the configurable listing-age thresholds.
type FreshnessPolicy struct {
	FreshFor time.Duration // listings updated within this are fresh
	StaleFor time.Duration // listings older than FreshFor but within this are stale; older are hidden
}

// DefaultFreshnessPolicy is fresh ≤ 7 days, stale ≤ 30 days, hidden after.
func DefaultFreshnessPolicy() FreshnessPolicy {
	return FreshnessPolicy{FreshFor: 7 * 24 * time.Hour, StaleFor: 30 * 24 * time.Hour}
}

// Of computes the freshness of something last updated at updatedAt, as of now.
// A timestamp in the future (clock skew) counts as fresh.
func (p FreshnessPolicy) Of(updatedAt, now time.Time) Freshness {
	age := now.Sub(updatedAt)
	switch {
	case age <= p.FreshFor:
		return Fresh
	case age <= p.StaleFor:
		return Stale
	default:
		return Hidden
	}
}

// Confidence is the qualitative confidence of an estimate.
type Confidence string

// Confidence levels.
const (
	ConfidenceLow    Confidence = "low"
	ConfidenceMedium Confidence = "medium"
	ConfidenceHigh   Confidence = "high"
)

// ValidConfidence reports whether c is a known level.
func ValidConfidence(c Confidence) bool {
	return c == ConfidenceLow || c == ConfidenceMedium || c == ConfidenceHigh
}

// EstimateMethod records how an estimate was produced.
type EstimateMethod string

// Estimation methods.
const (
	MethodStatistical EstimateMethod = "statistical"
	MethodLLMAssisted EstimateMethod = "llm_assisted"
)

// EstimateStatus is the lifecycle state of an estimate request.
type EstimateStatus string

// Estimate states.
const (
	EstimatePending      EstimateStatus = "pending"
	EstimateReady        EstimateStatus = "ready"
	EstimateInsufficient EstimateStatus = "insufficient_data"
	EstimateFailed       EstimateStatus = "failed"
)

// ReportReason is why a shopper flagged a listing or shop.
type ReportReason string

// Report reasons.
const (
	ReportWrongPrice ReportReason = "wrong_price"
	ReportClosed     ReportReason = "closed"
	ReportFake       ReportReason = "fake"
)

// ValidReportReason reports whether r is a known reason.
func ValidReportReason(r ReportReason) bool {
	return r == ReportWrongPrice || r == ReportClosed || r == ReportFake
}
