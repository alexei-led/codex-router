// Package proxy is the loopback Responses proxy: POST /v1/responses and GET
// /v1/models go to the fixed ChatGPT backend with no redirects; SSE streams
// through unchanged except for the stripped model headers. It asks a Decider
// for a rewrite before the upstream call and reports usage to an Observer
// after it. Authorization and ChatGPT-Account-ID are forwarded upstream only
// and never logged. Nothing is re-sent once upstream has started.
package proxy
