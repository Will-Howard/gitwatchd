package main

import (
	"fmt"
	"math"
	"time"
)

// Pure string formatting for `gitwatchd status`. Same rows and strings as the
// macOS menu, so the habit transfers between machines unchanged.

// One status tail at most: paused wins over errors, errors over pending.
func rowTitle(name, branch string, paused bool, pending int, errorLabel string) string {
	base := name + " · " + branch
	if paused {
		return base + " · ⏸ paused"
	}
	if errorLabel != "" {
		return base + " · ⚠ " + errorLabel
	}
	if pending == 1 {
		return base + " · 1 pending change"
	}
	if pending > 1 {
		return fmt.Sprintf("%s · %d pending changes", base, pending)
	}
	return base
}

// Fixed-width row; the full path lives on the detail line.
func configErrorRow(label, reason string) string {
	return truncated("⚠ "+label+" · "+reason, 48)
}

func errorHeadline(label string, attempts int) string {
	if attempts > 1 {
		return fmt.Sprintf("⚠ %s (%d attempts)", label, attempts)
	}
	return "⚠ " + label
}

func retryLine(lastTried time.Time, nextRetry *time.Time, now time.Time) string {
	tried := "tried " + ago(now.Sub(lastTried).Seconds())
	if nextRetry == nil {
		return tried + " · retries on next change"
	}
	dt := nextRetry.Sub(now).Seconds()
	if dt <= 1 {
		return tried + " · retrying now"
	}
	return tried + " · retrying in " + span(dt)
}

func truncated(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max-1]) + "…"
}

// "just now", "40s ago", "5m ago", "3h ago", "2d ago".
func ago(seconds float64) string {
	if seconds < 5 {
		return "just now"
	}
	return span(seconds) + " ago"
}

func span(seconds float64) string {
	s := int(math.Round(seconds))
	if s < 1 {
		s = 1
	}
	if s < 90 {
		return fmt.Sprintf("%ds", s)
	}
	if s < 90*60 {
		return fmt.Sprintf("%dm", int(math.Round(float64(s)/60)))
	}
	if s < 36*3600 {
		return fmt.Sprintf("%dh", int(math.Round(float64(s)/3600)))
	}
	return fmt.Sprintf("%dd", int(math.Round(float64(s)/86400)))
}

// Push retry backoff: 30s doubling to a 5 minute cap.
const (
	backoffFirst = 30 * time.Second
	backoffCap   = 300 * time.Second
)

func backoffDelay(afterFailures int) time.Duration {
	if afterFailures <= 1 {
		return backoffFirst
	}
	d := time.Duration(float64(backoffFirst) * math.Pow(2, float64(afterFailures-1)))
	if d > backoffCap {
		return backoffCap
	}
	return d
}
