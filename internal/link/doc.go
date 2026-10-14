// Package link is the edge-brain socket of stage 2 (plan decision M7). The
// edge holds the provider port and asks the brain, over a 0600 unix socket,
// for each request's rewrite and reports each response's usage. A frame is a
// 4-byte length, a JSON header line and the raw request body; bodies never
// leave the machine. The client fails open: a brain that is down, slow or
// gone mid-call means "forward unchanged".
package link
