// Package engine is the Workbench's side of the control API
// (docs/control-api.md): types and an HTTP client over a Unix socket.
package engine

import (
	"encoding/json"
	"fmt"
	"time"
)

// APIMajor and APIMinor are the control API version this Workbench is
// built for. The engine must report the same major and at least this minor.
const (
	APIMajor = 1
	APIMinor = 0
)

type APIVersion struct {
	Major int `json:"major"`
	Minor int `json:"minor"`
}

func (v APIVersion) String() string { return fmt.Sprintf("%d.%d", v.Major, v.Minor) }

// Fits reports whether an engine speaking v can be used.
func (v APIVersion) Fits() bool { return v.Major == APIMajor && v.Minor >= APIMinor }

type Info struct {
	API           APIVersion   `json:"api"`
	EngineVersion string       `json:"engine_version"`
	Instance      string       `json:"instance"`
	Started       time.Time    `json:"started"`
	Limits        Limits       `json:"limits"`
	LimitsScope   string       `json:"limits_scope,omitempty"` // "engine": the limits hold for all callers together
	Capabilities  Capabilities `json:"capabilities"`
	Counts        Counts       `json:"counts"`
	Host          Host         `json:"host"`
	Usage         Usage        `json:"usage"`
}

type Limits struct {
	Streams                  int `json:"streams"`
	AnalysesPerStream        int `json:"analyses_per_stream"`
	RulesPerAnalysis         int `json:"rules_per_analysis"`
	SubscriptionsPerAnalysis int `json:"subscriptions_per_analysis"`
	MaxSampleRate            int `json:"max_sample_rate"`
	MaxBitrateKbps           int `json:"max_bitrate_kbps"`
	Groups                   int `json:"groups"`            // 1.1
	MembersPerGroup          int `json:"members_per_group"` // 1.1
}

type Capabilities struct {
	RuleKinds    []string `json:"rule_kinds"`
	Detectors    []any    `json:"detectors"`
	FileInputs   bool     `json:"file_inputs"`
	AddressCheck string   `json:"address_check"`
	// 1.1
	StationTime   bool               `json:"station_time"`
	GroupAnalyses []GroupAnalysisCap `json:"group_analyses"`
}

// GroupAnalysisCap is one group analysis the engine offers, with the
// timing it needs: "seconds" works on wall time, "milliseconds" needs
// station time.
type GroupAnalysisCap struct {
	ID     string `json:"id"`
	Timing string `json:"timing"`
}

// Groups reports whether the engine has groups (control API 1.1).
func (i Info) Groups() bool { return i.API.Minor >= 1 && len(i.Capabilities.GroupAnalyses) > 0 }

type Counts struct {
	Streams   int `json:"streams"`
	Connected int `json:"connected"`
	Analyses  int `json:"analyses"`
}

type Host struct {
	Cores    int `json:"cores"`
	MemoryMB int `json:"memory_mb"`
}

// Usage is what something costs right now (control-api.md, Usage).
type Usage struct {
	CPUPercent      float64 `json:"cpu_percent"`
	MemoryMB        float64 `json:"memory_mb"`
	InKbps          float64 `json:"in_kbps,omitempty"`
	InMeasured      bool    `json:"in_measured,omitempty"`
	MemoryEstimated bool    `json:"memory_estimated,omitempty"`
	// A stream's usage is its equal share of its source's cost when other
	// streams, of any caller, share the source; the source's full cost is
	// given once beside it (decided 2026-10-16).
	SourceCPUPercent float64 `json:"source_cpu_percent,omitempty"`
	SourceMemoryMB   float64 `json:"source_memory_mb,omitempty"`
	Shared           int     `json:"shared,omitempty"`
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
	IdleAfterS int       `json:"idle_after_s,omitempty"`
	RetryMaxS  int       `json:"retry_max_s,omitempty"`
	View       *ViewSpec `json:"view,omitempty"`
}

// View scales.
const (
	ScaleLinear = "linear"
	ScaleLog    = "log"
)

// ViewSpec is a stream's spectrogram view (decided 2026-10-16): bands of
// its own, unfiltered and independent of any analysis's range, computed
// once per stream. Unset fields take the engine's defaults.
type ViewSpec struct {
	Scale string   `json:"scale"`
	MinHz *float64 `json:"min_hz,omitempty"`
	MaxHz *float64 `json:"max_hz,omitempty"`
	Bands *int     `json:"bands,omitempty"`
}

// View is a stream's view as in effect.
type View struct {
	Scale string  `json:"scale"`
	MinHz float64 `json:"min_hz"`
	MaxHz float64 `json:"max_hz"`
	Bands int     `json:"bands"`
}

type Stream struct {
	ID         string     `json:"id"`
	Input      string     `json:"input"`
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
	// Shared counts the streams, of all callers, on this stream's source,
	// this one included; Source says where it is read: "network", "file"
	// (development) or "local" (an Observatory listener's tap).
	Shared int    `json:"shared,omitempty"`
	Source string `json:"source,omitempty"`
	Usage  Usage  `json:"usage"`
}

// CodeIngestUnavailable is a stream's last_error while the engine cannot
// reach listen-ingest, the platform's only service that connects to
// stations; the stream is then reconnecting, not refused.
const CodeIngestUnavailable = "ingest_unavailable"

// Problem is an error or a probe problem with a code for programs and a
// message for people.
type Problem struct {
	Code    string     `json:"code"`
	Message string     `json:"message"`
	At      *time.Time `json:"at,omitempty"`
}

// AnalysisSpec is the body of PUT …/analyses/{analysis}. Every part is
// optional; the Workbench stores it as the analysis's revision.
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

type Rule struct {
	Kind         string  `json:"kind"`
	Hz           float64 `json:"hz"`
	Odd          bool    `json:"odd,omitempty"`
	TolHz        float64 `json:"tol_hz,omitempty"`
	DriftPercent float64 `json:"drift_percent,omitempty"`
	Text         string  `json:"text,omitempty"` // returned by the engine only
}

// Memory modes.
const (
	MemoryDisk = "disk"
	MemoryRAM  = "ram"
	MemoryOff  = "off"
)

type MemorySpec struct {
	Mode         string `json:"mode,omitempty"`
	MotifReturnS int    `json:"motif_return_s,omitempty"`
	HistoryDays  int    `json:"history_days,omitempty"`
	Key          string `json:"key,omitempty"` // returned by the engine only
}

// Analysis states. An analysis fails on its own (decided 2026-10-16): its
// stream and the other analyses go on, and a PUT of the same analysis
// starts it again.
const (
	AnalysisIdle    = "idle"
	AnalysisRunning = "running"
	AnalysisFailed  = "failed"
)

type Analysis struct {
	ID          string     `json:"id"`
	Stream      string     `json:"stream"`
	Settings    Settings   `json:"settings"`
	Rules       []Rule     `json:"rules"`
	Memory      MemorySpec `json:"memory"`
	Label       string     `json:"label"`
	Effective   Effective  `json:"effective"`
	State       string     `json:"state"`
	LastError   *Problem   `json:"last_error,omitempty"`
	Subscribers int        `json:"subscribers"`
	Motifs      int        `json:"motifs"`
	Usage       Usage      `json:"usage"`
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

// ValidateRequest is the body of POST /v1/validate/analysis.
type ValidateRequest struct {
	SampleRate int `json:"sample_rate"`
	AnalysisSpec
}

// FieldError points into a request, so that a form can mark the field.
type FieldError struct {
	Path    string `json:"path"`
	Code    string `json:"code"`
	Message string `json:"message"`
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
	OK          bool    `json:"ok"`
	Exists      *string `json:"exists"`
	ContentType *string `json:"content_type"`
	Codec       string  `json:"codec"`
	SampleRate  int     `json:"sample_rate"`
	Channels    int     `json:"channels"`
	BitrateKbps int     `json:"bitrate_kbps"`
	LevelDB     float64 `json:"level_db"`
	Silent      bool    `json:"silent"`
	// Shared: the address is read already (by another caller); the probe
	// opened no connection and SampleRate is the rate it is decoded at.
	Shared   bool      `json:"shared,omitempty"`
	Problems []Problem `json:"problems"`
}

// Memory is one memory kept on disk for an analysis.
type Memory struct {
	Key       string          `json:"key"`
	Settings  json.RawMessage `json:"settings,omitempty"`
	Rules     []Rule          `json:"rules,omitempty"`
	FirstUsed time.Time       `json:"first_used"`
	LastUsed  time.Time       `json:"last_used"`
	Motifs    int             `json:"motifs"`
	Bytes     int64           `json:"bytes"`
	InUse     bool            `json:"in_use"`
}

// Change is one event of GET /v1/events.
type Change struct {
	Type string          `json:"-"`
	Data json.RawMessage `json:"-"`
}

// Hello is the data of engine.hello.
type Hello struct {
	Instance string     `json:"instance"`
	API      APIVersion `json:"api"`
	Resumed  bool       `json:"resumed"` // started from its saved configuration (decided 2026-10-16)
}

// AnalysisState is the data of analysis.state.
type AnalysisState struct {
	Stream    string   `json:"stream"`
	Analysis  string   `json:"analysis"`
	State     string   `json:"state"`
	MemoryKey string   `json:"memory_key"`
	LastError *Problem `json:"last_error,omitempty"`
}

// UsageReport is the data of the usage event.
type UsageReport struct {
	Engine   Usage            `json:"engine"`
	Streams  map[string]Usage `json:"streams"`
	Analyses map[string]Usage `json:"analyses"` // keyed "stream/analysis"
	// Sources gives each source's full cost once, keyed by this caller's
	// stream on it, so that totals do not count a shared source twice.
	Sources map[string]Usage `json:"sources,omitempty"`
}

// Error is an error answer of the engine.
type Error struct {
	Status  int          `json:"-"`
	Code    string       `json:"code"`
	Message string       `json:"message"`
	Errors  []FieldError `json:"errors"`
}

func (e *Error) Error() string { return fmt.Sprintf("engine: %d %s: %s", e.Status, e.Code, e.Message) }

// GroupMember is one analysis in a group (control-api.md, Groups).
type GroupMember struct {
	Stream   string   `json:"stream"`
	Analysis string   `json:"analysis"`
	Lat      *float64 `json:"lat,omitempty"`
	Lon      *float64 `json:"lon,omitempty"`
}

// GroupSpec is what is put for a group.
type GroupSpec struct {
	Members     []GroupMember `json:"members"`
	Analyses    []string      `json:"analyses"`
	HistoryDays int           `json:"history_days,omitempty"`
	Label       string        `json:"label,omitempty"`
	Mode        string        `json:"mode,omitempty"`
}

// Group is a group as the engine reports it.
type Group struct {
	ID      string `json:"id"`
	Members []struct {
		GroupMember
		State      string   `json:"state"`
		TimeSource string   `json:"time_source"`
		DelayS     *float64 `json:"delay_s"`
	} `json:"members"`
	Analyses    []string `json:"analyses"`
	Mode        string   `json:"mode"`
	Label       string   `json:"label"`
	State       string   `json:"state"`
	Subscribers int      `json:"subscribers"`
}
