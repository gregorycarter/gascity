package tmux

import "testing"

// The ggc-dpb39 fix: paneBusy alone cannot confirm a submit, because a fast
// turn starts and finishes between polls. These tests cover the second signal —
// the draft clearing from the prompt line — and the guards around it.

// TestSubmitEnterAndConfirmDraftClearedConfirms proves a fast turn is no longer
// reported as unconfirmed. Busy is never observed (the turn finished between
// polls), but the draft cleared, which is durable evidence the Enter landed.
func TestSubmitEnterAndConfirmDraftClearedConfirms(t *testing.T) {
	var enters int
	busy := func() (bool, error) { return false, nil } // spinner never caught
	// Draft is present before Enter and gone once a send lands.
	draftPending := func() (bool, error) { return enters == 0, nil }
	sendEnter := func() error { enters++; return nil }

	confirmed, err := submitEnterAndConfirm(sendEnter, func() {}, busy, draftPending, noSleep)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if !confirmed {
		t.Fatal("confirmed = false, want true: a cleared draft confirms the submit")
	}
	if enters != 1 {
		t.Fatalf("enters = %d, want 1: a confirmed submit must not be re-sent", enters)
	}
}

// TestSubmitEnterAndConfirmDraftStillPendingFails proves the genuinely-lost
// Enter is still reported. This is the case that must NOT be swallowed: the
// draft never clears, so the caller gets confirmed=false and requeues.
func TestSubmitEnterAndConfirmDraftStillPendingFails(t *testing.T) {
	busy := func() (bool, error) { return false, nil }
	draftPending := func() (bool, error) { return true, nil } // never submits
	sendEnter := func() error { return nil }

	confirmed, err := submitEnterAndConfirm(sendEnter, func() {}, busy, draftPending, noSleep)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if confirmed {
		t.Fatal("confirmed = true, want false: a draft still at the prompt was never submitted")
	}
}

// TestSubmitEnterAndConfirmEmptyPromptDoesNotConfirm guards the false positive:
// if no draft was ever observed (the paste itself failed), an empty prompt must
// not be read as a successful submit.
func TestSubmitEnterAndConfirmEmptyPromptDoesNotConfirm(t *testing.T) {
	busy := func() (bool, error) { return false, nil }
	draftPending := func() (bool, error) { return false, nil } // never had a draft
	sendEnter := func() error { return nil }

	confirmed, err := submitEnterAndConfirm(sendEnter, func() {}, busy, draftPending, noSleep)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if confirmed {
		t.Fatal("confirmed = true, want false: an always-empty prompt is not evidence of a submit")
	}
}

func TestPromptLineHasDraft(t *testing.T) {
	cases := []struct {
		name  string
		lines []string
		want  bool
	}{
		{"empty prompt is submitted", []string{"some output", "❯ "}, false},
		{"prompt carrying text is unsubmitted", []string{"some output", "❯ check hook again"}, true},
		{"newest prompt wins over history", []string{"❯ old command", "output", "❯ "}, false},
		{"box-bordered prompt with draft", []string{"│ ❯ do the thing"}, true},
		{"no prompt at all", []string{"just output"}, false},
		{"nbsp after glyph is still empty", []string{"\u276f\u00a0"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := promptLineHasDraft(tc.lines, DefaultReadyPromptPrefix); got != tc.want {
				t.Fatalf("promptLineHasDraft(%q) = %v, want %v", tc.lines, got, tc.want)
			}
		})
	}
}
