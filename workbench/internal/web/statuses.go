package web

import "strings"

// The words the pages use for states (proposed 2026-10-16): one word per
// meaning on every page, always with its colour. The stored values, and
// what MCP and the data stream return, stay as they are.

type status struct {
	Word, Tone string // tone: ok, info, warn, bad or neutral
}

var statusWords = map[string]map[string]status{
	// A source as registered.
	"source": {
		"draft":      {"Draft", "neutral"},
		"production": {"In service", "ok"},
		"paused":     {"Paused", "neutral"},
		"suspended":  {"Suspended", "warn"},
	},
	// A source's connection at the engine.
	"conn": {
		"unknown":      {"Unknown", "neutral"},
		"idle":         {"Not connected", "neutral"},
		"connecting":   {"Connecting", "info"},
		"connected":    {"Live", "ok"},
		"buffering":    {"Waiting for audio", "info"},
		"reconnecting": {"Reconnecting", "warn"},
		"refused":      {"Not reachable", "bad"},
	},
	// Whether an analysis is shown to others.
	"visibility": {
		"private":   {"Private", "neutral"},
		"published": {"Published", "ok"},
		"withdrawn": {"Withdrawn", "neutral"},
	},
	// What an analysis does now.
	"activity": {
		"run":     {"Running", "info"},
		"preview": {"Preview", "info"},
		"idle":    {"Idle", "neutral"},
		"failed":  {"Failed", "bad"},
	},
	// A run's data set.
	"dataset": {
		"recording": {"Recording", "info"},
		"freezing":  {"Finishing", "info"},
		"ready":     {"Ready", "ok"},
		"failed":    {"Failed", "bad"},
		"deleted":   {"Deleted", "neutral"},
		"none":      {"No data set", "neutral"},
	},
	// An access with a token.
	"access": {
		"ok":      {"OK", "ok"},
		"error":   {"Failed", "bad"},
		"refused": {"Refused", "warn"},
		"open":    {"Streaming", "info"},
		"cut":     {"Cut off", "bad"},
	},
}

// statusOf gives the word and tone for a state; an unknown state shows as
// it is, neutral.
func statusOf(kind, value string) status {
	if st, ok := statusWords[kind][value]; ok {
		return st
	}
	if value == "" {
		return status{"Unknown", "neutral"}
	}
	return status{strings.ToUpper(value[:1]) + value[1:], "neutral"}
}

// connWords are the connection words for the live views in the browser.
func connWords() map[string]status { return statusWords["conn"] }
