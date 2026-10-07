package ai

// prepareRequestHistory is shared by every wire adapter. Canonical stored history
// remains untouched; provider-private replay is used only on its originating route.
func prepareRequestHistory(messages []Message, api, endpoint, model string) []Message {
	out := repairToolHistory(stripAuthored(messages))
	for i := range out {
		if !replayMatches(out[i], api, endpoint, model) {
			out[i].Replay = nil
		}
	}
	return out
}
