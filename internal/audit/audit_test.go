package audit

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
)

func TestRecorderEmitAndRead(t *testing.T) {
	dir := t.TempDir()
	r, err := NewRecorder(dir, zap.NewNop())
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}

	r.Emit(&Event{Kind: "mcp.tool", Instance: "alpha", Tool: "filesystem__read_file", Status: "ok"})
	r.Emit(&Event{Kind: "mcp.tool", Instance: "alpha", Tool: "filesystem__list_dir", Status: "error", Error: "boom"})

	// Drain the channel.
	if err := r.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Re-open to read.
	r2, err := NewRecorder(dir, zap.NewNop())
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	defer r2.Close()

	// ReadTail should see both events (they're in today's file).
	lines, err := r2.ReadTail(10)
	if err != nil {
		t.Fatalf("ReadTail: %v", err)
	}
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d: %s", len(lines), strings.Join(lines, "\n"))
	}
	var e Event
	if err := json.Unmarshal([]byte(lines[0]), &e); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if e.Tool != "filesystem__list_dir" && e.Tool != "filesystem__read_file" {
		t.Fatalf("unexpected tool: %q", e.Tool)
	}
}

func TestRecorderRecentSortsDescending(t *testing.T) {
	dir := t.TempDir()
	r, _ := NewRecorder(dir, zap.NewNop())

	base := time.Now().UTC()
	for i := 0; i < 5; i++ {
		r.Emit(&Event{
			Timestamp: base.Add(time.Duration(i) * time.Second),
			Kind:      "mcp.tool",
			Instance:  "alpha",
			Tool:      "t" + string(rune('a'+i)),
		})
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}

	r2, _ := NewRecorder(dir, zap.NewNop())
	defer r2.Close()
	events, err := r2.Recent(3)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("expected 3, got %d", len(events))
	}
	if events[0].Tool != "te" || events[1].Tool != "td" || events[2].Tool != "tc" {
		t.Fatalf("not descending: %+v", events)
	}
}

func TestReadTailEmptyDir(t *testing.T) {
	r, _ := NewRecorder(t.TempDir(), zap.NewNop())
	defer r.Close()
	lines, err := r.ReadTail(5)
	if err != nil {
		t.Fatalf("ReadTail: %v", err)
	}
	if lines != nil {
		t.Fatalf("expected nil, got %v", lines)
	}
}

func TestEmitTimestampZeroGetsNow(t *testing.T) {
	r, _ := NewRecorder(t.TempDir(), zap.NewNop())
	defer r.Close()
	e := Event{Kind: "x", Tool: "y"}
	if !e.Timestamp.IsZero() {
		t.Fatalf("expected zero, got %v", e.Timestamp)
	}
	r.Emit(&e)
	if e.Timestamp.IsZero() {
		t.Fatalf("Emit should set timestamp")
	}
}