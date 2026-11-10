package sources

import "context"

// SourceRow is what the registry writes for each known source.
type SourceRow struct {
	Descriptor
	BaseURL string
	Enabled bool
}

// Rows returns the registry's state for persisting to online_sources, which
// the UI and the offers query read.
func (r *Registry) Rows() []SourceRow {
	out := make([]SourceRow, 0, len(r.descriptors))
	for _, d := range r.descriptors {
		base := r.BaseURL(d.ID)
		if base == "" {
			base = d.BaseURL
		}
		out = append(out, SourceRow{Descriptor: d, BaseURL: base, Enabled: r.IsEnabled(d.ID)})
	}
	return out
}

// Syncer persists source rows; implemented by the store in cmd.
type Syncer func(ctx context.Context, row SourceRow) error

// Sync writes every source's descriptor and feature-flag state.
func (r *Registry) Sync(ctx context.Context, save Syncer) error {
	for _, row := range r.Rows() {
		if err := save(ctx, row); err != nil {
			return err
		}
	}
	return nil
}
