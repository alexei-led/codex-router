package launcher

import (
	"context"
	"strings"
	"testing"
)

func TestProviderOverrideMustBeEffectiveBeforeStartingTUI(t *testing.T) {
	h := newHarness(t, envProvider+"=managed-gateway")
	h.opts.Config.Proxy.Upstream = newUpstream(t).URL
	code, err := Run(context.Background(), h.opts)
	if err != nil || code != 0 {
		t.Fatalf("Run = %d, %v", code, err)
	}
	if len(h.execed) != 1 || len(h.recorded(t, "remote")) != 0 {
		t.Fatalf("must fall back before TUI startup: fallback=%d remote=%d", len(h.execed), len(h.recorded(t, "remote")))
	}
	if !strings.Contains(h.stderr.String(), "did not select the router provider (got \"managed-gateway\")") {
		t.Fatalf("missing provider diagnostic: %s", h.stderr.String())
	}
}
