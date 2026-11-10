package dispatch

import "easy_proxies/internal/config"

// SelectionTiers returns an owned copy so in-flight requests keep a consistent
// selection chain across hot updates.
func (s *Server) SelectionTiers() []config.SelectionTier {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return config.CloneSelectionTiers(s.selectionTiers)
}

func (s *Server) SetSelectionTiers(tiers []config.SelectionTier) {
	s.mu.Lock()
	s.selectionTiers = config.CloneSelectionTiers(tiers)
	s.mu.Unlock()
}
