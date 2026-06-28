// Task-ID context propagation. The runtime derives a task-scoped context per
// task (File 04 §4.6) and attaches the task ID so ports that need it to
// publish events (the Cognitive Core publishes TokenEvent{Task}, File 07
// §7.2.2) can retrieve it without the port interface carrying a task ID. The
// spec's Think(ctx, msgs) signature has no task parameter, so threading the ID
// via context keeps that signature intact while letting events carry the right
// Task. Both runtime and cognitive import session, so this is the neutral home
// for the helpers.

package session

import "context"

// ctxKey is an unexported context key type so collisions are impossible.
type ctxKey int

const taskIDKey ctxKey = 1

// WithTaskID returns a copy of ctx carrying the task ID. Callers that need the
// ID downstream (the runtime's task-scoped context) attach it once; readers
// retrieve it via TaskIDFromContext.
func WithTaskID(ctx context.Context, id TaskID) context.Context {
	return context.WithValue(ctx, taskIDKey, id)
}

// TaskIDFromContext returns the task ID attached via WithTaskID, or "" if none
// is present (e.g. a unit test calling a port directly with a bare context —
// the caller then publishes events with an empty Task, which is benign).
func TaskIDFromContext(ctx context.Context) TaskID {
	if v, ok := ctx.Value(taskIDKey).(TaskID); ok {
		return v
	}
	return ""
}
