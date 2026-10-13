package format

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestToJSON(t *testing.T) {
	doc, err := Parse(example)
	if err != nil {
		t.Fatal(err)
	}
	// 12:35 local: Listening (until 12:40) is live, Status still live.
	j := ToJSON(Compose(doc, ts(t, "2026-10-13T12:35:00-04:00")))
	if j.Version != "0" || j.Name != "Mike Glass" || len(j.Links) != 2 {
		t.Fatalf("static fields: %+v", j)
	}
	if j.Current != "Lunch, back at 1" {
		t.Errorf("Current = %q", j.Current)
	}
	if len(j.Slots) != 4 || j.Slots[0].Key != "Status" || j.Slots[3].Key != "Listening" {
		t.Fatalf("slots: %+v", j.Slots)
	}
	if got := j.Slots[0]; got.Set != "2026-10-13T12:04:10-04:00" || got.Until != "2026-10-13T13:00:00-04:00" || got.Source != "" {
		t.Errorf("Status slot: %+v", got)
	}
	if got := j.Slots[2]; got.Until != "" || got.Source != "" {
		t.Errorf("Focus slot: %+v", got)
	}
	if j.Plan != "Reply within a day. Text for anything urgent." {
		t.Errorf("Plan = %q", j.Plan)
	}
}

func TestToJSONDropsExpiredAfterCompose(t *testing.T) {
	doc, err := Parse(example)
	if err != nil {
		t.Fatal(err)
	}
	// 13:30 local: Status and Listening have expired; Calendar is current.
	j := ToJSON(Compose(doc, ts(t, "2026-10-13T13:30:00-04:00")))
	for _, s := range j.Slots {
		if s.Key == "Status" || s.Key == "Listening" {
			t.Errorf("expired slot %s emitted", s.Key)
		}
	}
	if j.Current != "Busy until 15:00" {
		t.Errorf("Current = %q", j.Current)
	}
}

func TestToJSONEmptyArrays(t *testing.T) {
	b, err := json.Marshal(ToJSON(Document{Version: CurrentVersion}))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if !strings.Contains(s, `"links":[]`) || !strings.Contains(s, `"slots":[]`) {
		t.Errorf("empty arrays must encode as []: %s", s)
	}
	if strings.Contains(s, "null") {
		t.Errorf("unexpected null: %s", s)
	}
}
