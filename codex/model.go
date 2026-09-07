package codex

// modelAliases maps a family name to the newest model in that family.
//
// Resolved here rather than passed through, for the reason claudecode resolves
// its own: a name the CLI resolves means whatever that release decided it
// means, so the same request silently changes model across an upgrade, and a
// Result carries no record of which one answered. Resolving to a concrete ID
// makes the choice a property of this file, visible in a diff and changed
// deliberately.
//
// The vocabulary is OpenAI's own. A vocabulary SHARED with the other providers
// — a "sonnet" that means something here too — would let a manifest swap
// providers without rewriting its models, and it would buy that by having this
// library assert that some OpenAI model is the counterpart of some Anthropic
// one. That is an editorial claim about two vendors' products that nothing here
// has standing to make, and it fails in the direction resolution exists to
// prevent: a caller changing one line of configuration would silently get a
// model nobody chose. Each dialect's aliases name that vendor's own families,
// and a caller wanting the same model everywhere names it concretely.
//
// This table is the one place a version number appears for this dialect. Adding
// a family here is how a new one becomes available; nothing else needs to know.
var modelAliases = map[string]string{
	"astra": "gpt-6-astra",
	"sol":   "gpt-5.6-sol",
	"terra": "gpt-5.6-terra",
	"luna":  "gpt-5.6-luna",
	"mini":  "gpt-5.4-mini",
}

// ResolveModel turns a family alias into the model it currently names.
//
// Anything else is returned unchanged: a concrete ID, and equally a family this
// table has not heard of. The CLI is the authority on what it accepts, so
// rejecting an unknown name here would put every model OpenAI ships after this
// file behind a release of this package.
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
