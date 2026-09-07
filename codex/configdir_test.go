package codex

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	agentic "github.com/pedromvgomes/agentic-driver"
	"github.com/pedromvgomes/agentic-driver/agentictest"
)

// childEnv runs one request through a fake binary and reports the environment
// the child was actually given. Asserting on StreamCommand alone would test
// what the dialect asks for; this tests what survives buildEnv's scrub.
func childEnv(t *testing.T, p agentic.Provider, opts ...agentic.Option) map[string]string {
	t.Helper()

	stream, err := os.ReadFile(filepath.Join("testdata", "success-mini.ndjson"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	fake := (&agentictest.Fake{Stdout: string(stream)}).Build(t)

	d, err := agentic.New(p, append([]agentic.Option{agentic.WithBinary(fake.Path())}, opts...)...)
	if err != nil {
		t.Fatalf("agentic.New: %v", err)
	}
	if _, err := d.Run(t.Context(), agentic.Request{Prompt: "hi"}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	return fake.Recorded(t).Env
}

func withConfigDir(t *testing.T, dir string) *PathProvider {
	t.Helper()

	p, err := NewOnPath(WithConfigDir(dir))
	if err != nil {
		t.Fatalf("NewOnPath: %v", err)
	}
	return p
}

// A caller that nominates no directory gets exactly the environment it got
// before the option existed.
func TestNoConfigDirAddsNoVariable(t *testing.T) {
	inv, err := onPath(t).StreamCommand(agentic.Request{Prompt: "hi"})
	if err != nil {
		t.Fatalf("StreamCommand: %v", err)
	}

	if _, ok := inv.Env["CODEX_HOME"]; ok {
		t.Errorf("Env carries CODEX_HOME = %q, want it absent", inv.Env["CODEX_HOME"])
	}
	if inv.Env["NO_COLOR"] != "1" || inv.Env["TERM"] != "dumb" {
		t.Errorf("Env = %v, want the display settings unchanged", inv.Env)
	}
}

func TestAConfigDirBecomesCodexHome(t *testing.T) {
	dir := t.TempDir()

	inv, err := withConfigDir(t, dir).StreamCommand(agentic.Request{Prompt: "hi"})
	if err != nil {
		t.Fatalf("StreamCommand: %v", err)
	}

	if inv.Env["CODEX_HOME"] != dir {
		t.Errorf("CODEX_HOME = %q, want %q", inv.Env["CODEX_HOME"], dir)
	}
}

// The option reaches the dialect from either constructor: the two differ on
// which binary runs, not on the profile it reads.
func TestBothConstructorsCarryTheConfigDir(t *testing.T) {
	dir := t.TempDir()

	p := withConfigDir(t, dir)
	if p.configDir != dir {
		t.Errorf("NewOnPath configDir = %q, want %q", p.configDir, dir)
	}

	v, err := New(t.TempDir(), WithConfigDir(dir))
	if err != nil {
		t.Skipf("New: %v", err)
	}
	if v.configDir != dir {
		t.Errorf("New configDir = %q, want %q", v.configDir, dir)
	}
}

// CODEX_HOME redirects the profile the credential lives in, so an inherited
// one is a rerouted identity. Under isolation nothing is inherited at all,
// which is what this asserts of the assembled environment rather than of the
// deny list.
func TestAnInheritedCodexHomeNeverReachesAnIsolatedChild(t *testing.T) {
	t.Setenv("CODEX_HOME", "/somewhere/that/is/not/ours")
	home := t.TempDir()

	env := childEnv(t, onPath(t),
		agentic.WithCredentials(agentic.Isolated("token-abc")),
		agentic.WithHome(home))

	if got, ok := env["CODEX_HOME"]; ok {
		t.Errorf("CODEX_HOME = %q, want it absent from an isolated child", got)
	}
	if env["OPENAI_API_KEY"] != "token-abc" {
		t.Errorf("OPENAI_API_KEY = %q, want the injected token", env["OPENAI_API_KEY"])
	}
}

// The deny list is a backstop for a variable this dialect does not set. Naming
// one it DOES set would have buildEnv scrub the caller's own nomination.
func TestCodexHomeIsDeniedOnlyWhenTheDialectDoesNotSetIt(t *testing.T) {
	if !slices.Contains(onPath(t).DenyEnv(), "CODEX_HOME") {
		t.Error("DenyEnv omits CODEX_HOME with no config dir; an inherited profile could reroute the run")
	}
	if slices.Contains(withConfigDir(t, t.TempDir()).DenyEnv(), "CODEX_HOME") {
		t.Error("DenyEnv names CODEX_HOME while the dialect sets it; the scrub would drop the nominated directory")
	}
}

// The nominated directory has to survive the scrub that runs over the
// assembled environment, or the option would be silently inert under exactly
// the credentials mode that most needs it.
func TestAnIsolatedChildGetsTheNominatedConfigDir(t *testing.T) {
	t.Setenv("CODEX_HOME", "/somewhere/that/is/not/ours")
	dir, home := t.TempDir(), t.TempDir()

	env := childEnv(t, withConfigDir(t, dir),
		agentic.WithCredentials(agentic.Isolated("token-abc")),
		agentic.WithHome(home))

	if env["CODEX_HOME"] != dir {
		t.Errorf("CODEX_HOME = %q, want the nominated %q", env["CODEX_HOME"], dir)
	}
	if env["HOME"] != home {
		t.Errorf("HOME = %q, want the driver's nominated home %q; codex reads its profile from CODEX_HOME alone", env["HOME"], home)
	}
}

// Ambient means the caller's own environment, so a nominated directory still
// has to win over the one the operator happens to export.
func TestAnAmbientChildGetsTheNominatedConfigDir(t *testing.T) {
	t.Setenv("CODEX_HOME", "/somewhere/that/is/not/ours")
	dir := t.TempDir()

	env := childEnv(t, withConfigDir(t, dir))

	if env["CODEX_HOME"] != dir {
		t.Errorf("CODEX_HOME = %q, want the nominated %q", env["CODEX_HOME"], dir)
	}
}

// Nothing changes for a caller that nominates nothing: ambient credentials
// still mean the profile the operator is using by hand.
func TestAnAmbientChildKeepsItsOwnCodexHomeWhenNoneIsNominated(t *testing.T) {
	t.Setenv("CODEX_HOME", "/the/operators/own")

	env := childEnv(t, onPath(t))

	if env["CODEX_HOME"] != "/the/operators/own" {
		t.Errorf("CODEX_HOME = %q, want the inherited value", env["CODEX_HOME"])
	}
}
