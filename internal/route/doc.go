// Package route is the router engine: it keys state by thread, turn and
// request fingerprint, asks the classifier on a turn's first step and in
// background reviews, runs the policy, checks the compatibility group and
// context fit, pins a decision for retries, and publishes decisions and
// snapshots. It implements proxy.Decider, proxy.Observer and, with the
// endpoint's hello, control.Handler, without importing those packages.
// Every error fails open: no rewrite.
package route
