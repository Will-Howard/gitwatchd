package main

import (
	"fmt"
	"math"
)

// Pure formatting helpers, with no gitwatchd vocabulary of their own.

func truncated(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max-1]) + "…"
}

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
