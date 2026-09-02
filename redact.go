package agentic

import (
	"regexp"
	"strings"
)

// redactPlaceholder replaces anything recognised as secret material.
const redactPlaceholder = "[REDACTED]"

// authHeaderPattern matches the credential inside an Authorization header, as
// a CLI would echo one back when diagnosing a rejected request.
//
// It is anchored on the header name. A bare `(bearer|basic)\s+<8 or more>` also
// matches ordinary prose — "basic authentication required" becomes
// "[REDACTED] required" — and silently eating the explanation defeats the point
// of keeping stderr at all.
var authHeaderPattern = regexp.MustCompile(
	`(?i)((?:proxy-)?authorization"?\s*[:=]\s*"?(?:bearer|basic)\s+)[A-Za-z0-9._\-+/=]{8,}`)

// keyedTokenPattern matches the `sk-` family every provider in this library
// uses: Anthropic API keys and OAuth tokens, and OpenAI's keys.
//
// Shape-based, so it catches a secret the driver was never told about — the
// ambient case, where the credential came from the caller's own environment and
// the library has no copy of it to search for.
var keyedTokenPattern = regexp.MustCompile(`sk-[A-Za-z0-9_\-]{8,}`)

// redact removes recognisable secret material from s.
//
// An authentication endpoint echoes the rejected secret back, so stderr from a
// failed invocation is credential-carrying surface regardless of what the CLI
// intended to put there. This is a backstop against mistakes, never a licence
// to pass a secret somewhere it does not belong.
//
// known is credential material the driver injected itself. It is redacted by
// exact match, which is the only thing that works for a token format no pattern
// here anticipates.
func redact(s string, known ...string) string {
	for _, secret := range known {
		// A short value would match throughout ordinary text and blank out the
		// explanation along with the secret.
		if len(secret) < minRedactableSecret {
			continue
		}
		s = strings.ReplaceAll(s, secret, redactPlaceholder)
	}
	s = keyedTokenPattern.ReplaceAllString(s, redactPlaceholder)
	// ${1} keeps the header name and scheme, so the line still shows that an
	// Authorization header was present and of what kind.
	return authHeaderPattern.ReplaceAllString(s, "${1}"+redactPlaceholder)
}

// minRedactableSecret is how long a known secret must be before it is worth
// substring-matching for.
const minRedactableSecret = 8

// maxDetail bounds what a CLI can push into an error message. A stack trace or
// an HTML error page from a proxy would otherwise carry kilobytes into a log.
const maxDetail = 256

// truncate bounds s, cutting on a rune boundary so the result is still valid
// UTF-8 rather than a replacement character in whatever renders it.
func truncate(s string) string {
	if len(s) <= maxDetail {
		return s
	}
	cut := maxDetail
	for cut > 0 && !isRuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }

// detail renders captured stderr for an error message: redacted, collapsed and
// bounded.
func detail(stderr []byte, known ...string) string {
	s := strings.TrimSpace(string(stderr))
	if s == "" {
		return ""
	}
	return truncate(redact(s, known...))
}
