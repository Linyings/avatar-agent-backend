package avatar

import (
	"fmt"
	"math"
)

func ValidateVoiceSpeed(speed float64) error {
	if speed != 0 && (math.IsNaN(speed) || math.IsInf(speed, 0) || speed < .8 || speed > 1.3) {
		return fmt.Errorf("配音语速需在 0.8–1.3 倍之间")
	}
	return nil
}

// Missing values keep historical speech unchanged. Zero clears an override.
func EffectiveVoiceSpeed(in WorkflowInput, segment string) float64 {
	if value := in.SegmentVoiceSpeeds[segment]; value != 0 {
		return value
	}
	if in.VoiceSpeed != 0 {
		return in.VoiceSpeed
	}
	return 1
}

func (e WorkflowEdits) ApplyVoice(in *WorkflowInput) {
	if e.Voice != "" {
		in.Voice = e.Voice
	}
	if e.VoiceModel != "" {
		in.VoiceModel = e.VoiceModel
	}
	if e.VoiceSpeed != nil {
		in.VoiceSpeed = *e.VoiceSpeed
	}
	if e.SegmentVoiceSpeeds != nil {
		merged := map[string]float64{}
		for id, value := range in.SegmentVoiceSpeeds {
			merged[id] = value
		}
		for id, value := range e.SegmentVoiceSpeeds {
			if value == 0 {
				delete(merged, id)
			} else {
				merged[id] = value
			}
		}
		in.SegmentVoiceSpeeds = merged
	}
}
