package setup

import (
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/alexei-led/codex-router/internal/proxy"
)

// Marker lines around every block setup adds to config.toml (plan M10).
// RemoveBlocks deletes exactly these lines and what is between them.
const (
	blockBegin = "# >>> codex-router >>>"
	// blockBeginNL starts a block appended to a file that did not end in a
	// newline; removing the block removes the newline setup added too.
	blockBeginNL = blockBegin + " (newline added)"
	blockEnd     = "# <<< codex-router <<<"
)

// Provider is the router provider setup writes into a home's config.toml.
type Provider struct {
	Port  int
	Token string
}

func (p Provider) baseURL() string {
	return "http://" + proxy.LoopbackHost + ":" + strconv.Itoa(p.Port) + "/v1"
}

// selectBlock picks the router. It must come before the first table.
func (p Provider) selectBlock() string {
	return blockBegin + "\n" + `model_provider = "` + proxy.ProviderID + `"` + "\n" + blockEnd + "\n"
}

// tableBlock defines the router provider. The token goes in http_headers,
// not env_http_headers: the home's shared daemon does not inherit our env
// (plan M9). requires_openai_auth makes Codex send its own ChatGPT auth,
// which the edge forwards upstream only.
func (p Provider) tableBlock(begin string) string {
	return begin + "\n" +
		"[model_providers." + proxy.ProviderID + "]\n" +
		`name = "` + proxy.ProviderName + `"` + "\n" +
		`base_url = "` + p.baseURL() + `"` + "\n" +
		`wire_api = "responses"` + "\n" +
		"requires_openai_auth = true\n" +
		`http_headers = { "` + proxy.HeaderToken + `" = "` + p.Token + `" }` + "\n" +
		blockEnd + "\n"
}

// AddProvider returns src with the router selected as model_provider and its
// provider table appended, both in marked blocks; blocks from an earlier
// setup are replaced. The rest of the file, comments included, stays byte
// for byte. It refuses a file that does not parse, sets model_provider
// itself, or already defines the router provider. The result is checked
// with a TOML parser: it must select the router and decode to the same
// settings as src otherwise.
func AddProvider(src []byte, p Provider) ([]byte, error) {
	base, _, err := RemoveBlocks(src)
	if err != nil {
		return nil, err
	}
	old := map[string]any{}
	if err := toml.Unmarshal(base, &old); err != nil {
		return nil, fmt.Errorf("config.toml does not parse: %w", err)
	}
	if v, ok := old["model_provider"]; ok {
		return nil, fmt.Errorf("config.toml already sets model_provider = %q; remove it to route this home", fmt.Sprint(v))
	}
	if provs, ok := old["model_providers"].(map[string]any); ok {
		if _, ok := provs[proxy.ProviderID]; ok {
			return nil, fmt.Errorf("config.toml already defines model_providers.%s; remove it to route this home", proxy.ProviderID)
		}
	}
	lines := splitLines(string(base))
	// The select block goes before the first table. A line that looks like
	// a header may sit in a multi-line string or array, so each candidate is
	// checked, and the end of the file is the last one.
	var candidates []int
	for i, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), "[") {
			candidates = append(candidates, i)
		}
	}
	candidates = append(candidates, len(lines))
	for _, at := range candidates {
		out := insert(lines, at, p)
		if check(out, old, p) == nil {
			return out, nil
		}
	}
	return nil, errors.New("config.toml: found no place for model_provider before the first table; add the router provider by hand")
}

// insert puts the select block before lines[at] and appends the table
// block.
func insert(lines []string, at int, p Provider) []byte {
	var b strings.Builder
	for _, l := range lines[:at] {
		b.WriteString(l)
	}
	needNL := at == len(lines) && at > 0 && !strings.HasSuffix(lines[at-1], "\n")
	if needNL {
		b.WriteString("\n")
		b.WriteString(strings.Replace(p.selectBlock(), blockBegin, blockBeginNL, 1))
	} else {
		b.WriteString(p.selectBlock())
	}
	for _, l := range lines[at:] {
		b.WriteString(l)
	}
	begin := blockBegin
	if s := b.String(); !strings.HasSuffix(s, "\n") {
		b.WriteString("\n")
		begin = blockBeginNL
	}
	b.WriteString(p.tableBlock(begin))
	return []byte(b.String())
}

// check decodes out and requires the router selected with p's port and
// token, and every other setting equal to old.
func check(out []byte, old map[string]any, p Provider) error {
	got := map[string]any{}
	if err := toml.Unmarshal(out, &got); err != nil {
		return err
	}
	if got["model_provider"] != proxy.ProviderID {
		return errors.New("model_provider is not the router")
	}
	provs, _ := got["model_providers"].(map[string]any)
	prov, _ := provs[proxy.ProviderID].(map[string]any)
	headers, _ := prov["http_headers"].(map[string]any)
	if prov["base_url"] != p.baseURL() || headers[proxy.HeaderToken] != p.Token {
		return errors.New("router provider does not match")
	}
	delete(got, "model_provider")
	delete(provs, proxy.ProviderID)
	if _, ok := old["model_providers"]; !ok && len(provs) == 0 {
		delete(got, "model_providers")
	}
	if !reflect.DeepEqual(got, old) {
		return errors.New("the edit changes other settings")
	}
	return nil
}

// RemoveBlocks returns src without setup's marked blocks, and whether it had
// any. A block without its end marker is an error: the file was edited by
// hand and the block's extent is unknown.
func RemoveBlocks(src []byte) ([]byte, bool, error) {
	var out []byte
	in, found := false, false
	for _, l := range splitLines(string(src)) {
		t := strings.TrimRight(l, "\r\n")
		switch {
		case !in && (t == blockBegin || t == blockBeginNL):
			in, found = true, true
			if t == blockBeginNL && len(out) > 0 && out[len(out)-1] == '\n' {
				out = out[:len(out)-1]
			}
		case in:
			in = t != blockEnd
		default:
			out = append(out, l...)
		}
	}
	if in {
		return nil, true, errors.New("config.toml has a codex-router block without its end marker; fix it by hand")
	}
	return out, found, nil
}

// splitLines splits s after each newline; the last line may lack one.
func splitLines(s string) []string {
	lines := strings.SplitAfter(s, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// tokenValue matches the token in a provider's http_headers line.
var tokenValue = regexp.MustCompile(`("` + regexp.QuoteMeta(proxy.HeaderToken) + `"\s*=\s*")[^"]*(")`)

// Diff returns the line changes from a to b as hunks: a "@@ -i +j @@"
// header (1-based lines), then "-" and "+" lines. Token values are
// redacted. Setup only inserts and removes whole blocks, so a greedy
// resync is enough: it always yields a correct edit, and a minimal one for
// block edits.
func Diff(a, b []byte) string {
	al, bl := splitLines(string(a)), splitLines(string(b))
	var out strings.Builder
	i, j := 0, 0
	for i < len(al) || j < len(bl) {
		if i < len(al) && j < len(bl) && al[i] == bl[j] {
			i, j = i+1, j+1
			continue
		}
		del, ins := resync(al[i:], bl[j:])
		fmt.Fprintf(&out, "@@ -%d +%d @@\n", i+1, j+1)
		for _, l := range al[i : i+del] {
			out.WriteString(diffLine("-", l))
		}
		for _, l := range bl[j : j+ins] {
			out.WriteString(diffLine("+", l))
		}
		i, j = i+del, j+ins
	}
	return out.String()
}

// resync returns how many lines of a to delete and of b to insert before
// the two match again: the smallest one-sided step, else everything.
func resync(a, b []string) (del, ins int) {
	for k := 1; k <= max(len(a), len(b)); k++ {
		if len(a) > 0 && k < len(b) && b[k] == a[0] {
			return 0, k
		}
		if len(b) > 0 && k < len(a) && a[k] == b[0] {
			return k, 0
		}
	}
	return len(a), len(b)
}

func diffLine(sign, l string) string {
	l = tokenValue.ReplaceAllString(strings.TrimRight(l, "\r\n"), "${1}<redacted>${2}")
	return sign + " " + l + "\n"
}
