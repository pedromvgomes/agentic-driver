package claudecode

// modelAliases maps a family name to the newest model in that family.
//
// Resolved here rather than passed through, even though the CLI accepts a bare
// family name itself. A name the CLI resolves means whatever that release
// decided it means, so the same request silently changes model across an
// upgrade — and a Result carries no record of which one answered. Resolving to
// a concrete ID makes the choice a property of this file, visible in a diff and
// changed deliberately.
//
// This table is the one place a version number appears. Adding a family here is
// how a new one becomes available; nothing else needs to know.
var modelAliases = map[string]string{
	"opus":   "claude-opus-5",
	"sonnet": "claude-sonnet-5",
	"haiku":  "claude-haiku-4-5",
	"fable":  "claude-fable-5-1",
}

// ResolveModel turns a family alias into the model it currently names.
//
// Anything else is returned unchanged: a concrete ID, and equally a family this
// table has not heard of. The CLI is the authority on what it accepts, so
// rejecting an unknown name here would break the day Anthropic ships a family
// this file predates — and the caller would be unable to reach it until this
// package caught up.
func (p *dialect) ResolveModel(name string) string {
	if resolved, ok := modelAliases[name]; ok {
		return resolved
	}
	return name
}

// Models lists the family aliases this provider understands, so a caller can
// offer them without hard-coding the table.
func Models() map[string]string {
	out := make(map[string]string, len(modelAliases))
	for alias, model := range modelAliases {
		out[alias] = model
	}
	return out
}
