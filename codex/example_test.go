package codex_test

import (
	"fmt"
	"log"

	"github.com/pedromvgomes/agentic-driver/codex"
)

// A family alias resolves to the newest build in that family, spelled in
// OpenAI's own vocabulary. Anything else is passed through, so a concrete ID
// works and so does a family newer than this package.
func ExamplePathProvider_ResolveModel() {
	provider, err := codex.NewOnPath()
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println(provider.ResolveModel("astra"))
	fmt.Println(provider.ResolveModel("mini"))
	fmt.Println(provider.ResolveModel("gpt-5.5"))
	// Output:
	// gpt-6-astra
	// gpt-5.4-mini
	// gpt-5.5
}
