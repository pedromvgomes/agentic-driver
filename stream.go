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
	streamer, ok := d.provider.(Streamer)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrStreamUnsupported, d.descriptor.ID)
	}
	req, err := d.prepare(req)
	if err != nil {
		return nil, err
	}

	inv, err := streamer.StreamCommand(req)
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

		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 0, 64<<10), maxEventLine)

		for scanner.Scan() {
			line := scanner.Bytes()
			if len(line) == 0 {
				continue
			}

			event, err := streamer.ParseEvent(line)
			if err != nil {
				yield(Event{}, fmt.Errorf("%w: %s emitted an unreadable event: %w",
					ErrProviderUnavailable, d.descriptor.ID, err))
				return
			}
			if event.Kind == EventKindUnknown {
				continue
			}
			if !yield(event, nil) {
				return
			}
		}

		// Every remaining failure is reported through the same helper, so a
		// stream ends the way a Run does.
		reaped = true
		if err := d.streamEnd(parent, ctx, cmd.Wait(), scanner.Err(), stderr.Bytes(), timeout); err != nil {
			yield(Event{}, err)
		}
	}, nil
}

// streamEnd waits for the child and reports why the stream stopped, or nil if
// it simply finished.
//
// A non-zero exit is an error here, unlike in Run: there is no envelope left to
// parse, so nothing downstream can turn the code into a verdict.
func (d *Driver) streamEnd(parent, ctx context.Context, waitErr, scanErr error, stderr []byte, timeout time.Duration) error {
	switch {
	case parent.Err() != nil:
		return fmt.Errorf("%w: %s was cancelled: %w", ErrProviderUnavailable, d.descriptor.ID, parent.Err())
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
	return nil
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
