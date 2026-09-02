package claudecode

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// gated stands up an installer whose binary download blocks until released, so
// several callers can be held in flight at once.
//
// started closes once the download has actually begun, which is the only point
// at which "the first caller owns this install" is a statement about anything.
func gated(t *testing.T) (inst *Installer, version string, started <-chan struct{}, release chan<- struct{}) {
	t.Helper()

	b := newBucket(t)
	server := b.serve(t)

	var once atomic.Bool
	begun := make(chan struct{})
	unblock := make(chan struct{})

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/"+BinaryName) {
			if once.CompareAndSwap(false, true) {
				close(begun)
			}
			select {
			case <-unblock:
			case <-r.Context().Done():
				// The download was cancelled. Answering nothing is what the
				// caller's context asked for.
				return
			}
		}
		http.Redirect(w, r, server.URL+r.URL.Path, http.StatusTemporaryRedirect)
	})
	proxy := httptest.NewServer(mux)
	t.Cleanup(proxy.Close)

	installer, err := NewInstaller(t.TempDir(), WithBaseURL(proxy.URL), WithHTTPClient(proxy.Client()))
	if err != nil {
		t.Fatalf("NewInstaller: %v", err)
	}
	trust(installer, b.entity)

	return installer, b.version, begun, unblock
}

func waitFor(t *testing.T, c <-chan struct{}, what string) {
	t.Helper()

	select {
	case <-c:
	case <-time.After(20 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

// An install belongs to everyone waiting on it, not to whoever arrived first.
//
// The last caller to leave cancels the download; the first one to leave does
// not, because it is not special. Anything else is the mirror image of a joiner
// leaving, which correctly leaves the download running for the callers that
// remain.
func TestTheFirstCallerLeavingDoesNotCancelEveryoneElsesInstall(t *testing.T) {
	inst, version, started, release := gated(t)

	firstCtx, giveUp := context.WithCancel(context.Background())
	first := make(chan error, 1)
	go func() {
		_, err := inst.Install(firstCtx, version)
		first <- err
	}()

	waitFor(t, started, "the download to begin")

	// A second caller joins the download the first one started.
	second := make(chan error, 1)
	go func() {
		_, err := inst.Install(context.Background(), version)
		second <- err
	}()
	// Long enough for the joiner to have reached its wait.
	time.Sleep(200 * time.Millisecond)

	giveUp()
	select {
	case err := <-first:
		if err == nil {
			t.Error("the caller that gave up was told the install succeeded")
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the caller that gave up never returned")
	}

	close(release)

	select {
	case err := <-second:
		if err != nil {
			t.Errorf("the remaining caller's install was cancelled with it: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the remaining caller never returned")
	}

	installed, err := inst.Installed(context.Background())
	if err != nil {
		t.Fatalf("Installed: %v", err)
	}
	if len(installed) != 1 || installed[0] != version {
		t.Errorf("Installed() = %v, want the version the remaining caller waited for", installed)
	}
}

// Detaching the download from its first caller must not make it unstoppable:
// once nobody is waiting, the work has no one to be for.
func TestTheLastCallerLeavingStopsTheDownload(t *testing.T) {
	inst, version, started, release := gated(t)
	defer close(release)

	ctx, giveUp := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := inst.Install(ctx, version)
		done <- err
	}()

	waitFor(t, started, "the download to begin")
	giveUp()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("the caller that gave up was told the install succeeded")
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the caller that gave up never returned")
	}

	// The download is cancelled, so nothing is ever published. Polling rather
	// than asserting once: the goroutine unwinds after the caller returns.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		installed, err := inst.Installed(context.Background())
		if err != nil {
			t.Fatalf("Installed: %v", err)
		}
		if len(installed) == 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Error("the download carried on after the last caller left, so an abandoned install cannot be stopped")
}

// A caller's own deadline bounds that caller's wait, not the download, so a
// short-deadline request cannot truncate an install someone else is waiting on.
func TestAShortDeadlineDoesNotTruncateAnotherCallersInstall(t *testing.T) {
	inst, version, started, release := gated(t)

	impatientCtx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	impatient := make(chan error, 1)
	go func() {
		_, err := inst.Install(impatientCtx, version)
		impatient <- err
	}()

	waitFor(t, started, "the download to begin")

	patient := make(chan error, 1)
	go func() {
		_, err := inst.Install(context.Background(), version)
		patient <- err
	}()
	time.Sleep(200 * time.Millisecond)

	select {
	case err := <-impatient:
		if err == nil {
			t.Error("the impatient caller was told the install succeeded")
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the impatient caller never returned")
	}

	close(release)

	select {
	case err := <-patient:
		if err != nil {
			t.Errorf("the patient caller inherited the other one's deadline: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the patient caller never returned")
	}
}

// A download that is unwinding from a cancellation is not something a later
// caller can join.
//
// Between the last waiter leaving and the goroutine noticing, the install is
// still in flight but already doomed — and it will publish the cancellation
// that stopped it. A caller arriving in that window has a perfectly healthy
// context of its own and must not be handed someone else's context.Canceled.
func TestACancelledInstallIsNotJoinableByTheNextCaller(t *testing.T) {
	inst, version, started, release := gated(t)

	ctx, giveUp := context.WithCancel(context.Background())
	first := make(chan error, 1)
	go func() {
		_, err := inst.Install(ctx, version)
		first <- err
	}()

	waitFor(t, started, "the download to begin")
	giveUp()

	select {
	case <-first:
	case <-time.After(20 * time.Second):
		t.Fatal("the caller that gave up never returned")
	}

	// Immediately afterwards, while the abandoned download is still unwinding.
	second := make(chan error, 1)
	go func() {
		_, err := inst.Install(context.Background(), version)
		second <- err
	}()

	// Let whichever download this caller ends up on run to completion.
	close(release)

	select {
	case err := <-second:
		if errors.Is(err, context.Canceled) {
			t.Fatalf("a caller with a healthy context inherited an abandoned install's cancellation: %v", err)
		}
		if err != nil {
			t.Fatalf("Install: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the second caller never returned")
	}

	installed, err := inst.Installed(context.Background())
	if err != nil {
		t.Fatalf("Installed: %v", err)
	}
	if len(installed) != 1 || installed[0] != version {
		t.Errorf("Installed() = %v, want the version the second caller installed", installed)
	}
}
