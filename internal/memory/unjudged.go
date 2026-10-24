package memory

// UnjudgedMessage is a user message the hidden extractor has not judged yet:
// its turn was stopped or failed, or its extraction did not finish.
type UnjudgedMessage struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

// maxUnjudgedPerSession keeps the newest held messages of a session.
const maxUnjudgedPerSession = 8

// HoldUnjudged adds messages to a session's unjudged list, keeping the newest
// maxUnjudgedPerSession and one entry per ID.
func (s *Store) HoldUnjudged(session string, msgs []UnjudgedMessage) error {
	if session == "" || len(msgs) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.updateStateLocked(func(state *storeState) {
		if state.Unjudged == nil {
			state.Unjudged = map[string][]UnjudgedMessage{}
		}
		held := state.Unjudged[session]
		for _, m := range msgs {
			if m.ID == "" {
				continue
			}
			kept := held[:0]
			for _, h := range held {
				if h.ID != m.ID {
					kept = append(kept, h)
				}
			}
			held = append(kept, m)
		}
		if len(held) > maxUnjudgedPerSession {
			held = held[len(held)-maxUnjudgedPerSession:]
		}
		if len(held) == 0 {
			delete(state.Unjudged, session)
			return
		}
		state.Unjudged[session] = held
	})
}

// TakeUnjudged returns and clears a session's unjudged list.
func (s *Store) TakeUnjudged(session string) ([]UnjudgedMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.state()
	if err != nil || len(state.Unjudged[session]) == 0 {
		return nil, err
	}
	held := state.Unjudged[session]
	delete(state.Unjudged, session)
	return held, s.saveState(state)
}
