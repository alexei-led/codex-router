package tui

// Tab is one panel tab, in key order 1-5.
type Tab string

const (
	TabNow        Tab = "now"
	TabRoutes     Tab = "routes"
	TabPolicy     Tab = "policy"
	TabClassifier Tab = "classifier"
	TabUsage      Tab = "usage"
)

// Tabs lists the tabs in display order.
var Tabs = []Tab{TabNow, TabRoutes, TabPolicy, TabClassifier, TabUsage}

// Options configure Run.
type Options struct {
	Socket string
}
