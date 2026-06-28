// Package event — durability log (L3-004).
//
// The log is an append-only, fsync-per-Append file. It is the single source of
// truth for P4 (transparency): a debug session is `tail` on the log, a bug
// report is the log file, a test fixture is a recorded log. It is also the
// crash-recovery medium: because every Append fsyncs before the bus fans out
// (File 05 §5.3), a crash after the publishes leaves the same bytes on disk,
// and Replay reconstructs the event stream.
//
// The async ring-buffer optimization (File 05 §5.6, File 15 §10.5) is deferred;
// L3-004 implements the straightforward synchronous fsync-before-fanout.

package event

import (
	"encoding/json"
	"io"
	"os"
	"sync"
)

// appender is the durability seam the Bus calls before fan-out. The real Log
// implements it; tests inject fakes to assert ordering without touching disk.
type appender interface {
	Append(Envelope) error
	Close() error
}

// Log is an append-only, fsync-per-Append durability log (File 05 §5.3.1).
type Log struct {
	mu  sync.Mutex
	f   *os.File
	enc *json.Encoder
}

// OpenLog opens (creating if needed) an append-only durability log at path.
func OpenLog(path string) (*Log, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	return &Log{f: f, enc: json.NewEncoder(f)}, nil
}

// Append writes env as one JSON line and fsyncs, so the envelope is on disk
// before any subscriber sees it (durability before visibility).
func (l *Log) Append(env Envelope) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.enc.Encode(env); err != nil {
		return err
	}
	return l.f.Sync() // fsync; batchable in File 15 §10.5
}

// Close releases the underlying file. It does not add durability — each
// Append already fsynced — so closing is not required for crash safety.
func (l *Log) Close() error { return l.f.Close() }

// Replay reads a durability log from path and returns the envelopes in order.
// Used by crash recovery and by the golden-transcript harness (File 15
// §15.15.3).
func Replay(path string) ([]Envelope, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	dec := json.NewDecoder(f)
	var out []Envelope
	for {
		var env Envelope
		if err := dec.Decode(&env); err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}
		out = append(out, env)
	}
	return out, nil
}
