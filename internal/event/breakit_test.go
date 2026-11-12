package event

import "testing"

func TestTryToBreakIt(t *testing.T) {
	as := TryToBreakIt()
	if len(as) < 18 {
		t.Fatalf("only %d attacks", len(as))
	}
	for _, a := range as {
		if !a.Refused {
			t.Errorf("NOT REFUSED: %s", a.Name)
		}
		t.Logf("refused: %-38s %s", a.Name, a.Reason)
	}
}
