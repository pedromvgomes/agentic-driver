# Comment hygiene (house rule — always in scope)

Comments, doc comments, commit-adjacent docs, and identifiers in the diff must describe the code **as it is**, never narrate the change
that produced it. Code outlives its diff; a comment that only makes sense next to the PR is wrong the day it merges.

Flag any added or modified comment/doc line that:

- References the change's history: "previously", "used to", "no longer", "instead of the old", "moved from", "renamed from", "an
  earlier version". Past tense alone is not the signal — "the caller was validated upstream" describes the code; "this was a loose
  record" describes a diff.
- Narrates the edit: "Added ...", "Updated ...", "Removed ...", "Changed ...", "Refactored ..." as the comment's subject.
- Marks the change as a fix rather than describing behavior: "Regression:", "Fix for", "Fixes the bug where", "Workaround for <ticket>".
- Embeds PR numbers, ticket IDs, or issue links whose only purpose is change tracking (a link that documents an external contract or
  upstream bug the code must accommodate is fine).
- Uses "new" as change narration ("new implementation", "the new endpoint") rather than as a domain term.

File these as `severity: AMBER`, `category: conventions:comment-hygiene`, quoting the offending comment in `evidence` and proposing a
rewrite that states what the code does now. If deleting the comment outright is the honest fix, propose that.

Do not flag: changelog files, release notes, migration guides, or git commit messages — narration is their job. Do not flag comments the
diff merely moved without editing.
