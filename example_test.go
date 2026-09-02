package agentic_test

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	agentic "github.com/pedromvgomes/agentic-driver"
	"github.com/pedromvgomes/agentic-driver/claudecode"
)

// The developer-machine case: run the claude that is already installed and
// already authenticated.
func Example() {
	provider, err := claudecode.NewOnPath()
	if err != nil {
		log.Fatal(err)
	}

	driver, err := agentic.New(provider, agentic.WithModel("opus"))
	if err != nil {
		log.Fatal(err)
	}

	result, err := driver.Run(context.Background(), agentic.Request{
		Prompt: "What does this repository do?",
	})
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println(result.Text)
	fmt.Printf("answered by %s for $%.4f\n", result.Model, result.Usage.CostUSD)
}

// A CLI that ran and reported a failure of its own is a verdict, not an outage.
// It comes back as a Result with IsError set and a nil error, and treating it
// as a failed run would send the caller hunting a problem that is not there.
func ExampleDriver_Run_verdict() {
	driver := mustDriver()

	result, err := driver.Run(context.Background(), agentic.Request{Prompt: "hello"})
	switch {
	case errors.Is(err, agentic.ErrProviderUnavailable):
		// The CLI could not be run, or could not be understood. Nothing here
		// says anything about the request.
		fmt.Println("the provider is unavailable:", err)
	case err != nil:
		fmt.Println("the request was refused:", err)
	case result.IsError:
		// The CLI ran and said no. Text carries its explanation.
		fmt.Println("claude ended the turn:", result.Text)
	default:
		fmt.Println(result.Text)
	}
}

// Isolated credentials build the child environment from a fixed list rather
// than filtering the parent's, so a variable nobody thought of cannot arrive by
// accident.
func ExampleIsolated() {
	provider, err := claudecode.New("/var/lib/agents", claudecode.WithConfigDir("/var/lib/agents/config"))
	if err != nil {
		log.Fatal(err)
	}

	driver, err := agentic.New(provider,
		agentic.WithCredentials(agentic.Isolated(os.Getenv("SUBSCRIPTION_TOKEN"))),
		agentic.WithHome("/var/lib/agents/home"),
		agentic.WithTimeout(2*time.Minute))
	if err != nil {
		log.Fatal(err)
	}

	// A vendored provider is configured before its binary exists, so the two
	// states are worth telling apart before a request depends on it.
	if err := driver.Ready(); err != nil {
		if _, installErr := driver.Install(context.Background(), ""); installErr != nil {
			log.Fatal(installErr)
		}
	}

	result, err := driver.Run(context.Background(), agentic.Request{Prompt: "hello"})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(result.Text)
}

// Streaming yields events as the agent works, ending with a terminal event
// whose Result is what Run would have returned.
func ExampleDriver_Stream() {
	driver := mustDriver()

	events, err := driver.Stream(context.Background(), agentic.Request{Prompt: "Summarise the README."})
	if err != nil {
		log.Fatal(err)
	}

	for event, err := range events {
		if err != nil {
			log.Fatal(err)
		}
		switch event.Kind {
		case agentic.EventKindText:
			fmt.Print(event.Text)
		case agentic.EventKindToolUse:
			fmt.Printf("\n[running %s]\n", event.Text)
		case agentic.EventKindResult:
			fmt.Printf("\n$%.4f\n", event.Result.Usage.CostUSD)
		}
	}
}

// A session is continued by handing back the SessionID the previous turn
// returned. A provider that cannot resume refuses the request rather than
// starting a fresh session that answers as though it had read the history.
func ExampleDriver_Run_resume() {
	driver := mustDriver()
	ctx := context.Background()

	first, err := driver.Run(ctx, agentic.Request{Prompt: "Read main.go."})
	if err != nil {
		log.Fatal(err)
	}

	second, err := driver.Run(ctx, agentic.Request{
		Prompt:    "Now list its exported functions.",
		SessionID: first.SessionID,
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(second.Text)
}

// A family alias resolves to the newest build in that family, and Model reports
// which one that currently is — so a caller can show the active model without
// having run anything.
func ExampleDriver_Model() {
	driver := mustDriver()

	fmt.Println(driver.Model())
	fmt.Println(driver.ResolveModel("haiku"))
}

func mustDriver() *agentic.Driver {
	provider, err := claudecode.NewOnPath()
	if err != nil {
		log.Fatal(err)
	}
	driver, err := agentic.New(provider)
	if err != nil {
		log.Fatal(err)
	}
	return driver
}
