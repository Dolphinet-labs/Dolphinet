package engine

import "time"

const (
	buildSealTimeout      = time.Second * 10
	buildStartTimeout     = time.Second * 10
	buildCancelTimeout    = time.Second * 10
	payloadProcessTimeout = time.Second * 10
)

// effectiveOpTimeout returns configured when set (>0), otherwise fallback (legacy per-operation default).
func effectiveOpTimeout(configured, fallback time.Duration) time.Duration {
	if configured > 0 {
		return configured
	}
	return fallback
}
