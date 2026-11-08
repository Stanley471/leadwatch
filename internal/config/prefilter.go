package config

// PrefilterConfig holds lists of signals for prefiltering incoming leads.
type PrefilterConfig struct {
	// IntentSignals are keywords/phrases indicating someone is looking to hire or buy services.
	IntentSignals []string
	// SpamSignals are keywords/phrases indicating self-promotion, selling services, or spam.
	SpamSignals []string
}

// DefaultPrefilterConfig returns the standard hiring and spam signal keywords.
func DefaultPrefilterConfig() PrefilterConfig {
	return PrefilterConfig{
		IntentSignals: []string{
			"need",
			"looking for",
			"hiring",
			"seeking",
			"want",
			"budget",
			"quote",
			"build me",
			"develop",
			"[hiring]",
		},
		SpamSignals: []string{
			"[for hire]",
			"dm me for services",
			"i will build",
		},
	}
}
