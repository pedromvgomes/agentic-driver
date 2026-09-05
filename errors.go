package agentic

import "errors"

// The sentinels a caller matches with errors.Is. They divide every failure into
// the two answers a caller actually acts on: the CLI could not be run, or the
// CLI ran and said no.
var (
	// ErrProviderUnavailable means the invocation could not be carried out, or
	// could not be understood. A missing binary, a timeout, a crash and
	// unparseable output all land here: none of them is a statement about the
	// request or the credential.
	ErrProviderUnavailable = errors.New("provider unavailable")

	// ErrInvalidRequest means the provider refused to build a command from the
	// Request. No process is started.
	ErrInvalidRequest = errors.New("invalid request")

	// ErrIsolationUnsupported means Isolated credentials were asked of a
	// provider that does not implement Isolator, so there is no vocabulary for
	// constructing an environment that carries the token.
	ErrIsolationUnsupported = errors.New("provider does not support isolated credentials")

	// ErrResumeUnsupported means Request.SessionID was set on a provider that
	// does not implement Resumer. Silently dropping the field would start a
	// fresh session and answer as though the history had been read.
	ErrResumeUnsupported = errors.New("provider does not support resuming a session")

	// ErrAgentsUnsupported means Request.Agents was set on a provider that does
	// not implement AgentDefiner. Dropping the roster leaves a run that answers
	// the prompt itself instead of delegating, and says so nowhere.
	ErrAgentsUnsupported = errors.New("provider does not support defining agents")

	// ErrPermissionsUnsupported means Request.AllowedTools or
	// Request.PermissionMode was set on a provider that does not implement
	// Permitter. Dropping either widens what the run may do, so the silent
	// failure runs with more authority than was asked for, never less.
	ErrPermissionsUnsupported = errors.New("provider does not support scripted permissions")

	// ErrStreamUnsupported means Stream was called on a provider that does not
	// implement Streamer.
	ErrStreamUnsupported = errors.New("provider does not support streaming")

	// ErrInstallUnsupported means the provider vendors no binary, so there is
	// nothing for Install to fetch.
	ErrInstallUnsupported = errors.New("provider installs nothing")
)
