// Package agentictest builds a stand-in agent CLI, so the driver's process
// handling can be tested without a real one.
//
// It lives in a normal package rather than a _test file so every provider's
// tests can use the same fake, the way net/http/httptest is importable.
// Importing "testing" here is deliberate.
//
// The fake records its own argv, environment and working directory before
// answering. That is what makes the process policy testable: how a child is
// built is a set of statements about the child, and the only way to check them
// is to ask it.
package agentictest

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Fake is a scripted agent binary.
type Fake struct {
	// Stdout is printed verbatim.
	Stdout string
	// Stderr is written before exiting.
	Stderr string
	// ExitCode is the status it exits with. A verdict may arrive with a
	// non-zero exit, which is the case the driver has to keep the output for.
	ExitCode int
	// SleepSeconds is how long it stalls before printing anything, for testing
	// timeouts and cancellation. The stall happens first so a killed process
	// produces no output.
	SleepSeconds int
	// LingerSeconds is how long it stays alive AFTER printing, for testing a
	// caller that stops reading a stream part-way through. Output has to come
	// first there, or there is nothing to stop reading.
	LingerSeconds int
	// SpawnChild makes the fake start a background sleeper of its own, so a
	// cancellation can be checked to reach the whole process group rather than
	// just the process the driver started.
	SpawnChild bool

	dir        string
	path       string
	recordPath string
	childPath  string
}

// Build writes the fake to a temporary directory and returns it, ready to be
// named by WithBinary.
//
// The script is shell rather than a compiled helper because the properties
// under test are all about the process the driver constructs, and a shell
// script observes them exactly as a real CLI would while costing no build step.
func (f *Fake) Build(t *testing.T) *Fake {
	t.Helper()

	f.dir = t.TempDir()
	f.path = filepath.Join(f.dir, "fake-agent")
	f.recordPath = filepath.Join(f.dir, "invocation.json")
	f.childPath = filepath.Join(f.dir, "child.pid")

	var body strings.Builder
	body.WriteString("#!/bin/sh\n")
	// The warm-up path, first and cheap. See warm below for why it exists.
	body.WriteString("if [ \"$1\" = " + shellQuote(warmupArg) + " ]; then exit 0; fi\n")
	body.WriteString(record(f.recordPath))

	if f.SpawnChild {
		// A grandchild that outlives its parent unless the whole group is
		// signalled.
		body.WriteString("sh -c 'sleep 60' &\n")
		body.WriteString("echo $! > " + shellQuote(f.childPath) + "\n")
	}
	if f.SleepSeconds > 0 {
		body.WriteString("sleep " + itoa(f.SleepSeconds) + "\n")
	}
	if f.Stderr != "" {
		body.WriteString("printf '%s' " + shellQuote(f.Stderr) + " >&2\n")
	}
	if f.Stdout != "" {
		body.WriteString("printf '%s' " + shellQuote(f.Stdout) + "\n")
	}
	if f.LingerSeconds > 0 {
		body.WriteString("sleep " + itoa(f.LingerSeconds) + "\n")
	}
	body.WriteString("exit " + itoa(f.ExitCode) + "\n")

	// Executable by its owner and nobody else. A scanner's usual advice of
	// 0600 cannot apply to a file whose whole purpose is to be run, and the
	// tighter half of that advice is already met: no group or world bit is set,
	// and the file lives in a per-test temporary directory.
	// #nosec G306 -- a script that must be executable; owner-only, in a test temp dir
	err := os.WriteFile(f.path, []byte(body.String()), 0o700)
	if err != nil {
		t.Fatalf("write the fake agent: %v", err)
	}
	f.warm(t)
	return f
}

// warmupArg makes the fake exit immediately without recording anything.
const warmupArg = "__warmup__"

// warm executes the fake once and discards the result.
//
// The first execution of a newly written file costs hundreds of milliseconds
// while the operating system inspects it, which is longer than the deadlines
// the timeout tests use. Without this, those tests pass for the wrong reason:
// the deadline expires before the process has started, so nothing about a
// RUNNING child is exercised — and a test that cannot fail is worse than none.
func (f *Fake) warm(t *testing.T) {
	t.Helper()

	// Both scanners flag the variable path, and the audit they ask for is this:
	// f.path is <t.TempDir()>/fake-agent, written by Build a few lines above
	// from a script this package composes. It is never a caller's string, and
	// the single argument is a package constant.
	//
	// nosemgrep: go.lang.security.audit.dangerous-exec-command.dangerous-exec-command
	cmd := exec.Command(f.path, warmupArg) // #nosec G204 -- see the audit above: the path is this package's own file under a test temp dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("the fake agent is not executable: %v", err)
	}
}

// Path is the fake's absolute location.
func (f *Fake) Path() string { return f.path }

// Dir is the temporary directory holding the fake and its recording.
func (f *Fake) Dir() string { return f.dir }

// Invocation is what the fake observed about how it was run.
type Invocation struct {
	Args []string
	Env  map[string]string
	Cwd  string
}

// Recorded returns the invocation, failing the test if the fake was never run.
func (f *Fake) Recorded(t *testing.T) Invocation {
	t.Helper()

	raw, err := os.ReadFile(f.recordPath)
	if err != nil {
		t.Fatalf("the fake agent was never invoked: %v", err)
	}

	inv := Invocation{Env: map[string]string{}}
	inEnv := false
	for _, line := range strings.Split(string(raw), "\n") {
		switch {
		case line == "ENV":
			inEnv = true
		case inEnv:
			// A variable whose value spans lines contributes only its first,
			// which is enough for everything under test here and keeps the
			// parser free of a framing scheme the shell would have to match.
			if name, value, ok := strings.Cut(line, "="); ok {
				inv.Env[name] = value
			}
		case strings.HasPrefix(line, "arg:"):
			inv.Args = append(inv.Args, strings.TrimPrefix(line, "arg:"))
		case strings.HasPrefix(line, "cwd:"):
			inv.Cwd = strings.TrimPrefix(line, "cwd:")
		}
	}
	return inv
}

// Ran reports whether the fake was invoked at all, for the cases where not
// spawning is the point.
func (f *Fake) Ran() bool {
	_, err := os.Stat(f.recordPath)
	return err == nil
}

// ChildPID is the process the fake spawned under SpawnChild, or 0 if it never
// got that far.
func (f *Fake) ChildPID(t *testing.T) int {
	t.Helper()

	raw, err := os.ReadFile(f.childPath)
	if err != nil {
		return 0
	}
	pid := 0
	for _, c := range strings.TrimSpace(string(raw)) {
		if c < '0' || c > '9' {
			return 0
		}
		pid = pid*10 + int(c-'0')
	}
	return pid
}

// record emits the shell that writes down how the fake was invoked.
//
// The format is line-based with a prefix per field rather than JSON, because
// the fake has to run under the minimal PATH an isolated child is given — a
// POSIX shell and nothing else. Every argument gets its own "arg:" line, so an
// EMPTY argument survives: that matters because a provider passes one, and a
// recording that dropped it would make the flag it belongs to untestable.
func record(path string) string {
	return `{
  printf 'cwd:%s\n' "$PWD"
  for a in "$@"; do printf 'arg:%s\n' "$a"; done
  echo "ENV"
  env
} > ` + shellQuote(path) + "\n"
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
