package agentic_test

import (
	"path/filepath"
	"strings"
	"testing"

	agentic "github.com/pedromvgomes/agentic-driver"
	"github.com/pedromvgomes/agentic-driver/agentictest"
)

// poison sets every variable a provider might deny, in the PARENT process, so
// the child can be asked whether any of them arrived.
//
// This is the failure with no symptom: the run works, the answers come back
// correct, and the wrong account is billed. Nothing observable says so until an
// invoice does.
func poison(t *testing.T, names ...string) {
	t.Helper()

	for _, name := range names {
		t.Setenv(name, "leaked-"+name)
	}
}

const denied = "PROVIDER_API_KEY"

func isolatedDriver(t *testing.T, f *agentictest.Fake, token string, opts ...agentic.Option) *agentic.Driver {
	t.Helper()

	p := &isolating{stub: stub{deny: []string{denied}}}
	opts = append([]agentic.Option{
		agentic.WithBinary(f.Path()),
		agentic.WithCredentials(agentic.Isolated(token)),
		agentic.WithHome(t.TempDir()),
	}, opts...)

	d, err := agentic.New(p, opts...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return d
}

// The guarantee is that the environment is CONSTRUCTED, not filtered. A
// variable nobody thought of cannot arrive, because nothing arrives that was
// not written down.
func TestAnIsolatedChildInheritsNothingItWasNotGiven(t *testing.T) {
	poison(t, denied, "SOME_UNRELATED_VARIABLE", "EDITOR")

	fake := (&agentictest.Fake{Stdout: okEnvelope}).Build(t)
	d := isolatedDriver(t, fake, "the-token")

	if _, err := d.Run(t.Context(), agentic.Request{Prompt: "hi"}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	env := fake.Recorded(t).Env
	for _, name := range []string{denied, "SOME_UNRELATED_VARIABLE", "EDITOR"} {
		if value, present := env[name]; present {
			t.Errorf("the child inherited %s=%q from the parent", name, value)
		}
	}
	if env["STUB_TOKEN"] != "the-token" {
		t.Errorf("STUB_TOKEN = %q, want the token the caller supplied", env["STUB_TOKEN"])
	}
}

// The deny list is a backstop, not the mechanism: it catches what a later edit
// puts back. Here the provider's own dialect map is the later edit.
func TestADeniedVariableIsDroppedEvenWhenTheProviderSuppliesIt(t *testing.T) {
	fake := (&agentictest.Fake{Stdout: okEnvelope}).Build(t)
	p := &isolating{stub: stub{
		deny: []string{denied},
		env:  map[string]string{denied: "supplied-by-the-provider", "DIALECT": "1"},
	}}

	d, err := agentic.New(p,
		agentic.WithBinary(fake.Path()),
		agentic.WithCredentials(agentic.Isolated("the-token")),
		agentic.WithHome(t.TempDir()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := d.Run(t.Context(), agentic.Request{Prompt: "hi"}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	env := fake.Recorded(t).Env
	if _, present := env[denied]; present {
		t.Errorf("%s reached the child even though the provider denies it", denied)
	}
	if env["DIALECT"] != "1" {
		t.Error("the provider's own dialect settings did not reach the child")
	}
}

// The credential is applied after the dialect map, so a provider cannot shadow
// the token it is meant to accompany.
func TestTheCredentialOutranksTheProvidersOwnEnvironment(t *testing.T) {
	fake := (&agentictest.Fake{Stdout: okEnvelope}).Build(t)
	p := &isolating{stub: stub{env: map[string]string{"STUB_TOKEN": "shadowed"}}}

	d, err := agentic.New(p,
		agentic.WithBinary(fake.Path()),
		agentic.WithCredentials(agentic.Isolated("the-token")),
		agentic.WithHome(t.TempDir()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := d.Run(t.Context(), agentic.Request{Prompt: "hi"}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if got := fake.Recorded(t).Env["STUB_TOKEN"]; got != "the-token" {
		t.Errorf("STUB_TOKEN = %q, want the caller's credential to win", got)
	}
}

func TestAnIsolatedChildGetsTheNominatedHome(t *testing.T) {
	home := t.TempDir()
	fake := (&agentictest.Fake{Stdout: okEnvelope}).Build(t)
	d := isolatedDriver(t, fake, "the-token", agentic.WithHome(home))

	if _, err := d.Run(t.Context(), agentic.Request{Prompt: "hi"}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	env := fake.Recorded(t).Env
	if env["HOME"] != home {
		t.Errorf("HOME = %q, want the nominated directory %q", env["HOME"], home)
	}
	if env["PATH"] == "" {
		t.Error("PATH is empty, so the CLI can shell out to nothing at all")
	}
}

// Ambient means "use whatever the CLI is already authenticated with". Applying
// the deny list here would break the mode: a developer pointing the CLI at a
// proxy is using it as intended.
func TestAmbientCredentialsInheritTheParentEnvironment(t *testing.T) {
	poison(t, denied)

	fake := (&agentictest.Fake{Stdout: okEnvelope}).Build(t)
	p := &isolating{stub: stub{deny: []string{denied}, env: map[string]string{"DIALECT": "1"}}}

	d, err := agentic.New(p, agentic.WithBinary(fake.Path()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := d.Run(t.Context(), agentic.Request{Prompt: "hi"}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	env := fake.Recorded(t).Env
	if env[denied] == "" {
		t.Errorf("%s did not reach an ambient child, so the mode is not ambient", denied)
	}
	if env["DIALECT"] != "1" {
		t.Error("the provider's dialect settings did not reach an ambient child")
	}
}

// An authentication endpoint echoes the rejected secret back, so stderr from a
// failed invocation carries credential material regardless of what the CLI
// intended to put there.
func TestStderrIsRedactedBeforeItReachesAnError(t *testing.T) {
	const token = "sk-ant-oat01-0123456789abcdefghijklmnop"

	fake := (&agentictest.Fake{
		Stdout:   "not json",
		Stderr:   "rejected token " + token + " (Authorization: Bearer " + token + ")",
		ExitCode: 1,
	}).Build(t)
	d := isolatedDriver(t, fake, token)

	_, err := d.Run(t.Context(), agentic.Request{Prompt: "hi"})
	if err == nil {
		t.Fatal("Run accepted unparseable output")
	}
	if strings.Contains(err.Error(), token) {
		t.Errorf("the error carries the credential verbatim: %q", err)
	}
	if !strings.Contains(err.Error(), "rejected token") {
		t.Errorf("redaction ate the explanation as well as the secret: %q", err)
	}
}

// A secret the library was never told about — the ambient case, where the
// credential came from the caller's own environment — is still removed by
// shape.
func TestAnAmbientCredentialIsRedactedByShape(t *testing.T) {
	const token = "sk-ant-api03-abcdefghijklmnopqrstuvwxyz"

	fake := (&agentictest.Fake{Stdout: "not json", Stderr: "401 for " + token, ExitCode: 1}).Build(t)
	d := driver(t, &stub{}, fake)

	_, err := d.Run(t.Context(), agentic.Request{Prompt: "hi"})
	if err == nil {
		t.Fatal("Run accepted unparseable output")
	}
	if strings.Contains(err.Error(), token) {
		t.Errorf("the error carries a credential the driver never held: %q", err)
	}
}

// A stack trace or an HTML error page from a proxy would otherwise carry
// kilobytes into whatever logs the error.
func TestStderrIsBoundedBeforeItReachesAnError(t *testing.T) {
	fake := (&agentictest.Fake{Stdout: "not json", Stderr: strings.Repeat("x", 10_000), ExitCode: 1}).Build(t)
	d := driver(t, &stub{}, fake)

	_, err := d.Run(t.Context(), agentic.Request{Prompt: "hi"})
	if err == nil {
		t.Fatal("Run accepted unparseable output")
	}
	if len(err.Error()) > 1_000 {
		t.Errorf("the error is %d bytes; stderr was not bounded", len(err.Error()))
	}
}

func TestTheWorkingDirectoryIsTheOneTheRequestAsksFor(t *testing.T) {
	dir := t.TempDir()
	fake := (&agentictest.Fake{Stdout: okEnvelope}).Build(t)
	d := driver(t, &stub{}, fake)

	if _, err := d.Run(t.Context(), agentic.Request{Prompt: "hi", WorkDir: dir}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// A temporary directory is reached through a symlink on some platforms, and
	// the shell reports the resolved path. Both sides are resolved so the test
	// is about the directory, not about how it was spelled.
	want, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("resolve %s: %v", dir, err)
	}
	got, err := filepath.EvalSymlinks(fake.Recorded(t).Cwd)
	if err != nil {
		t.Fatalf("resolve the recorded cwd: %v", err)
	}
	if got != want {
		t.Errorf("cwd = %q, want %q", got, want)
	}
}
