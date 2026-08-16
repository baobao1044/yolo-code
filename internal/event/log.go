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
//
// Secret redaction (SetLogRedactor) sits on Append rather than on any event
// producer: the log is the one sink every event reaches, so a hook here covers
// PatchAppliedEvent.Diff, ErrorEvent.Msg and every event type nobody has
// written yet, without each producer having to remember.

package event

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
)

// ErrTruncatedLog reports that the log's final record was cut short — the
// signature of a crash between write and fsync. Replay returns it alongside
// every record it did recover, so a caller can tell "the tail is torn, here is
// what survived" from "this log is unreadable".
var ErrTruncatedLog = errors.New("event: log ends in a truncated record")

// appender is the durability seam the Bus calls before fan-out. The real Log
// implements it; tests inject fakes to assert ordering without touching disk.
type appender interface {
	Append(Envelope) error
	Close() error
}

// Redactor masks secrets in a string. It is the seam the durability log uses to
// keep credentials out of the on-disk event stream. event is the bottom of the
// import matrix (§15.15.2) and cannot import infra, so the concrete registry
// (infra.Secrets) is injected by the composition root via SetLogRedactor.
type Redactor interface {
	Redact(string) string
}

// The installed redactor is process-wide rather than per-Log because nothing
// outside package event ever holds a *Log: Bus.Open constructs one internally
// and never exposes it, so a per-instance setter would be unreachable from the
// root. This mirrors infra.DefaultRedactor's own rationale (secrets.go) — the
// boundaries are wired at different points in the root's startup, so a single
// process-wide value is what makes redaction on by default at all of them.
var (
	redactMu  sync.RWMutex
	redactSet Redactor
)

// SetLogRedactor installs the redactor every subsequent Append consults. Nil
// (or a call that never happens) means no redaction — the historical behaviour,
// kept so packages with no infra dependency, and event's own tests, still get a
// working log. Insisting on a non-nil redactor is the composition root's job,
// not this package's: cmd/yolo panics rather than start with none, the same way
// it does for the exec boundary. Safe to call concurrently with Append.
func SetLogRedactor(r Redactor) {
	redactMu.Lock()
	redactSet = r
	redactMu.Unlock()
}

// logRedactor returns the installed redactor, or nil.
func logRedactor() Redactor {
	redactMu.RLock()
	defer redactMu.RUnlock()
	return redactSet
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
// before any subscriber sees it (durability before visibility). With a redactor
// installed the payload is masked first (redactedLine); with none the bytes are
// exactly what they always were.
func (l *Log) Append(env Envelope) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if r := logRedactor(); r != nil {
		line, err := redactedLine(env, r)
		if err != nil {
			return err
		}
		if _, err := l.f.Write(line); err != nil {
			return err
		}
		return l.f.Sync()
	}
	if err := l.enc.Encode(env); err != nil {
		return err
	}
	return l.f.Sync() // fsync; batchable in File 15 §10.5
}

// redactedLine renders env as one redacted JSON line, terminator included.
//
// The redaction is applied to the *decoded* string values inside the event
// payload, not to the marshaled line as text. Masking the text is simpler and
// was the obvious first move, but it is wrong twice over. It can corrupt the
// record: a pattern whose match ends inside a JSON escape ("…\" or "…\\")
// replaces half an escape sequence and the line stops parsing, which turns a
// leak into an unreadable log. And it can break replay outright: the envelope's
// "type" tag is a plain string on the same line, so a rule that happened to
// match a topic name would rewrite the tag and Replay would fail with "no
// factory registered". Decoding first confines redaction to payload values, and
// re-encoding puts the escaping back correctly whatever the replacement text
// contains.
//
// The seq/at/type header is left alone for the same reason: none of it is user
// or tool data, and all of it is load-bearing for Replay. Object *keys* are not
// redacted either — a key is a field name, and rewriting one would silently
// drop the field on the way back in.
//
// Round-trippability is preserved because the payload is re-encoded from the
// same JSON shape it was decoded from: Replay unmarshals it into the concrete
// event the topic registry supplies, and only string values changed. Numbers go
// through json.Number so no float reformatting creeps in.
func redactedLine(env Envelope, r Redactor) ([]byte, error) {
	raw, err := json.Marshal(env)
	if err != nil {
		return nil, err
	}
	var w wireEnvelope
	if err := json.Unmarshal(raw, &w); err != nil {
		return nil, err
	}
	if len(w.Evt) > 0 {
		payload, err := redactPayload(w.Evt, r)
		if err != nil {
			return nil, err
		}
		w.Evt = payload
	}
	out, err := json.Marshal(w)
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}

// redactPayload rewrites one event payload, masking every string value in it at
// any depth and leaving everything else byte-identical.
//
// It walks tokens rather than decoding into map[string]any and re-marshaling,
// which is the shorter version of the same idea but reorders object keys (Go
// sorts map keys) and reformats numbers. The log is read by humans — "a debug
// session is `tail` on the log" — and a redacted line whose fields have
// silently rearranged themselves next to an unredacted one from another build
// is a needless obstacle. UseNumber keeps numeric literals as written.
func redactPayload(raw json.RawMessage, r Redactor) (json.RawMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var out bytes.Buffer
	if err := redactValueTo(&out, dec, r); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// redactValueTo copies exactly one JSON value from dec to out, redacting string
// values. Object *keys* are copied verbatim: a key is a field name, and
// rewriting one would drop the field on the way back through Replay.
func redactValueTo(out *bytes.Buffer, dec *json.Decoder, r Redactor) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			out.WriteByte('{')
			for first := true; dec.More(); first = false {
				if !first {
					out.WriteByte(',')
				}
				key, err := dec.Token()
				if err != nil {
					return err
				}
				name, ok := key.(string)
				if !ok {
					return fmt.Errorf("event: redact: object key is %T, not a string", key)
				}
				writeJSONString(out, name)
				out.WriteByte(':')
				if err := redactValueTo(out, dec, r); err != nil {
					return err
				}
			}
			if _, err := dec.Token(); err != nil { // the closing '}'
				return err
			}
			out.WriteByte('}')
		case '[':
			out.WriteByte('[')
			for first := true; dec.More(); first = false {
				if !first {
					out.WriteByte(',')
				}
				if err := redactValueTo(out, dec, r); err != nil {
					return err
				}
			}
			if _, err := dec.Token(); err != nil { // the closing ']'
				return err
			}
			out.WriteByte(']')
		default:
			return fmt.Errorf("event: redact: unexpected delimiter %q", t)
		}
	case string:
		writeJSONString(out, r.Redact(t))
	case json.Number:
		out.WriteString(t.String())
	case bool:
		if t {
			out.WriteString("true")
		} else {
			out.WriteString("false")
		}
	case nil:
		out.WriteString("null")
	default:
		return fmt.Errorf("event: redact: unexpected token %T", tok)
	}
	return nil
}

// writeJSONString appends s as a JSON string literal, escaped the way
// encoding/json escapes it by default (HTML-safe), so the re-emitted payload
// matches what Marshal would have produced. Marshaling a string cannot fail.
func writeJSONString(out *bytes.Buffer, s string) {
	b, _ := json.Marshal(s)
	out.Write(b)
}

// Close releases the underlying file. It does not add durability — each
// Append already fsynced — so closing is not required for crash safety.
func (l *Log) Close() error { return l.f.Close() }

// Replay reads a durability log from path and returns the envelopes in order.
// Used by crash recovery and by the golden-transcript harness (File 15
// §15.15.3).
//
// A torn final record — the normal result of being killed between write and
// fsync, which is the case crash recovery exists for — yields the records that
// did survive plus ErrTruncatedLog, rather than discarding the whole recovery.
// Any other decode failure (an unregistered topic, corruption mid-file) is
// still a hard error: those are not something a crash produces.
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
			if errors.Is(err, io.EOF) {
				break
			}
			if errors.Is(err, io.ErrUnexpectedEOF) {
				return out, ErrTruncatedLog
			}
			return nil, err
		}
		out = append(out, env)
	}
	return out, nil
}
