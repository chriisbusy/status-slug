package provider

import (
	"context"
	"time"

	"github.com/chriisbusy/status-slug/internal/check"
	"github.com/chriisbusy/status-slug/internal/config"
)

// BuildJobs constructs probe jobs honoring the probe_mode chain:
// settings.probe_mode is the provider default, provider.probe_mode overrides
// it, and models[].probe ("chat" default, "models" = free membership check)
// governs favourite probes. only restricts to one provider when non-empty.
// resolve maps a provider to its credential; it is called inside each job.
func BuildJobs(cfg config.Config, resolve func(config.Provider) string, timeout time.Duration, only string) []check.Job {
	var jobs []check.Job
	for _, p := range cfg.Providers {
		if !p.Enabled || (only != "" && p.Name != only) {
			continue
		}
		p := p
		adapter := New(p.Kind)

		mode := p.ProbeMode
		if mode == "" {
			mode = cfg.Settings.ProbeMode
		}
		if mode == "" {
			mode = "models"
		}

		jobs = append(jobs, check.Job{
			Provider: p.Name,
			Run: func(ctx context.Context) check.Result {
				doer := check.NewDoer(timeout, resolve(p))
				if mode == "chat" {
					if mid := firstFavourite(p); mid != "" {
						return adapter.ProbeModel(ctx, doer, p, mid)
					}
					// No favourite to chat-probe with: fall back to the
					// free models probe rather than failing spuriously.
				}
				return adapter.Probe(ctx, doer, p)
			},
		})

		for _, m := range p.Models {
			if !m.Favourite {
				continue
			}
			m := m
			probe := m.Probe
			if probe == "" {
				probe = "chat"
			}
			jobs = append(jobs, check.Job{
				Provider: p.Name,
				ModelID:  m.ID,
				Run: func(ctx context.Context) check.Result {
					doer := check.NewDoer(timeout, resolve(p))
					if probe == "models" {
						return probeMembership(ctx, adapter, doer, p, m.ID)
					}
					return adapter.ProbeModel(ctx, doer, p, m.ID)
				},
			})
		}
	}
	return jobs
}

// firstFavourite returns the first favourite model ID, or "".
func firstFavourite(p config.Provider) string {
	for _, m := range p.Models {
		if m.Favourite {
			return m.ID
		}
	}
	return ""
}

// probeMembership checks a model is listed by the provider's models endpoint
// — the zero-token probe for favourites with probe = "models".
func probeMembership(ctx context.Context, a Adapter, d *check.Doer, p config.Provider, modelID string) check.Result {
	start := time.Now()
	ids, err := a.ListModels(ctx, d, p)
	latency := float64(time.Since(start).Microseconds()) / 1000.0
	if err != nil {
		// Classify the transport/HTTP failure properly.
		return check.Classify(0, nil, err, latency)
	}
	for _, id := range ids {
		if id == modelID {
			return check.Result{Status: check.OK, Reason: "listed", LatencyMs: latency, CheckedAt: time.Now()}
		}
	}
	return check.Result{
		Status:    check.Down,
		Reason:    "model not in /models listing",
		LatencyMs: latency,
		CheckedAt: time.Now(),
	}
}
