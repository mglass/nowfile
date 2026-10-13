package format

import "time"

// DocumentJSON is the structured form of a document, served as /{name}.json
// and by the write API's GET /api/v1/me. Both go through ToJSON so the two
// cannot drift. Timestamps are RFC 3339 strings, and Links and Slots encode
// as [] rather than null when empty.
type DocumentJSON struct {
	// Version is the format version, "0" for draft 1, or "" for a classic
	// plan.
	Version  string   `json:"version"`
	Name     string   `json:"name,omitempty"`
	Headline string   `json:"headline,omitempty"`
	Photo    string   `json:"photo,omitempty"`
	Links    []string `json:"links"`
	// Current is the one line a renderer shows where it has room for one,
	// by the precedence in docs/format.md (see Headline).
	Current string `json:"current,omitempty"`
	// Slots are in document order.
	Slots []SlotJSON `json:"slots"`
	Plan  string     `json:"plan,omitempty"`
}

// SlotJSON is one slot in DocumentJSON.
type SlotJSON struct {
	Key    string `json:"key"`
	Value  string `json:"value"`
	Set    string `json:"set,omitempty"`
	Until  string `json:"until,omitempty"`
	Source string `json:"source,omitempty"`
}

// ToJSON returns the structured form of doc. Like Render and Headline it
// does not drop expired slots; callers pass a composed document.
func ToJSON(doc Document) DocumentJSON {
	out := DocumentJSON{
		Version:  doc.Version,
		Name:     doc.Name,
		Headline: doc.Headline,
		Photo:    doc.Photo,
		Links:    append([]string{}, doc.Links...),
		Current:  Headline(doc),
		Slots:    make([]SlotJSON, 0, len(doc.Slots)),
		Plan:     doc.Plan,
	}
	for _, s := range doc.Slots {
		out.Slots = append(out.Slots, SlotJSON{
			Key:    s.Key,
			Value:  s.Value,
			Set:    jsonTime(s.Set),
			Until:  jsonTime(s.Until),
			Source: s.Source,
		})
	}
	return out
}

func jsonTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339)
}
