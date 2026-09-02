package claudecode_test

import (
	"fmt"
	"log"

	"github.com/pedromvgomes/agentic-driver/claudecode"
)

// A family alias resolves to the newest build in that family. Anything else is
// passed through, so a concrete ID works and so does a family newer than this
// package.
func ExamplePathProvider_ResolveModel() {
	provider, err := claudecode.NewOnPath()
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println(provider.ResolveModel("opus"))
	fmt.Println(provider.ResolveModel("haiku"))
	fmt.Println(provider.ResolveModel("claude-opus-4-8"))
	// Output:
	// claude-opus-5
	// claude-haiku-4-5
	// claude-opus-4-8
}
