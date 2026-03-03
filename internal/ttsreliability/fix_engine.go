package ttsreliability

// DecideFix determines what action to take after a chunk QA failure.
// It returns a FixAction describing the retry strategy and any settings changes.
func DecideFix(failType FailType, attemptNum int, current TTSSettings) FixAction {
	switch failType {

	case FailLowVolumeDrift:
		switch attemptNum {
		case 1:
			return FixAction{
				Action:      "retry",
				PostProcess: "loudnorm",
				NewSettings: current,
				Reason:      "low volume: applying loudnorm post-process",
			}
		case 2:
			return FixAction{
				Action:      "retry_smaller",
				PostProcess: "loudnorm",
				NewSettings: current,
				Reason:      "low volume: halving chunk + loudnorm",
			}
		default:
			return giveUp(failType)
		}

	case FailClipping:
		switch attemptNum {
		case 1:
			s := current
			if s.Stability == 0 {
				s.Stability = 0.80
			}
			s.Stability = clamp(s.Stability-0.10, 0.20, 1.0)
			return FixAction{
				Action:      "retry",
				NewSettings: s,
				Reason:      "clipping: reducing stability",
			}
		case 2:
			return FixAction{
				Action:      "retry",
				PostProcess: "loudnorm",
				NewSettings: current,
				Reason:      "clipping: applying loudnorm limiter",
			}
		default:
			return giveUp(failType)
		}

	case FailRoboticArtifact:
		switch attemptNum {
		case 1:
			return FixAction{
				Action:      "retry",
				NewSettings: TTSSettings{}, // reset to defaults
				Reason:      "robotic artifact: resetting to default settings",
			}
		case 2:
			return FixAction{
				Action:      "retry_smaller",
				NewSettings: TTSSettings{}, // defaults + smaller chunk
				Reason:      "robotic artifact: halving chunk + defaults",
			}
		default:
			s := current
			s.ModelID = switchModel(s.ModelID)
			return FixAction{
				Action:      "retry",
				NewSettings: s,
				Reason:      "robotic artifact: switching model",
			}
		}

	case FailWeirdTimbreShift:
		switch attemptNum {
		case 1:
			s := current
			if s.SimilarityBoost == 0 {
				s.SimilarityBoost = 0.75
			}
			s.SimilarityBoost = clamp(s.SimilarityBoost+0.10, 0.0, 1.0)
			return FixAction{
				Action:      "retry",
				NewSettings: s,
				Reason:      "timbre shift: increasing similarity boost",
			}
		case 2:
			s := current
			s.ModelID = switchModel(s.ModelID)
			return FixAction{
				Action:      "retry",
				NewSettings: s,
				Reason:      "timbre shift: switching model",
			}
		default:
			return giveUp(failType)
		}

	case FailSilenceAnomaly:
		switch attemptNum {
		case 1:
			return FixAction{
				Action:      "retry_smaller",
				NewSettings: current,
				Reason:      "silence anomaly: halving chunk",
			}
		case 2:
			return FixAction{
				Action:      "retry",
				NewSettings: current,
				Reason:      "silence anomaly: retrying same settings",
			}
		default:
			return giveUp(failType)
		}

	default:
		return giveUp(failType)
	}
}

func giveUp(ft FailType) FixAction {
	return FixAction{
		Action: "give_up",
		Reason: "max attempts reached for " + string(ft),
	}
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// switchModel toggles between monolingual and multilingual v2.
func switchModel(current string) string {
	switch current {
	case "eleven_monolingual_v1", "":
		return "eleven_multilingual_v2"
	case "eleven_multilingual_v2":
		return "eleven_turbo_v2_5"
	default:
		return "eleven_multilingual_v2"
	}
}
