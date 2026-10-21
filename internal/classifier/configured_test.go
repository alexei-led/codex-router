package classifier

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alexei-led/codex-router/internal/config"
)

// TestConfigured keeps the classifier-selection table: which key and endpoint
// values a selection needs, where each comes from, and the reason it is off.
func TestConfigured(t *testing.T) {
	withAPI := func(api string) config.Config {
		cfg := config.Default()
		cc := cfg.Classifiers[cfg.Classifier]
		cc.API = api
		cfg.Classifiers[cfg.Classifier] = cc
		return cfg
	}
	unknown := config.Default()
	unknown.Classifier = "missing"
	dir := t.TempDir()
	keyFile := func(name, body string, mode os.FileMode) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		return path
	}
	withFile := func(path string) config.Config {
		cfg := config.Default()
		cc := cfg.Classifiers[cfg.Classifier]
		cc.EnvFile = path
		cfg.Classifiers[cfg.Classifier] = cc
		return cfg
	}
	good := keyFile("good.env", "export TYPESAFE_API_KEY='from-file'\n", 0o600)
	shared := keyFile("shared.env", "TYPESAFE_API_KEY=from-file\n", 0o644)
	noKey := keyFile("nokey.env", "OTHER=x\n", 0o600)
	keyFile("home.env", "TYPESAFE_API_KEY=from-file\n", 0o600)
	cfAccount := keyFile("cf-account.env", "CLOUDFLARE_ACCOUNT_ID=acct\n", 0o600)
	cfBoth := keyFile("cf-both.env", "CLOUDFLARE_API_TOKEN=t\nCLOUDFLARE_ACCOUNT_ID=acct\n", 0o600)
	openaiFile := keyFile("openai.env", "OPENAI_API_KEY=k\n", 0o600)
	missing := filepath.Join(dir, "missing.env")
	withClassifier := func(name string) config.Config {
		cfg := config.Default()
		cfg.Classifier = name
		return cfg
	}
	withEnvFile := func(name, path string) config.Config {
		cfg := withClassifier(name)
		cc := cfg.Classifiers[name]
		cc.EnvFile = path
		cfg.Classifiers[name] = cc
		return cfg
	}
	withEndpoint := func(endpoint string) config.Config {
		cfg := config.Default()
		cc := cfg.Classifiers[cfg.Classifier]
		cc.Endpoint = endpoint
		cfg.Classifiers[cfg.Classifier] = cc
		return cfg
	}
	cases := []struct {
		name       string
		cfg        config.Config
		env        map[string]string
		wantOn     bool
		wantReason string
	}{
		{"key set", config.Default(), map[string]string{"TYPESAFE_API_KEY": "k"}, true, ""},
		{"key missing", config.Default(), nil, false, "TYPESAFE_API_KEY is not set"},
		{"key from envFile", withFile(good), nil, true, ""},
		{"envFile under ~/", withFile("~/home.env"), map[string]string{"HOME": dir}, true, ""},
		{"env wins over envFile", withFile(missing), map[string]string{"TYPESAFE_API_KEY": "k"}, true, ""},
		{"envFile shared", withFile(shared), nil, false,
			"TYPESAFE_API_KEY is not set; envFile " + shared + ": open to group or others; chmod 600 it"},
		{"envFile without key", withFile(noKey), nil, false,
			"TYPESAFE_API_KEY is not set; envFile " + noKey + ": TYPESAFE_API_KEY: key not in file"},
		{"envFile missing", withFile(missing), nil, false,
			"TYPESAFE_API_KEY is not set; envFile open " + missing + ": no such file or directory"},
		{"clef account and token in env", withClassifier("clef"), map[string]string{"CLOUDFLARE_API_TOKEN": "t", "CLOUDFLARE_ACCOUNT_ID": "a"}, true, ""},
		{"clef account in envFile", withEnvFile("clef", cfAccount), map[string]string{"CLOUDFLARE_API_TOKEN": "t"}, true, ""},
		{"clef token and account in envFile", withEnvFile("clef-omni", cfBoth), nil, true, ""},
		{"clef account missing", withClassifier("clef"), map[string]string{"CLOUDFLARE_API_TOKEN": "t"}, false, "CLOUDFLARE_ACCOUNT_ID is not set"},
		{"clef token missing wins", withClassifier("clef"), nil, false, "CLOUDFLARE_API_TOKEN is not set"},
		{"clef account not in envFile", withEnvFile("clef-flash", noKey), map[string]string{"CLOUDFLARE_API_TOKEN": "t"}, false,
			"CLOUDFLARE_ACCOUNT_ID is not set; envFile " + noKey + ": CLOUDFLARE_ACCOUNT_ID: key not in file"},
		{"clef account envFile shared", withEnvFile("clef", shared), map[string]string{"CLOUDFLARE_API_TOKEN": "t"}, false,
			"CLOUDFLARE_ACCOUNT_ID is not set; envFile " + shared + ": open to group or others; chmod 600 it"},
		{"openai key set", withClassifier("openai"), map[string]string{"OPENAI_API_KEY": "k"}, true, ""},
		{"openai key missing", withClassifier("openai"), nil, false, "OPENAI_API_KEY is not set"},
		{"openai key from envFile", withEnvFile("openai", openaiFile), nil, true, ""},
		{"bad placeholder", withEndpoint("https://x/{acct}"), map[string]string{"TYPESAFE_API_KEY": "k"}, false,
			"endpoint placeholder {acct}: name must match ^[A-Z][A-Z0-9_]*$"},
		{"no key needed", func() config.Config { c := config.Default(); c.Classifier = "ollama"; return c }(), nil, true, ""},
		{"not configured", unknown, nil, false, "no classifier configured"},
		{"off", func() config.Config { c := config.Default(); c.Classifier = config.ClassifierOff; return c }(),
			map[string]string{"TYPESAFE_API_KEY": "k"}, false, "classifier is off in the config"},
		{"unknown api", withAPI("bogus"), map[string]string{"TYPESAFE_API_KEY": "k"}, false, "unknown api bogus"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			asker, reason := Configured(tc.cfg.Classifier, tc.cfg.Classifiers, func(k string) string { return tc.env[k] })
			if on := asker != nil; on != tc.wantOn || reason != tc.wantReason {
				t.Errorf("Configured = on %v, %q; want on %v, %q", on, reason, tc.wantOn, tc.wantReason)
			}
		})
	}
}

// TestConfiguredTakesTheSelectionAlone pins the narrow inputs: the selected
// name and the definitions, with no config of more than that.
func TestConfiguredTakesTheSelectionAlone(t *testing.T) {
	cc := config.Classifier{API: config.APIOllama, Label: "local", Model: "m", Endpoint: "http://localhost:11434/api"}
	cases := []struct {
		name       string
		selected   string
		defs       map[string]config.Classifier
		wantOn     bool
		wantReason string
	}{
		{"selected in defs", "local", map[string]config.Classifier{"local": cc}, true, ""},
		{"empty selection", "", map[string]config.Classifier{"local": cc}, false, "no classifier configured"},
		{"empty defs", "local", nil, false, "no classifier configured"},
		{"off without defs", config.ClassifierOff, nil, false, "classifier is off in the config"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			asker, reason := Configured(tc.selected, tc.defs, func(string) string { return "" })
			if on := asker != nil; on != tc.wantOn || reason != tc.wantReason {
				t.Errorf("Configured = on %v, %q; want on %v, %q", on, reason, tc.wantOn, tc.wantReason)
			}
		})
	}
}

// TestConfiguredEscapesPlaceholderValues keeps endpoint values path-escaped:
// an account id with a slash must stay one path segment.
func TestConfiguredEscapesPlaceholderValues(t *testing.T) {
	cfg := config.Default()
	cc := cfg.Classifiers[cfg.Classifier]
	cc.Endpoint = "https://classifier.test/{ACCOUNT}/answer"
	cc.KeyEnv = "PLACEHOLDER_TEST_KEY"
	cfg.Classifiers[cfg.Classifier] = cc
	env := map[string]string{"PLACEHOLDER_TEST_KEY": "the-key", "ACCOUNT": "team/a b"}
	asker, why := Configured(cfg.Classifier, cfg.Classifiers, func(k string) string { return env[k] })
	if asker == nil || why != "" {
		t.Fatalf("Configured = on %v, %q; want the classifier on", asker != nil, why)
	}
	c, ok := asker.(*Client)
	if !ok {
		t.Fatalf("Configured returned %T, want *Client", asker)
	}
	if want := "https://classifier.test/team%2Fa%20b/answer"; c.cfg.Endpoint != want {
		t.Errorf("endpoint = %q, want %q", c.cfg.Endpoint, want)
	}
	if c.key != "the-key" {
		t.Errorf("key = %q, want the env value", c.key)
	}
}

// TestConfiguredReasonsHoldNoValue keeps every reason free of the credential
// and endpoint values it looked for: the reason goes to the log and the panel.
func TestConfiguredReasonsHoldNoValue(t *testing.T) {
	const secret = "synthetic-file-secret"
	dir := t.TempDir()
	other := filepath.Join(dir, "other.env")
	if err := os.WriteFile(other, []byte("OTHER_KEY="+secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	shared := filepath.Join(dir, "shared.env")
	if err := os.WriteFile(shared, []byte("TYPESAFE_API_KEY="+secret+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(shared, 0o644); err != nil {
		t.Fatal(err)
	}
	keyOnly := filepath.Join(dir, "key.env")
	if err := os.WriteFile(keyOnly, []byte("TYPESAFE_API_KEY="+secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(keyOnly, 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name     string
		env      map[string]string
		envFile  string
		endpoint string
	}{
		{"key not in the file", nil, other, ""},
		{"file is open to others", nil, shared, ""},
		// The key is read, then a placeholder fails: the key stays out of the reason.
		{"missing placeholder value", nil, keyOnly, "https://classifier.test/{ACCOUNT}/answer"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Default()
			cc := cfg.Classifiers[cfg.Classifier]
			cc.EnvFile = tc.envFile
			if tc.endpoint != "" {
				cc.Endpoint = tc.endpoint
			}
			cfg.Classifiers[cfg.Classifier] = cc
			asker, why := Configured(cfg.Classifier, cfg.Classifiers, func(k string) string { return tc.env[k] })
			if asker != nil || why == "" {
				t.Fatalf("Configured = on %v, %q; want it off with a reason", asker != nil, why)
			}
			if strings.Contains(why, secret) {
				t.Errorf("reason leaks the value: %q", why)
			}
		})
	}
}
