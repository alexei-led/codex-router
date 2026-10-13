package responses

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
)

// The scanner finds byte spans of object members so Apply can splice new
// values in without re-encoding anything else. It runs only on bodies that
// json.Valid accepted, so it checks bounds but not grammar.

var errMalformed = errors.New("responses: malformed JSON object")

// member is one object member: its decoded key and the span of its value.
type member struct {
	key        string
	start, end int
}

// members lists the members of the object whose '{' is at b[open].
func members(b []byte, open int) ([]member, error) {
	if open >= len(b) || b[open] != '{' {
		return nil, errMalformed
	}
	i := skipSpace(b, open+1)
	if i < len(b) && b[i] == '}' {
		return nil, nil
	}
	var out []member
	for {
		if i >= len(b) || b[i] != '"' {
			return nil, errMalformed
		}
		keyEnd, err := skipString(b, i)
		if err != nil {
			return nil, err
		}
		key, err := decodeKey(b[i:keyEnd])
		if err != nil {
			return nil, err
		}
		i = skipSpace(b, keyEnd)
		if i >= len(b) || b[i] != ':' {
			return nil, errMalformed
		}
		start := skipSpace(b, i+1)
		end, err := skipValue(b, start)
		if err != nil {
			return nil, err
		}
		out = append(out, member{key: key, start: start, end: end})
		i = skipSpace(b, end)
		if i >= len(b) {
			return nil, errMalformed
		}
		switch b[i] {
		case ',':
			i = skipSpace(b, i+1)
		case '}':
			return out, nil
		default:
			return nil, errMalformed
		}
	}
}

// lookup returns the last member named key, the one encoding/json would use.
func lookup(ms []member, key string) (member, bool) {
	for i := len(ms) - 1; i >= 0; i-- {
		if ms[i].key == key {
			return ms[i], true
		}
	}
	return member{}, false
}

func decodeKey(quoted []byte) (string, error) {
	if bytes.IndexByte(quoted, '\\') < 0 {
		return string(quoted[1 : len(quoted)-1]), nil
	}
	var key string
	err := json.Unmarshal(quoted, &key)
	return key, err
}

func skipSpace(b []byte, i int) int {
	for i < len(b) && (b[i] == ' ' || b[i] == '\t' || b[i] == '\n' || b[i] == '\r') {
		i++
	}
	return i
}

// skipString returns the index just past the string whose '"' is at b[i].
func skipString(b []byte, i int) (int, error) {
	for j := i + 1; j < len(b); j++ {
		switch b[j] {
		case '\\':
			j++
		case '"':
			return j + 1, nil
		}
	}
	return 0, errMalformed
}

// skipValue returns the index just past the value that starts at b[i].
func skipValue(b []byte, i int) (int, error) {
	if i >= len(b) {
		return 0, errMalformed
	}
	switch b[i] {
	case '"':
		return skipString(b, i)
	case '{', '[':
		depth := 0
		for i < len(b) {
			switch b[i] {
			case '"':
				end, err := skipString(b, i)
				if err != nil {
					return 0, err
				}
				i = end
				continue
			case '{', '[':
				depth++
			case '}', ']':
				depth--
				if depth == 0 {
					return i + 1, nil
				}
			}
			i++
		}
		return 0, errMalformed
	default:
		for i < len(b) && strings.IndexByte(",}] \t\r\n", b[i]) < 0 {
			i++
		}
		return i, nil
	}
}
