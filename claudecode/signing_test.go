package claudecode_test

import (
	"strings"
	"testing"

	agentic "github.com/pedromvgomes/agentic-driver"
	"github.com/pedromvgomes/agentic-driver/claudecode"
)

// The provenance claim is the fingerprint, not the word "verified". An operator
// compares it against Anthropic's own published value, and an identity that
// named nothing would be an assertion nobody can check.
func TestTheVendoredProviderNamesTheKeyItVerifiesAgainst(t *testing.T) {
	p, err := claudecode.New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	d, err := agentic.New(p)
	if err != nil {
		t.Fatalf("agentic.New: %v", err)
	}

	identity, err := d.SigningIdentity()
	if err != nil {
		t.Fatalf("SigningIdentity: %v", err)
	}
	if !strings.Contains(identity, claudecode.SigningKeyFingerprint) {
		t.Errorf("SigningIdentity() = %q, want it to name %s", identity, claudecode.SigningKeyFingerprint)
	}
}

// Both, and the pair is the claim: Pinner says which build runs, Installer that
// it was signed by the key SigningIdentity names.
func TestTheVendoredProviderPinsAndVerifies(t *testing.T) {
	p, err := claudecode.New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var subject any = p
	if _, ok := subject.(agentic.Pinner); !ok {
		t.Error("Provider does not implement Pinner")
	}
	if _, ok := subject.(agentic.Installer); !ok {
		t.Error("Provider does not implement Installer; it verifies a signed manifest")
	}
}

// A provider that runs someone else's binary has chosen no version and verified
// no signature, and the driver reads that absence rather than taking its word.
func TestThePathProviderClaimsNeither(t *testing.T) {
	p, err := claudecode.NewOnPath()
	if err != nil {
		t.Fatalf("NewOnPath: %v", err)
	}

	var subject any = p
	if _, ok := subject.(agentic.Pinner); ok {
		t.Error("PathProvider implements Pinner")
	}
	if _, ok := subject.(agentic.Installer); ok {
		t.Error("PathProvider implements Installer")
	}
}
