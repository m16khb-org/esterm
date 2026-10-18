package main

import "testing"

func TestMainObsNeedsTheFeedAndAWholeMainFrame(t *testing.T) {
	p := newMMPipeline([]champIcon{{ref: champRef{PID: 1, Team: 100, Champ: "X"}}}, nil)
	lay := mmLayouts[0]
	full := make([]byte, mmMainBytes)
	if got := p.mainObs(0, full, lay, nil); got != nil {
		t.Fatalf("without lives (level and health) bars cannot be matched: %v", got)
	}
	if got := p.mainObs(0, full[:100], lay, mmLives{1: {Level: 5, HP: 1}}); got != nil {
		t.Fatalf("a truncated main frame must be ignored: %v", got)
	}
	// a black main view has no health bars
	if got := p.mainObs(0, full, lay, mmLives{1: {Level: 5, HP: 1}}); len(got) != 0 {
		t.Fatalf("a black frame gave observations: %v", got)
	}
}
