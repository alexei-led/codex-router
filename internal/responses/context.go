package responses

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"hash"
)

// Context identifies a measured input prefix without retaining its text.
// The zero value cannot be used as a measurement anchor.
type Context struct {
	envelope [sha256.Size]byte
	prefix   [sha256.Size]byte
	items    int
	bytes    int
	valid    bool
}

func hashPart(h hash.Hash, b []byte) {
	var size [8]byte
	binary.LittleEndian.PutUint64(size[:], uint64(len(b)))
	_, _ = h.Write(size[:])
	_, _ = h.Write(b)
}

func (r *Request) readContext(top []member, items []json.RawMessage) {
	h := sha256.New()
	for _, m := range top {
		if m.key == keyInput || m.key == keyModel {
			continue
		}
		hashPart(h, []byte(m.key))
		// Only effort is routing-owned; other reasoning controls must match.
		if m.key == keyReasoning && r.raw[m.start] == '{' {
			fields, err := members(r.raw, m.start)
			if err != nil {
				return
			}
			for _, field := range fields {
				if field.key != keyEffort {
					hashPart(h, []byte(field.key))
					hashPart(h, r.raw[field.start:field.end])
				}
			}
		} else {
			hashPart(h, r.raw[m.start:m.end])
		}
	}
	var c Context
	copy(c.envelope[:], h.Sum(nil))
	h.Reset()
	c.valid = true
	copy(c.prefix[:], h.Sum(nil))
	r.contexts = append(r.contexts, c)
	for _, item := range items {
		hashPart(h, item)
		c.items++
		c.bytes += len(item)
		copy(c.prefix[:], h.Sum(nil))
		r.contexts = append(r.contexts, c)
	}
}

// Context returns the entire parsed input's shape. Synthetic requests without
// a raw body have no trustworthy shape and retain the full-body estimate.
func (r *Request) Context() Context {
	if len(r.contexts) == 0 {
		return Context{}
	}
	return r.contexts[len(r.contexts)-1]
}

// AddedTokens estimates appended items only when the measured prefix and all
// non-routing request fields are unchanged. Unknown fields are included.
func (r *Request) AddedTokens(prior Context) (int, bool) {
	if !prior.valid || prior.items >= len(r.contexts) || r.contexts[prior.items] != prior {
		return 0, false
	}
	added := r.Context().bytes - prior.bytes
	return (added + 3) / 4, true
}
