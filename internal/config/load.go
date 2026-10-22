package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Path returns the config file location: $XDG_CONFIG_HOME/codex-router/config.json,
// else ~/.config/codex-router/config.json. A relative XDG_CONFIG_HOME is ignored
// per the XDG spec.
func Path() (string, error) {
	if dir := os.Getenv("XDG_CONFIG_HOME"); filepath.IsAbs(dir) {
		return filepath.Join(dir, DirName, FileName), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("config path: %w", err)
	}
	return filepath.Join(home, ".config", DirName, FileName), nil
}

// Load reads the config file over Default(): keys the file omits keep their
// defaults, a map entry the file names replaces the default entry whole, and
// a classifiers entry changes only the fields it names. A
// missing file yields the defaults. Unknown keys and invalid values are errors.
func Load(path string) (Config, error) {
	cfg := Default()
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		if strings.Contains(err.Error(), `unknown field "preset"`) {
			err = fmt.Errorf("%w (%s)", err, presetRenamed)
		}
		return Config{}, fmt.Errorf("parse %s: %w", path, err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return Config{}, fmt.Errorf("parse %s: trailing data after JSON object", path)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

// UnmarshalJSON decodes each entry over the one already in cs. Unknown
// fields are errors, as in the rest of the file.
func (cs *Classifiers) UnmarshalJSON(b []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	if *cs == nil {
		*cs = Classifiers{}
	}
	for name, msg := range raw {
		c := (*cs)[name]
		dec := json.NewDecoder(bytes.NewReader(msg))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&c); err != nil {
			return fmt.Errorf("classifiers.%s: %w", name, err)
		}
		(*cs)[name] = c
	}
	return nil
}

// UnmarshalJSON decodes each entry over the one already in ms, so a file
// changes only the patch fields it names; null clears a built-in field.
// Unknown fields are errors.
func (ms *Modes) UnmarshalJSON(b []byte) error {
	var raw map[RoutingMode]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	if *ms == nil {
		*ms = Modes{}
	}
	for mode, msg := range raw {
		m := (*ms)[mode]
		dec := json.NewDecoder(bytes.NewReader(msg))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&m); err != nil {
			return fmt.Errorf("modes.%s: %w", mode, err)
		}
		(*ms)[mode] = m
	}
	return nil
}
