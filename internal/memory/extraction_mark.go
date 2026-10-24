package memory

// ExtractionMark returns the last user message ID the extractor judged for
// a session, or "" when none is recorded.
func (s *Store) ExtractionMark(session string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.state()
	if err != nil {
		return "", err
	}
	return state.ExtractionMarks[session], nil
}

// SetExtractionMark records the last user message ID judged for a session.
func (s *Store) SetExtractionMark(session, messageID string) error {
	if session == "" || messageID == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.updateStateLocked(func(state *storeState) {
		if state.ExtractionMarks == nil {
			state.ExtractionMarks = map[string]string{}
		}
		state.ExtractionMarks[session] = messageID
	})
}
