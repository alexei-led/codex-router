package control

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/alexei-led/codex-router/internal/catalog"
	"github.com/alexei-led/codex-router/internal/classifier"
	"github.com/alexei-led/codex-router/internal/config"
	"github.com/alexei-led/codex-router/internal/policy"
	"github.com/alexei-led/codex-router/internal/route"
)

// The control-v2 wire schema is frozen by the literal fixtures in
// testdata/protocol-v2: every name, nesting, null and omitted field a client
// or a panel can see is spelled out there and published here, so a rename or
// a reshape of an engine struct cannot change an already-versioned protocol
// without a deliberate fixture change. The fixtures are written by hand, not
// harvested from these types; they compare byte for byte against one
// json.Marshal of the payload each test builds.

// wireFixture returns a protocol-v2 fixture without its final newline, so it
// compares with one MarshalIndent call.
func wireFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "protocol-v2", name))
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSuffix(string(b), "\n")
}

// wireLines returns the lines of a JSONL fixture.
func wireLines(t *testing.T, name string) []string {
	t.Helper()
	var lines []string
	for _, line := range strings.Split(wireFixture(t, name), "\n") {
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// wireSnapshot is the populated snapshot the populated fixture freezes.
func wireSnapshot() route.Snapshot {
	at := time.Date(2024, 3, 4, 5, 6, 7, 0, time.UTC)
	high := config.TierHigh
	return route.Snapshot{
		Seq:          7,
		ThreadID:     "t-1",
		At:           at,
		Mode:         route.ModePaused,
		StateLost:    true,
		Preset:       config.PresetQuality,
		Pin:          &high,
		Baseline:     config.Route{Model: "gpt-5-codex"},
		Footer:       config.Route{Model: "gpt-5-codex", Effort: config.EffortHigh},
		FooterSynced: true,
		Last: &route.Decision{
			ThreadID: "t-1", TurnID: "turn-1", Step: 2,
			Tier: config.TierHigh, Activity: config.ActivityReview,
			Route:     config.Route{Model: "gpt-5-codex", Effort: config.EffortHigh},
			Original:  config.Route{Model: "gpt-5-mini"},
			Rewritten: true, Reason: policy.ReasonUpgrade,
			Why: "the upgrade mass cleared the bar", Support: "medium 0.42, high 0.91, bar 0.55",
			Estimate: &policy.Estimate{TaxTokens: 4096, Threshold: 0.55, Mass: 0.91, Streak: 2},
			Reading: &route.Reading{
				Tiers:    map[config.Tier]float64{config.TierHigh: 0.91, config.TierMedium: 0.42},
				Activity: config.ActivityReview, ActivityP: 0.8,
				Alternates: []route.Share{{Activity: config.ActivityCode, P: 0.1}},
			},
			MidTurn: true, At: at,
		},
		Recent: []route.Decision{{
			ThreadID: "t-1", TurnID: "turn-0", Step: 1,
			Tier: config.TierMedium, Activity: config.ActivityCode,
			Route:    config.Route{Model: "gpt-5-mini"},
			Original: config.Route{Model: "gpt-5-mini"},
			Reason:   policy.ReasonSameTier,
			Why:      "the answer stayed at the incumbent tier", Support: "medium 0.42",
			SupportParts: []string{"medium 0.42"},
			At:           at,
		}},
		Classifier: classifier.Health{
			Failures: 1, PausedUntil: at, Latency: 250 * time.Millisecond,
			QueueWait: 10 * time.Millisecond, QueueTimeouts: 2, LastError: "timeout",
		},
		ClassifierOffReason: "no key",
		ClassifierLabel:     "clef",
		ClassifierModel:     "deepseek-flash",
		ClassifierMedian:    1500 * time.Millisecond,
		Routes: map[config.Tier]config.Route{
			config.TierMedium: {Model: "gpt-5-mini"},
			config.TierHigh:   {Model: "gpt-5-codex", Effort: config.EffortHigh},
		},
		Group:                catalog.Group{CompHash: "gpt-5", Lite: true, ToolMode: "code"},
		CurrentModelKnown:    true,
		CatalogAvailable:     true,
		Blocked:              []string{"gpt-5-pro"},
		CompatibilityUnknown: []string{"gpt-5.1-codex"},
		Policy: config.Policy{
			UpgradeVotes: 2, UpgradeBase: 0.5, UpgradeSlope: 0.2, UpgradePivotTokens: 8192,
			JumpConfidence: 0.9, DowngradeVotes: 1, DowngradeMass: 0.6, DowngradeSlope: 0.2,
			DowngradePivotTokens: 4096, ContinuationMass: 0.5, EscalationHoldTurns: 2,
			ReviewEveryRequests: 3, MinDwellRequests: 1, ActivityMass: 0.6, ContextFill: 0.85,
		},
		Stats: route.Stats{
			Since: at, Replies: 3, Switches: 1,
			ByTier:     map[config.Tier]int{config.TierHigh: 3},
			ByActivity: map[config.Activity]int{config.ActivityReview: 3},
			Strip:      []config.Tier{config.TierMedium, config.TierHigh},
			LastSwitch: &route.Switch{
				At: at, From: config.TierMedium, To: config.TierHigh,
				Activity: config.ActivityReview, MidTurn: true,
			},
			RepliesPerCell: 1, CacheReusePct: 80, LastInputTokens: 1000,
			LastCachedTokens: 800, UncachedSwitchTokens: 200, FitHeld: 2,
			RoutedUSD: 0.25, BaselineUSD: 0.5, PricedReplies: 3, EligibleReplies: 3,
		},
		ApproxTokens: 1234,
		Window:       200000,
		Worth:        []route.Finding{{Kind: route.FindingCache, Text: "cache reuse 80%, switching wrote 200 uncached tokens"}},
	}
}

// wireSessions is the sessions list the sessions fixture freezes: one routed
// session, then a row that shows which fields carry no omitempty.
func wireSessions() []route.SessionInfo {
	at := time.Date(2024, 3, 4, 5, 6, 7, 0, time.UTC)
	saved, spent := 0.25, 0.75
	return []route.SessionInfo{
		{
			ID: "h/t-1", Home: "h", ThreadID: "t-1",
			Label: "home · api · main", Cwd: "/h/api",
			Mode: route.ModeOn, Tier: config.TierHigh,
			Route:   config.Route{Model: "gpt-5-codex", Effort: config.EffortHigh},
			Replies: 3, Switches: 1,
			Strip:      []config.Tier{config.TierMedium, config.TierHigh},
			LastSwitch: &route.Switch{At: at, From: config.TierMedium, To: config.TierHigh, Activity: config.ActivityReview, MidTurn: true},
			LastActive: at, Attention: route.AttentionPaused, PendingPin: true, FooterSynced: true,
			PricedReplies: 3, EligibleReplies: 3, SavedUSD: &saved, SpentUSD: &spent,
		},
		{ID: "h/t-2", Home: "h", ThreadID: "t-2"},
	}
}

func TestSnapshotWireIsFrozen(t *testing.T) {
	tests := []struct {
		name    string
		fixture string
		snap    route.Snapshot
	}{
		{"populated", "snapshot-populated.json", wireSnapshot()},
		{"empty", "snapshot-empty.json", route.Snapshot{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			want := wireFixture(t, tt.fixture)
			got, err := json.MarshalIndent(tt.snap, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != want {
				t.Fatalf("snapshot JSON changed:\ngot:\n%s\nfixture %s:\n%s", got, tt.fixture, want)
			}
			var back route.Snapshot
			if err := json.Unmarshal([]byte(want), &back); err != nil {
				t.Fatalf("fixture does not decode into a snapshot: %v", err)
			}
			again, err := json.MarshalIndent(back, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			if string(again) != want {
				t.Fatalf("fixture does not round-trip:\ngot:\n%s\nfixture %s:\n%s", again, tt.fixture, want)
			}
		})
	}
}

// TestSnapshotWireKeepsZeroValues records what the populated and empty
// fixtures mean: gone pointers and slices are null, not dropped, and every
// scalar is written even at its zero value.
func TestSnapshotWireKeepsZeroValues(t *testing.T) {
	var empty map[string]any
	if err := json.Unmarshal([]byte(wireFixture(t, "snapshot-empty.json")), &empty); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]any{
		"Pin": nil, "Last": nil, "Recent": nil, "Routes": nil,
		"Blocked": nil, "CompatibilityUnknown": nil, "Worth": nil,
		"Seq": float64(0), "ThreadID": "", "At": "0001-01-01T00:00:00Z", "ClassifierOff": false,
	} {
		got, ok := empty[key]
		if !ok {
			t.Errorf("%s is missing from the empty snapshot; a zero value is written, never dropped", key)
			continue
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s = %#v, want %#v", key, got, want)
		}
	}
}

func TestSessionsWireIsFrozen(t *testing.T) {
	want := wireFixture(t, "sessions.json")
	got, err := json.MarshalIndent(wireSessions(), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("sessions JSON changed:\ngot:\n%s\nfixture sessions.json:\n%s", got, want)
	}
	var back []route.SessionInfo
	if err := json.Unmarshal([]byte(want), &back); err != nil {
		t.Fatalf("fixture does not decode into a sessions list: %v", err)
	}
	again, err := json.MarshalIndent(back, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != want {
		t.Fatalf("fixture does not round-trip:\ngot:\n%s\nfixture sessions.json:\n%s", again, want)
	}
}

// TestSessionRowOmitsOptionalFields pins which names a row may leave out, and
// that a row authored with only its required fields decodes to zero values.
func TestSessionRowOmitsOptionalFields(t *testing.T) {
	optional := []string{"last_switch", "attention", "pending_pin", "saved_usd", "spent_usd"}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(wireFixture(t, "sessions-sparse.json")), &rows); err != nil {
		t.Fatalf("sparse fixture does not decode: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("sparse fixture has %d rows, want 1", len(rows))
	}
	for _, key := range optional {
		if v, ok := rows[0][key]; ok {
			t.Errorf("%s = %#v, want the name left out", key, v)
		}
	}

	var sparse []route.SessionInfo
	if err := json.Unmarshal([]byte(wireFixture(t, "sessions-sparse.json")), &sparse); err != nil {
		t.Fatal(err)
	}
	row := sparse[0]
	if row.ID != "h/t-2" || row.Home != "h" || row.ThreadID != "t-2" {
		t.Errorf("row = %+v, want h/t-2 named", row)
	}
	if row.Attention != "" || row.PendingPin || row.LastSwitch != nil || row.SavedUSD != nil || row.SpentUSD != nil {
		t.Errorf("row = %+v, want the optional fields zero", row)
	}
	if !reflect.DeepEqual(row, wireSessions()[1]) {
		t.Errorf("sparse row = %+v, want %+v", row, wireSessions()[1])
	}

	// The names without omitempty come back when this build publishes the row.
	b, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"id", "home", "thread_id", "label", "cwd", "route", "strip", "last_active", "footer_synced"} {
		if _, ok := out[key]; !ok {
			t.Errorf("%s is missing from %s; it has no omitempty", key, b)
		}
	}
}

func TestCommandWireIsFrozen(t *testing.T) {
	// The hello names the protocol version: 2 is the frozen control-v2 number
	// and no fixture here may move it.
	if Protocol != 2 {
		t.Fatalf("control protocol = %d, want 2", Protocol)
	}
	want := []Request{
		{Command: route.Command{Op: OpHello}, Protocol: 2},
		{Command: route.Command{Op: OpSubscribe, Session: "h/t-1", ID: "c-1"}},
		{Command: route.Command{Op: route.OpPin, Session: "h/t-1", ID: "c-2", Tier: config.TierHigh}},
		{Command: route.Command{Op: route.OpPause, Scope: route.ScopeHome, ID: "c-3"}},
		{Command: route.Command{Op: route.OpPreset, Preset: config.PresetThrifty, ID: "c-4"}},
		{Command: route.Command{Op: route.OpSnapshot, ID: "c-5"}},
	}
	lines := wireLines(t, "commands.jsonl")
	if len(lines) != len(want) {
		t.Fatalf("commands.jsonl has %d lines, want %d", len(lines), len(want))
	}
	for i, line := range lines {
		var got Request
		if err := json.Unmarshal([]byte(line), &got); err != nil {
			t.Fatalf("line %d does not decode: %v (%s)", i, err, line)
		}
		if !reflect.DeepEqual(got, want[i]) {
			t.Errorf("line %d = %+v, want %+v", i, got, want[i])
		}
		b, err := json.Marshal(got)
		if err != nil {
			t.Fatal(err)
		}
		if string(b) != line {
			t.Errorf("line %d re-encodes as %s, want %s", i, b, line)
		}
	}
}

func TestReplyWireIsFrozen(t *testing.T) {
	want := []Message{
		{Type: MessageAck, ID: "c-2"},
		{Type: MessageError, ID: "c-5", Error: `unknown op "lint"`},
	}
	lines := wireLines(t, "replies.jsonl")
	if len(lines) != len(want) {
		t.Fatalf("replies.jsonl has %d lines, want %d", len(lines), len(want))
	}
	for i, line := range lines {
		var got Message
		if err := json.Unmarshal([]byte(line), &got); err != nil {
			t.Fatalf("line %d does not decode: %v (%s)", i, err, line)
		}
		if !reflect.DeepEqual(got, want[i]) {
			t.Errorf("line %d = %+v, want %+v", i, got, want[i])
		}
		b, err := json.Marshal(want[i])
		if err != nil {
			t.Fatal(err)
		}
		if string(b) != line {
			t.Errorf("line %d encodes as %s, want %s", i, b, line)
		}
	}

	// A message carries only what it has: no snapshot without one.
	if b, err := json.Marshal(Message{Type: MessageSnapshot, Session: "h/t-1"}); err != nil {
		t.Fatal(err)
	} else if got, want := string(b), `{"type":"snapshot","session":"h/t-1"}`; got != want {
		t.Errorf("bare snapshot message = %s, want %s", got, want)
	}
}

// TestUnknownWireFieldsAreIgnored is the compatibility direction a frozen
// schema needs: a field a later protocol-v2 revision adds is dropped by this
// build, not rejected, at every level of the payload.
func TestUnknownWireFieldsAreIgnored(t *testing.T) {
	fixture := wireFixture(t, "snapshot-forward.json")
	var m Message
	if err := json.Unmarshal([]byte(fixture), &m); err != nil {
		t.Fatalf("a snapshot with added fields does not decode: %v", err)
	}
	if m.Type != MessageSnapshot || m.Session != "h/t-1" || m.Snapshot == nil {
		t.Fatalf("message = %+v, want a snapshot for h/t-1", m)
	}
	s := *m.Snapshot
	switch {
	case s.Seq != 9 || s.ThreadID != "t-9":
		t.Errorf("snapshot = %d/%q, want 9/t-9", s.Seq, s.ThreadID)
	case s.Last == nil || s.Last.Tier != config.TierHigh:
		t.Errorf("Last = %+v, want the tier high", s.Last)
	case s.Last.Estimate == nil || s.Last.Estimate.TaxTokens != 512:
		t.Errorf("Last.Estimate = %+v, want 512 tax tokens", s.Last.Estimate)
	case s.Group.CompHash != "gpt-5":
		t.Errorf("Group = %+v, want comp hash gpt-5", s.Group)
	case s.Classifier.Failures != 2 || s.Classifier.LastError != "http 503":
		t.Errorf("Classifier = %+v, want 2 failures of http 503", s.Classifier)
	}

	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "future") {
		t.Errorf("this build re-published a field it does not know: %s", b)
	}
}

// TestMissingOptionalWireFields is the other direction: a payload without the
// omitted names decodes to zero values, and the zero values it then publishes
// are still the ones the fixtures froze.
func TestMissingOptionalWireFields(t *testing.T) {
	var m Message
	if err := json.Unmarshal([]byte(wireFixture(t, "snapshot-sparse.json")), &m); err != nil {
		t.Fatalf("a sparse snapshot does not decode: %v", err)
	}
	if m.Type != MessageSnapshot || m.ID != "" || m.Seq != 0 || m.Items != nil || m.Error != "" || m.Hello != nil {
		t.Errorf("message = %+v, want only its type and session", m)
	}
	if m.Snapshot == nil || m.Snapshot.ThreadID != "t-2" {
		t.Fatalf("snapshot = %+v, want thread t-2", m.Snapshot)
	}
	s := *m.Snapshot
	if s.Seq != 0 || s.Mode != "" || s.Pin != nil || s.Last != nil || s.Recent != nil ||
		s.Routes != nil || s.Blocked != nil || s.Worth != nil || s.Stats.ByTier != nil {
		t.Errorf("snapshot = %+v, want zero optional state", s)
	}

	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"Last":null`, `"Recent":null`, `"Pin":null`, `"Routes":null`, `"Worth":null`, `"Seq":0`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("re-encoded sparse message lacks %s: %s", want, b)
		}
	}
	for _, unwanted := range []string{`"items"`, `"hello"`, `"error"`, `"id"`, `"seq"`} {
		if strings.Contains(string(b), unwanted) {
			t.Errorf("re-encoded sparse message has %s, want the name left out: %s", unwanted, b)
		}
	}
}
