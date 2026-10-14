package proxy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/alexei-led/codex-router/internal/catalog"
	"github.com/alexei-led/codex-router/internal/responses"
)

// maxCatalogBytes caps how much of a /models body is buffered to parse the
// catalog; a larger body is still relayed, just not parsed.
const maxCatalogBytes = 8 << 20

// Stages a stream can abort in, for the stream-aborted log line.
const (
	stageClient   = "client"
	stageUpstream = "upstream"
)

// hopHeaders are connection-scoped and never cross the proxy (RFC 9110 §7.6.1).
var hopHeaders = []string{
	"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate",
	"Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade",
}

func (s *Server) handleResponses(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	if r.ContentLength > MaxBodyBytes {
		s.log.Warn("body-too-large", "bytes", r.ContentLength)
		http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxBodyBytes))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			s.log.Warn("body-too-large", "bytes", tooBig.Limit)
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		s.log.Warn("body-read-failed", "err", err)
		http.Error(w, "cannot read request body", http.StatusBadRequest)
		return
	}
	meta := responses.ParseMeta(r.Header)
	ids := []any{"thread", meta.ThreadID, "turn", meta.TurnID}

	resp, ok := s.roundTrip(w, r, "/responses", s.rewrite(r.Context(), body, meta), ids)
	if !ok {
		return
	}
	defer func() { _ = resp.Body.Close() }()

	var tee func([]byte)
	completed := false
	if resp.StatusCode/100 == 2 {
		sse := &sseSplitter{onEvent: func(ev []byte) {
			if u, served, ok := responses.UsageFromSSE(ev); ok {
				completed = true
				s.observe(meta, served, u, resp.StatusCode)
			}
		}}
		tee = sse.write
	} else {
		s.observe(meta, "", responses.Usage{}, resp.StatusCode)
	}
	s.relay(w, r, resp, resp.Body, tee, ids, func() bool { return completed })
	s.log.Debug("responses", append(ids, "status", resp.StatusCode, "bytes", len(body), "ms", time.Since(start).Milliseconds())...)
}

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	resp, ok := s.roundTrip(w, r, "/models", nil, nil)
	if !ok {
		return
	}
	defer func() { _ = resp.Body.Close() }()

	var body io.Reader = resp.Body
	if resp.StatusCode == http.StatusOK && s.opts.OnCatalog != nil {
		head, err := io.ReadAll(io.LimitReader(resp.Body, maxCatalogBytes+1))
		if err != nil {
			s.log.Warn("upstream-error", "path", PathModels, "err", err)
			http.Error(w, "upstream unavailable", http.StatusBadGateway)
			return
		}
		if len(head) <= maxCatalogBytes {
			s.setCatalog(head, resp.Header.Get("ETag"))
		}
		body = io.MultiReader(bytes.NewReader(head), resp.Body)
	}
	s.relay(w, r, resp, body, nil, nil, nil)
	s.log.Debug("models", "status", resp.StatusCode)
}

// rewrite returns the body to send upstream: the Decider's rewrite, or the
// original bytes on any parse, decision or rewrite failure.
func (s *Server) rewrite(ctx context.Context, body []byte, meta responses.Meta) []byte {
	ids := []any{"thread", meta.ThreadID, "turn", meta.TurnID}
	req, err := responses.Parse(body)
	if err != nil {
		s.log.Warn("parse-error", append(ids, "bytes", len(body))...)
		return body
	}
	if s.opts.Decider == nil {
		return body
	}
	rw, ok := s.decide(ctx, req, meta, ids)
	if !ok {
		return body
	}
	out, err := req.Apply(rw)
	if err != nil {
		s.log.Warn("rewrite-error", ids...)
		return body
	}
	s.log.Debug("rewrite", append(ids, "from", req.Model, "model", rw.Model, "effort", rw.Effort)...)
	return out
}

// decide runs the Decider under the decision deadline. A panic, a timeout or
// a cancelled request all mean: forward the original.
func (s *Server) decide(ctx context.Context, req *responses.Request, meta responses.Meta, ids []any) (responses.Rewrite, bool) {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	type result struct {
		rw responses.Rewrite
		ok bool
	}
	done := make(chan result, 1)
	go func() {
		defer func() {
			if p := recover(); p != nil {
				s.log.Error("decide-panic", append(ids, "type", fmt.Sprintf("%T", p))...)
				done <- result{}
			}
		}()
		rw, ok := s.opts.Decider.Decide(ctx, req, meta)
		done <- result{rw, ok}
	}()
	select {
	case res := <-done:
		return res.rw, res.ok
	case <-ctx.Done():
		s.log.Warn("decide-timeout", append(ids, "ms", s.timeout.Milliseconds())...)
		return responses.Rewrite{}, false
	}
}

// roundTrip sends one upstream request. On failure it answers the client
// (502, or nothing if the client is gone) and returns false. The request is
// never re-sent, not even on a stale keep-alive connection: that case is a
// 502 too, and Codex retries on its own (research §3.8).
func (s *Server) roundTrip(w http.ResponseWriter, r *http.Request, suffix string, body []byte, ids []any) (*http.Response, bool) {
	u := *s.upstream
	u.Path = strings.TrimSuffix(u.Path, "/") + suffix
	u.RawPath = ""
	u.RawQuery = r.URL.RawQuery
	out, err := http.NewRequestWithContext(r.Context(), r.Method, u.String(), nil)
	if err != nil {
		s.log.Error("upstream-request", append(ids, "err", err)...)
		http.Error(w, "bad upstream request", http.StatusInternalServerError)
		return nil, false
	}
	// A request with a nil or NoBody body (every GET, an empty POST with an
	// Idempotency-Key) or with GetBody counts as replayable, and the transport
	// re-sends it when a reused connection dies after the request was written.
	// A non-nil Body without GetBody is never replayed. An empty GET body is
	// still sent as no body at all.
	out.Body = io.NopCloser(bytes.NewReader(body))
	out.ContentLength = int64(len(body))
	out.Header = withoutHop(r.Header)
	out.Header.Del("Content-Length")
	out.Header.Del(HeaderToken)
	// The tee and catalog parse need plain bytes; see upstreamClient.
	out.Header.Del("Accept-Encoding")

	resp, err := s.client.Do(out)
	if err != nil {
		if r.Context().Err() != nil {
			s.log.Warn("stream-aborted", append(ids, "path", r.URL.Path, "stage", stageClient)...)
			return nil, false
		}
		s.log.Warn("upstream-error", append(ids, "path", r.URL.Path, "err", cause(err))...)
		http.Error(w, "upstream unavailable", http.StatusBadGateway)
		return nil, false
	}
	// http.Client hands back a 307/308 to a POST without GetBody instead of
	// asking CheckRedirect, so refuse every redirect status here too.
	if isRedirect(resp.StatusCode) {
		_ = resp.Body.Close()
		s.log.Warn("upstream-error", append(ids, "path", r.URL.Path, "err", errRedirect, "status", resp.StatusCode)...)
		http.Error(w, "upstream unavailable", http.StatusBadGateway)
		return nil, false
	}
	return resp, true
}

func isRedirect(status int) bool {
	return status/100 == 3 && status != http.StatusNotModified
}

// cause drops url.Error's method and URL: a refused redirect's URL comes from
// upstream and may carry anything.
func cause(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

// relay writes status and headers (minus hop-by-hop and served-model
// headers) and streams body with a flush per chunk. tee sees each chunk just
// before the client does, so usage is observed before Codex can send its
// next step. An upstream failure mid-stream aborts the client
// connection so Codex sees a broken stream, not a clean end. Codex closes the
// connection right after response.completed, so a client close once completed
// reports true is a normal end, not an abort.
func (s *Server) relay(w http.ResponseWriter, r *http.Request, resp *http.Response, body io.Reader, tee func([]byte), ids []any, completed func() bool) {
	h := w.Header()
	for k, v := range withoutHop(resp.Header) {
		h[k] = v
	}
	h.Del(responses.HeaderModel)
	h.Del(responses.HeaderXModel)
	w.WriteHeader(resp.StatusCode)
	rc := http.NewResponseController(w)
	_ = rc.Flush()

	stage, err := copyFlush(w, rc, body, tee)
	if err == nil {
		return
	}
	if r.Context().Err() != nil {
		stage = stageClient
	}
	if stage == stageClient && completed != nil && completed() {
		s.log.Debug("stream-closed-after-completed", append(ids, "path", r.URL.Path)...)
		return
	}
	s.log.Warn("stream-aborted", append(ids, "path", r.URL.Path, "stage", stage)...)
	if stage == stageUpstream {
		panic(http.ErrAbortHandler)
	}
}

func copyFlush(w io.Writer, rc *http.ResponseController, body io.Reader, tee func([]byte)) (string, error) {
	buf := make([]byte, 32<<10)
	for {
		n, rerr := body.Read(buf)
		if n > 0 {
			if tee != nil {
				tee(buf[:n])
			}
			if _, err := w.Write(buf[:n]); err != nil {
				return stageClient, err
			}
			if err := rc.Flush(); err != nil {
				return stageClient, err
			}
		}
		if errors.Is(rerr, io.EOF) {
			return "", nil
		}
		if rerr != nil {
			return stageUpstream, rerr
		}
	}
}

func withoutHop(h http.Header) http.Header {
	out := h.Clone()
	if out == nil {
		return http.Header{}
	}
	for _, v := range h.Values("Connection") {
		for name := range strings.SplitSeq(v, ",") {
			out.Del(strings.TrimSpace(name))
		}
	}
	for _, k := range hopHeaders {
		out.Del(k)
	}
	return out
}

func (s *Server) observe(meta responses.Meta, served string, u responses.Usage, status int) {
	if s.opts.Observer == nil {
		return
	}
	defer func() {
		if p := recover(); p != nil {
			s.log.Error("observe-panic", "thread", meta.ThreadID, "turn", meta.TurnID, "type", fmt.Sprintf("%T", p))
		}
	}()
	s.opts.Observer.Observe(meta, served, u, status)
}

func (s *Server) setCatalog(body []byte, etag string) {
	c, err := catalog.Parse(body)
	if err != nil {
		s.log.Warn("catalog-parse-error", "bytes", len(body))
		return
	}
	c.ETag, c.FetchedAt = etag, time.Now()
	defer func() {
		if p := recover(); p != nil {
			s.log.Error("catalog-panic", "type", fmt.Sprintf("%T", p))
		}
	}()
	s.opts.OnCatalog(c)
	s.log.Debug("catalog", "models", len(c.Models))
}
