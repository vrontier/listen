// Package engine is the engine process of the Listening Observatory
// Workbench: many streams, each decoded once, each with its own analyses,
// configured over the control API (the Workbench's docs/control-api.md,
// version 1.0) on a Unix socket.
package engine

import (
	"encoding/json"
	"time"
)

// API version served.
const (
	APIMajor = 1
	APIMinor = 1 // 1.1: groups
)

// Limits are set by the engine's operator when it starts; the engine
// enforces them.
type Limits struct {
	Streams                  int `json:"streams"`
	AnalysesPerStream        int `json:"analyses_per_stream"`
	RulesPerAnalysis         int `json:"rules_per_analysis"`
	SubscriptionsPerAnalysis int `json:"subscriptions_per_analysis"`
	MaxSampleRate            int `json:"max_sample_rate"`
	MaxBitrateKbps           int `json:"max_bitrate_kbps"`
	Groups                   int `json:"groups"`
	MembersPerGroup          int `json:"members_per_group"`
}

// DefaultLimits are the limits without flags.
var DefaultLimits = Limits{
	Streams: 40, AnalysesPerStream: 8, RulesPerAnalysis: 16, SubscriptionsPerAnalysis: 4,
	MaxSampleRate: 48000, MaxBitrateKbps: 512, Groups: 20, MembersPerGroup: 8,
}

// Stream modes.
const (
	OnDemand   = "on_demand"
	Continuous = "continuous"
)

// StreamSpec is the body of PUT /v1/streams/{stream}.
type StreamSpec struct {
	Input      string    `json:"input"`
	SampleRate int       `json:"sample_rate"`
	Mode       string    `json:"mode,omitempty"`
	IdleAfterS *int      `json:"idle_after_s,omitempty"`
	RetryMaxS  *int      `json:"retry_max_s,omitempty"`
	View       *ViewSpec `json:"view,omitempty"`
}

// ViewSpec is a stream's spectrogram view: bands of its own, unfiltered and
// independent of any analysis's range.
type ViewSpec struct {
	Scale string   `json:"scale"`
	MinHz *float64 `json:"min_hz,omitempty"`
	MaxHz *float64 `json:"max_hz,omitempty"`
	Bands *int     `json:"bands,omitempty"`
}

// View is a stream's view in effect.
type View struct {
	Scale string  `json:"scale"`
	MinHz float64 `json:"min_hz"`
	MaxHz float64 `json:"max_hz"`
	Bands int     `json:"bands"`
}

// Problem is an error with a code for programs and a message for people.
type Problem struct {
	Code    string     `json:"code"`
	Message string     `json:"message"`
	At      *time.Time `json:"at,omitempty"`
}

// Usage is what something costs, averaged over the last 10 seconds.
type Usage struct {
	CPUPercent      float64 `json:"cpu_percent"`
	MemoryMB        float64 `json:"memory_mb"`
	InKbps          float64 `json:"in_kbps,omitempty"`
	InMeasured      *bool   `json:"in_measured,omitempty"`
	MemoryEstimated bool    `json:"memory_estimated,omitempty"`
	// A stream's usage is its equal share of its source's cost; the
	// source's full cost is given once beside it.
	SourceCPUPercent *float64 `json:"source_cpu_percent,omitempty"`
	SourceMemoryMB   *float64 `json:"source_memory_mb,omitempty"`
	Shared           int      `json:"shared,omitempty"`
}

// StreamView is a stream as GET /v1/streams/{stream} shows it.
type StreamView struct {
	ID         string     `json:"id"`
	Input      string     `json:"input,omitempty"`
	SampleRate int        `json:"sample_rate"`
	Mode       string     `json:"mode"`
	IdleAfterS int        `json:"idle_after_s"`
	RetryMaxS  int        `json:"retry_max_s"`
	State      string     `json:"state"`
	Since      time.Time  `json:"since"`
	LastData   *time.Time `json:"last_data"`
	LastError  *Problem   `json:"last_error"`
	Reconnects int        `json:"reconnects"`
	DelayMs    int        `json:"delay_ms"`
	Analyses   []string   `json:"analyses"`
	View       *View      `json:"view,omitempty"`
	Usage      Usage      `json:"usage"`
	Shared     int        `json:"shared"` // streams of all callers on this stream's source, this one included
	Source     string     `json:"source"` // network, file, later local
}

// AnalysisSpec is the body of PUT …/analyses/{analysis}. Every part is
// optional.
type AnalysisSpec struct {
	Settings Settings   `json:"settings"`
	Rules    []Rule     `json:"rules"`
	Memory   MemorySpec `json:"memory"`
	Label    string     `json:"label,omitempty"`
}

type Settings struct {
	MinHz         *float64 `json:"min_hz,omitempty"`
	MaxHz         *float64 `json:"max_hz,omitempty"`
	TransientK    *float64 `json:"transient_k,omitempty"`
	TransientGapS *float64 `json:"transient_gap_s,omitempty"`
}

// Rule kinds.
const (
	RuleLine = "line"
	RuleComb = "comb"
)

// Rule is a filter rule; Text is returned by the engine only (ignored in
// requests).
type Rule struct {
	Kind         string   `json:"kind"`
	Hz           float64  `json:"hz"`
	Odd          bool     `json:"odd,omitempty"`
	TolHz        *float64 `json:"tol_hz,omitempty"`
	DriftPercent *float64 `json:"drift_percent,omitempty"`
	Text         string   `json:"text,omitempty"`
}

// Memory modes.
const (
	MemoryDisk = "disk"
	MemoryRAM  = "ram"
	MemoryOff  = "off"
)

type MemorySpec struct {
	Mode         string `json:"mode,omitempty"`
	MotifReturnS *int   `json:"motif_return_s,omitempty"`
	HistoryDays  *int   `json:"history_days,omitempty"`
	Key          string `json:"key,omitempty"` // returned by the engine only
}

// Analysis states.
const (
	AnalysisIdle    = "idle"
	AnalysisRunning = "running"
	AnalysisFailed  = "failed"
)

// AnalysisView is an analysis as GET …/analyses/{analysis} shows it.
type AnalysisView struct {
	ID          string     `json:"id"`
	Stream      string     `json:"stream"`
	Settings    Settings   `json:"settings"`
	Rules       []Rule     `json:"rules"`
	Memory      MemoryView `json:"memory"`
	Label       string     `json:"label"`
	Effective   Effective  `json:"effective"`
	State       string     `json:"state"`
	LastError   *Problem   `json:"last_error,omitempty"`
	Subscribers int        `json:"subscribers"`
	Motifs      int        `json:"motifs"`
	Usage       Usage      `json:"usage"`
}

type MemoryView struct {
	Mode         string `json:"mode"`
	MotifReturnS int    `json:"motif_return_s"`
	HistoryDays  int    `json:"history_days"`
	Key          string `json:"key"`
}

// Effective is what an analysis really works with after defaults.
type Effective struct {
	SampleRate   int     `json:"sample_rate"`
	BinHz        float64 `json:"bin_hz"`
	MinHz        float64 `json:"min_hz"`
	MaxHz        float64 `json:"max_hz"`
	PeakMinHz    float64 `json:"peak_min_hz"`
	PeakMaxHz    float64 `json:"peak_max_hz"`
	DefaultTolHz float64 `json:"default_tol_hz"`
}

// FieldError points into a request, so that a form can mark the field.
type FieldError struct {
	Path    string `json:"path"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ValidateRequest is the body of POST /v1/validate/analysis.
type ValidateRequest struct {
	SampleRate int `json:"sample_rate"`
	AnalysisSpec
}

type Validation struct {
	Valid     bool         `json:"valid"`
	Errors    []FieldError `json:"errors"`
	Warnings  []FieldError `json:"warnings"`
	Effective *Effective   `json:"effective,omitempty"`
	Rules     []Rule       `json:"rules,omitempty"`
}

type ProbeRequest struct {
	Input   string `json:"input"`
	ListenS int    `json:"listen_s,omitempty"`
}

type ProbeResult struct {
	OK          bool      `json:"ok"`
	Exists      *string   `json:"exists"`
	ContentType *string   `json:"content_type"`
	Codec       string    `json:"codec"`
	SampleRate  int       `json:"sample_rate"`
	Channels    int       `json:"channels"`
	BitrateKbps int       `json:"bitrate_kbps"`
	LevelDB     float64   `json:"level_db"`
	Silent      bool      `json:"silent"`
	StationTime bool      `json:"station_time"`
	Shared      bool      `json:"shared,omitempty"` // read already, for another caller: no connection was opened
	Problems    []Problem `json:"problems"`
}

// MemoryInfo is one memory kept on disk for an analysis.
type MemoryInfo struct {
	Key       string          `json:"key"`
	Settings  json.RawMessage `json:"settings"`
	Rules     []Rule          `json:"rules"`
	FirstUsed time.Time       `json:"first_used"`
	LastUsed  time.Time       `json:"last_used"`
	Motifs    int             `json:"motifs"`
	Bytes     int64           `json:"bytes"`
	InUse     bool            `json:"in_use"`
}
