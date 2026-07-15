// Package audit records a JSONL log of MCP and connector events to
// disk and offers a recent-events reader for the admin console.
//
// The log is intentionally append-only and lock-free (writes go
// through a buffered channel + a single goroutine). This keeps the
// hot path (an MCP call) cheap even when the disk is slow.
//
// On disk the log lives under data_dir/audit/YYYY-MM-DD.jsonl and
// rolls over at midnight UTC. Old files are never deleted by the
// platform — operators can ship them to object storage.
package audit

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/processcrash/egmcp/internal/log"
	"go.uber.org/zap"
)

// Event is a single audit row. Field tags are snake_case for
// readability when the file is tailed by humans.
type Event struct {
	Timestamp time.Time      `json:"ts"`
	Kind      string         `json:"kind"` // "mcp.tool" | "mcp.initialize" | "admin" | "auth"
	Instance  string         `json:"instance,omitempty"`
	Connector string         `json:"connector,omitempty"`
	Tool      string         `json:"tool,omitempty"`
	Status    string         `json:"status"` // "ok" | "error"
	LatencyMS int64          `json:"latency_ms"`
	SourceIP  string         `json:"source_ip,omitempty"`
	UserAgent string         `json:"user_agent,omitempty"`
	Error     string         `json:"error,omitempty"`
	Extras    map[string]any `json:"extras,omitempty"`
}

// Recorder writes events to disk in the background.
type Recorder struct {
	dir   string
	ch    chan Event
	done  chan struct{}
	wg    sync.WaitGroup
	zap   *zap.Logger
}

// NewRecorder starts a background goroutine that drains the in-memory
// channel and writes JSONL records. It returns a Recorder whose Emit
// method is non-blocking: events are dropped with a warning if the
// channel is full.
func NewRecorder(dir string, logger *zap.Logger) (*Recorder, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("audit: mkdir: %w", err)
	}
	r := &Recorder{
		dir:  dir,
		ch:   make(chan Event, 1024),
		done: make(chan struct{}),
		zap:  logger,
	}
	r.wg.Add(1)
	go r.loop()
	return r, nil
}

// Emit queues an event for asynchronous writing. Safe for
// concurrent callers. The argument is taken by pointer so the
// caller can observe the Timestamp we stamp before queueing.
func (r *Recorder) Emit(e *Event) {
	if e.Timestamp.IsZero() {
		e.Timestamp = time.Now().UTC()
	}
	select {
	case r.ch <- *e:
	default:
		// Best-effort drop — never block the hot path.
		if r.zap != nil {
			r.zap.Warn("audit: dropped event (queue full)", log.String("kind", e.Kind))
		}
	}
}

// Close drains the queue and stops the writer goroutine.
func (r *Recorder) Close() error {
	close(r.ch)
	r.wg.Wait()
	return nil
}

// Recent returns the most recent n events across today's log. It is
// used by the admin API; performance is intentionally bounded
// because n is always a small user-supplied number.
func (r *Recorder) Recent(n int) ([]Event, error) {
	if n <= 0 {
		return nil, nil
	}
	files, err := latestLogFiles(r.dir, 3)
	if err != nil {
		return nil, err
	}
	out := make([]Event, 0, n)
	for _, path := range files {
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			var e Event
			if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
				continue
			}
			out = append(out, e)
		}
		_ = f.Close()
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Timestamp.After(out[j].Timestamp)
	})
	if len(out) > n {
		out = out[:n]
	}
	return out, nil
}

// ─────────────────────────────────────────────────────────────────────
// background writer
// ─────────────────────────────────────────────────────────────────────

func (r *Recorder) loop() {
	defer r.wg.Done()

	type openFile struct {
		day   string
		path  string
		f     *os.File
		w     *bufio.Writer
		flush chan struct{}
	}
	current := (*openFile)(nil)
	flushTick := time.NewTicker(2 * time.Second)
	defer flushTick.Stop()

	open := func(day string) (*openFile, error) {
		path := filepath.Join(r.dir, day+".jsonl")
		f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return nil, err
		}
		return &openFile{
			day:   day,
			path:  path,
			f:     f,
			w:     bufio.NewWriter(f),
			flush: make(chan struct{}, 1),
		}, nil
	}
	close := func(of *openFile) {
		if of == nil {
			return
		}
		_ = of.w.Flush()
		_ = of.f.Close()
	}

	for {
		select {
		case e, ok := <-r.ch:
			if !ok {
				close(current)
				return
			}
			day := e.Timestamp.UTC().Format("2006-01-02")
			if current == nil || current.day != day {
				close(current)
				f, err := open(day)
				if err != nil {
					if r.zap != nil {
						r.zap.Warn("audit: open file failed", log.Err(err))
					}
					continue
				}
				current = f
			}
			bs, err := json.Marshal(e)
			if err != nil {
				if r.zap != nil {
					r.zap.Warn("audit: marshal failed", log.Err(err))
				}
				continue
			}
			_, _ = current.w.Write(bs)
			_, _ = current.w.WriteString("\n")
		case <-flushTick.C:
			if current != nil {
				_ = current.w.Flush()
			}
		}
	}
}

// latestLogFiles returns the most recent day-stamped files, newest
// first. We only look at the last few days to keep Recent() cheap.
func latestLogFiles(dir string, maxFiles int) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	type pair struct {
		day string
		path string
	}
	var files []pair
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		day := strings.TrimSuffix(name, ".jsonl")
		files = append(files, pair{day: day, path: filepath.Join(dir, name)})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].day > files[j].day })
	if len(files) > maxFiles {
		files = files[:maxFiles]
	}
	out := make([]string, len(files))
	for i, f := range files {
		out[i] = f.path
	}
	return out, nil
}

// ReadTail returns the last n lines of today's log as raw strings —
// useful for `curl .../audit/tail`.
func (r *Recorder) ReadTail(n int) ([]string, error) {
	if n <= 0 {
		return nil, nil
	}
	today := filepath.Join(r.dir, time.Now().UTC().Format("2006-01-02")+".jsonl")
	f, err := os.Open(today)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var lines []string
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines, nil
}

// Shutdown is a thin alias for Close kept for clarity at the call
// site (the admin package calls Close; tests call Shutdown).
func (r *Recorder) Shutdown(ctx context.Context) error {
	_ = ctx
	return r.Close()
}