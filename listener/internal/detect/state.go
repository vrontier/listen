package detect

// StateInput is the smoothed feature set the classifier works on.
type StateInput struct {
	Energy           float64 // 0..1, adaptive
	LevelDB          float64 // absolute RMS level, dBFS
	CentroidHz       float64
	Entropy          float64
	Flatness         float64
	Harmonicity      float64
	Novelty          float64
	RecentTransients int // in the last few seconds
	ActiveResonances int
}

// Classify maps measured features to one of the descriptive states of §8.
// The rules are ordered; the first match wins.
func Classify(in StateInput) string {
	switch {
	case in.LevelDB < -60 || in.Energy < 0.08:
		return "quiet"
	case in.RecentTransients >= 2:
		return "transient_activity"
	case in.ActiveResonances > 0 && in.Harmonicity >= 0.35 && in.Novelty < 0.35:
		return "stable_resonance"
	case in.Harmonicity >= 0.35:
		return "harmonic_activity"
	case in.Novelty >= 0.45:
		return "evolving_texture"
	case in.CentroidHz < 700 && in.Harmonicity < 0.25:
		return "wind_like"
	case in.Flatness >= 0.3 && in.Harmonicity < 0.25:
		return "broadband_noise"
	case in.ActiveResonances > 0:
		return "stable_resonance"
	default:
		return "unknown"
	}
}
