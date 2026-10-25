package audit

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestTruncateRespectsLimitAndUTF8(t *testing.T) {
	s, tr := Truncate("abc")
	if s != "abc" || tr != 0 {
		t.Fatal("short strings untouched")
	}
	long := strings.Repeat("中", MaxRequestObjectBytes) // 3 字节 × N
	s, tr = Truncate(long)
	if tr != 1 || len(s) > MaxRequestObjectBytes || !utf8.ValidString(s) {
		t.Fatalf("len=%d valid=%v tr=%d", len(s), utf8.ValidString(s), tr)
	}
}

func TestEventJSONShape(t *testing.T) {
	e := Event{TS: time.Date(2026, 10, 24, 6, 1, 58, 312e6, time.UTC), Source: SourceKube}
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if m["ts"] != "2026-10-24T06:01:58.312Z" {
		t.Fatalf("ts = %v", m["ts"])
	}
	if _, ok := m["groups"].([]any); !ok {
		t.Fatalf("groups must be [] not null: %v", m["groups"])
	}
	if _, ok := m["annotations"].(map[string]any); !ok {
		t.Fatalf("annotations must be {} not null: %v", m["annotations"])
	}
}
