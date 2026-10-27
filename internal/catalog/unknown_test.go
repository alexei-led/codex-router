package catalog_test

import (
	"testing"

	"github.com/alexei-led/codex-router/internal/catalog"
)

func TestMissingCompatibilityHashRemainsUnknown(t *testing.T) {
	c, err := catalog.Parse([]byte(`{"models":[{"slug":"a"},{"slug":"b"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, known := c.GroupOf("a"); known {
		t.Error("missing compatibility hash reported known")
	}
	if c.Compatible("a", "b") {
		t.Error("two missing compatibility hashes treated as equal")
	}
}
