package facts_test

import (
	"testing"

	"github.com/alexei-led/codex-router/internal/facts"
)

func TestFailureEpochRequiresBothFailuresAfterBoundary(t *testing.T) {
	items := repeated(execOut(1, "FAIL", "1"), execOut(1, "FAIL", "1"))
	for _, from := range []int{0, 2, 3, 6, 99} {
		o := opts
		o.FailureFrom = from
		f := facts.Extract(request(items...), o)
		if (f.Failure != nil) != (from <= 2) {
			t.Errorf("boundary %d failure=%+v", from, f.Failure)
		}
	}
}
