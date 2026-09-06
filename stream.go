package agentic

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"iter"
	"time"
)

// maxEventLine bounds one line of the event stream. A CLI that emits a whole
// file's contents as a single tool result would otherwise let the child decide
// how much memory the caller spends.
const maxEventLine = 4 << 20 // 4 MiB

// Stream runs a request and yields events as they arrive.
//
// The sequence ends after the first non-nil error, and always ends: the caller
// stopping early cancels the child. A line the provider does not model is
// skipped rather than yielded as an error, so a CLI adding an event type does
// not break a run that is otherwise working.
//
// The terminal EventKindResult carries the same Result a non-streaming Run
// would have produced.
func (d *Driver) Stream(ctx context.Context, req Request) (iter.Seq2[Event, error], error) {
	req, inv, err := d.invocation(req)
	if err != nil {
		return nil, err
	}
	env, err := d.buildEnv(inv)
	if err != nil {
		return nil, err
	}

	return func(yield func(Event, error) bool) {
		timeout := d.timeout
		if req.Timeout > 0 {
			timeout = req.Timeout
		}

		// The parent is kept alongside the derived context for the same reason
		// as in Run: only the pair distinguishes a hung CLI from a caller that
		// went away.
		parent := ctx
		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()

		cmd := d.command(ctx, inv.Args, env, req.WorkDir)

		stdout, err := cmd.StdoutPipe()
		if err != nil {
			yield(Event{}, fmt.Errorf("%w: %s: %w", ErrProviderUnavailable, d.descriptor.ID, err))
			return
		}
		// stderr is collected rather than streamed: it is diagnostic material
		// for whatever error ends the run, and it is redacted before it is
		// used.
		stderr := boundedBuffer{limit: maxStderrCapture}
		cmd.Stderr = &stderr

		if err := cmd.Start(); err != nil {
			// A missing or unrunnable binary is the common case here, and
			// "fork/exec …: no such file or directory" does not say what to do
			// about it. Checked only on the failure path, so a healthy run pays
			// nothing for it.
			if ready := d.Ready(); ready != nil {
				yield(Event{}, ready)
				return
			}
			yield(Event{}, fmt.Errorf("%w: %s could not be run: %w", ErrProviderUnavailable, d.descriptor.ID, err))
			return
		}
		// Reached on every path out, including the caller abandoning the
		// sequence mid-iteration. Cancelling the context is what kills the
		// process group; Wait then reaps it. The flag keeps this from being a
		// second Wait on the path that already waited to learn how the run
		// ended.
		reaped := false
		defer func() {
			cancel()
			if !reaped {
				_ = cmd.Wait()
			}
		}()

		// One decoder for one run, built from the request that run answers.
		// Its state is the fold that becomes the terminal Result, so it must
		// not be shared with any other invocation.
		decoder := d.provider.NewDecoder(req)

		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 0, 64<<10), maxEventLine)

		var decodeErr error
		for scanner.Scan() {
			line := scanner.Bytes()
			if len(line) == 0 {
				continue
			}

			event, err := decoder.Decode(line)
			if err != nil {
				// Recorded and reported after the child is reaped, so the CLI's
				// own stderr can be carried with it: a CLI that explains itself
				// before emitting something unreadable is explaining the very
				// thing being diagnosed. Reading the stderr buffer while the
				// process is still writing to it would be a race.
				decodeErr = err
				break
			}
			if event.Kind == EventKindUnknown {
				continue
			}
			if !yield(event, nil) {
				return
			}
		}

		// Cancelled before the wait, because abandoning stdout part-way leaves
		// a child that fills the pipe and blocks forever.
		if decodeErr != nil {
			cancel()
		}
		reaped = true
		waitErr := cmd.Wait()

		if decodeErr != nil {
			yield(Event{}, fmt.Errorf("%w: %s emitted an unreadable event: %w%s",
				ErrProviderUnavailable, d.descriptor.ID, decodeErr, d.suffix(stderr.Bytes())))
			return
		}

		// Every remaining failure is reported through the same helper, so a
		// stream ends the way a Run does.
		result, complete := decoder.Result()
		if err := d.streamEnd(parent, ctx, waitErr, scanner.Err(), stderr.Bytes(), timeout, complete); err != nil {
			yield(Event{}, err)
			return
		}

		// Built here rather than by the decoder, so exactly one thing in the
		// library decides what a run finally said and Run cannot disagree with
		// the stream it folded.
		yield(Event{Kind: EventKindResult, Text: result.Text, Result: result}, nil)
	}, nil
}

// streamEnd waits for the child and reports why the stream stopped, or nil if
// it simply finished.
//
// complete says the decoder reached a terminal outcome, and it outranks
// everything except a caller who went away. A CLI reports a rejected credential
// or an unsupported model by finishing its stream properly and THEN exiting
// non-zero; judging the exit code first turns those verdicts into spurious
// outages. A complete result outranks a TIMEOUT for the same reason it does in
// Run: the answer arrived and was paid for, and a CLI that flushes its last
// event and then hangs holding stdout open has stalled in its teardown, not in
// the work.
func (d *Driver) streamEnd(parent, ctx context.Context, waitErr, scanErr error, stderr []byte, timeout time.Duration, complete bool) error {
	switch {
	case parent.Err() != nil:
		// Never reported as a success. Everything else here is a question of
		// what came back; this is a question of whether anyone is still
		// listening.
		return fmt.Errorf("%w: %s was cancelled: %w", ErrProviderUnavailable, d.descriptor.ID, parent.Err())
	case complete:
		return nil
	case ctx.Err() != nil:
		return fmt.Errorf("%w: %s did not finish within %s%s",
			ErrProviderUnavailable, d.descriptor.ID, timeout, d.suffix(stderr))
	case scanErr != nil && !errors.Is(scanErr, io.EOF):
		return fmt.Errorf("%w: %s: reading the event stream: %w%s",
			ErrProviderUnavailable, d.descriptor.ID, scanErr, d.suffix(stderr))
	case waitErr != nil:
		return fmt.Errorf("%w: %s exited before the stream ended: %w%s",
			ErrProviderUnavailable, d.descriptor.ID, waitErr, d.suffix(stderr))
	}

	// A clean exit that never said how the turn ended. Positive evidence of a
	// terminal outcome is required, because the alternative is reporting a run
	// that produced no answer whatsoever as a successful empty one. A usage
	// error takes this path when the CLI rejects a flag and exits zero, and so
	// does a stream truncated at exactly a line boundary.
	return fmt.Errorf("%w: %s ended without a result%s",
		ErrProviderUnavailable, d.descriptor.ID, d.suffix(stderr))
}

// boundedBuffer collects output up to a limit and then discards, so a child
// that writes without end cannot exhaust the parent's memory while the parent
// waits for it.
type boundedBuffer struct {
	limit int
	buf   []byte
}

const (
	// maxStderrCapture bounds diagnostic output. Everything kept from stderr is
	// destined for an error message, which is truncated far below this anyway.
	maxStderrCapture = 64 << 10
	// maxStdoutCapture bounds the result envelope. Generous, because the answer
	// itself is in there and truncating it would corrupt a valid result rather
	// than protect anything.
	maxStdoutCapture = 32 << 20
)

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if room := b.limit - len(b.buf); room > 0 {
		if len(p) < room {
			room = len(p)
		}
		b.buf = append(b.buf, p[:room]...)
	}
	// The full length is reported: the child is not writing to a broken pipe,
	// it is writing to a reader that has stopped keeping everything.
	return len(p), nil
}

func (b *boundedBuffer) Bytes() []byte { return b.buf }
