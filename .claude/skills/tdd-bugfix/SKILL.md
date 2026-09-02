---
name: tdd-bugfix
description: "Use whenever the goal is to correct a defect: a reported bug, a failing test, a CI or build failure to investigate, a regression, or any \"why is this broken / make this behave correctly\" task. Enforces a test-first RED→GREEN workflow — reproduce the bug with a failing test and confirm it fails for the RIGHT reason BEFORE writing any fix, then implement the fix and confirm that test plus the surrounding suite pass. Trigger this even when the user never says \"TDD\" or \"test\": phrases like \"fix this bug\", \"investigate the CI failure\", \"this is broken\", \"figure out why X happens\", or pasting a stack trace all count. Do NOT jump straight to editing source code."
---

# TDD Bugfix

A bug is a gap between expected and actual behavior that no current test catches.
The fix is not done when the symptom disappears — it is done when a test *would
have caught the bug* and now passes. Writing that test first is the only way to be
sure of both.

The discipline is: make the bug visible as a failing test (RED), then make it
disappear (GREEN), then leave the test behind as a guard. Skipping the RED step is
the most common way to "fix" something that was never actually broken where you
thought, or to ship a fix that quietly does nothing.

## When this applies

Use this loop for any defect-correction task, including when the bug is discovered
indirectly:

- A bug report or reproduction steps from a user or ticket.
- A failing or flaky test you're asked to fix.
- A CI / build / pipeline failure you're asked to investigate.
- A regression ("this used to work").
- Vague reports — "this is broken", "the numbers are wrong", "it hangs sometimes" —
  where the first job is to pin the defect down precisely enough to write a test.

This is for correcting *incorrect behavior*. Pure new-feature work and mechanical
refactors (behavior unchanged, tests already green) are out of scope.

## The loop: RED → GREEN → guard

### 1. Reproduce — understand the exact failure first

Do not write a test until you can state, in one sentence, what the wrong behavior is
and where it happens. Resist the pull to start editing source. Concretely:

- Read the actual evidence: the stack trace, assertion message, failing CI log,
  reproduction steps. For a CI failure, read the real error from the logs before
  forming any theory — do not guess from the job name.
- Locate the smallest unit that exhibits the bug (a function, an endpoint, a
  workflow step). The narrower the surface, the faster the test.
- Form a falsifiable hypothesis: "given input X, the code returns Y but should
  return Z." If you can't yet, investigate until you can.

### 2. RED — write a test that fails for the right reason

Write a test that asserts the *correct* behavior, so it fails against the current
buggy code. Then run it and read the failure.

This is the step that earns the whole skill, so be rigorous about it:

- **Confirm it fails, and fails for the reason you predicted.** The failure message /
  assertion must match your hypothesis from step 1. A test that fails because of a
  compile error, a missing import, a wrong fixture, or an unrelated assertion has
  proven nothing — fix the test scaffolding and re-run until the *only* reason it's
  red is the bug itself.
- **If it unexpectedly passes, STOP.** Do not edit source. An already-green test means
  one of: the bug isn't where you think, your reproduction is wrong, or it's already
  fixed. Go back to step 1 with the new information.
- Make the test minimal and targeted at the defect — one clear assertion about the
  wrong behavior, not a sprawling scenario.
- Name it so its intent survives without context, and reference the ticket/issue if
  there is one (e.g. `returns_zero_balance_for_closed_account`, or a name tying it to
  the bug ID). This test is going to live in the suite permanently.

Run the narrowest target that includes the new test (see "Running tests" below) and
paste/read the failure before moving on.

### 3. GREEN — implement the minimal fix

Now, and only now, change the source.

- Make the smallest change that turns the red test green. Do not bundle refactors,
  cleanup, or unrelated improvements into the same change — note them separately and
  raise them after.
- **Never weaken the test to make it pass.** Loosening an assertion, deleting a case,
  or adding a special-case branch *in the test* to dodge the failure defeats the
  entire purpose. The code moves to meet the test, never the reverse.

### 4. Verify and guard against regression

A passing new test is necessary but not sufficient.

- Re-run the new test → confirm GREEN.
- Run the surrounding suite (the module/package, then wider if the change is risky) →
  confirm you didn't break anything else. Bug fixes frequently break adjacent
  behavior that no longer matches the old wrong assumption.
- **Keep the reproduction test.** It is now a regression guard — never delete it after
  the fix. That permanent test is the durable value of this whole exercise.
- For anything subtle or high-stakes, *prove the test catches the bug*: temporarily
  revert the fix, confirm the test goes RED again, then re-apply. This guarantees the
  test is actually wired to the defect and not passing for an incidental reason.

Only report the bug as fixed once the new test passes, the suite is green, and the
reproduction test remains in place.

## Running tests

Run the **narrowest target** during the loop for fast feedback (a single test or
test class), then widen to the module and finally the relevant suite before
declaring done. Discover the right command from the project rather than assuming —
check the build files, existing test layout, and CI config:

- **Gradle / Kotlin / JVM**: `./gradlew :module:test --tests 'FullyQualifiedTestName'`;
  whole module `:module:test`. Check `build.gradle(.kts)` and the CI workflow for the
  exact module path and any required flags.
- **Node / TypeScript**: inspect `package.json` scripts. Common: `npm test -- <pattern>`,
  `jest <file>`, `vitest run <file>`. Match the runner the repo actually uses.
- **Rust**: `cargo test <test_name>` for one test, `cargo test -p <crate>` for a crate.
- **Other stacks**: read the CI config — it is the source of truth for how tests are
  run in this repo, including environment setup.

If you can't determine the command, find and mimic how an existing test in the repo
is run rather than inventing one.

## When you genuinely can't write a failing test first

Some bugs resist a clean test-first reproduction: races and concurrency, timing,
flakiness, environment-specific failures, or defects buried behind untested
integration seams. Do not silently abandon TDD when this happens. Instead:

- **Try harder first.** Loop/stress the test to surface a race; inject the triggering
  condition (clock, fault, ordering) deterministically; add a seam or test hook;
  reproduce at a lower level than where the symptom appears.
- **If still impossible, say so explicitly** and write the closest test you can — even
  one that characterizes the surrounding behavior or asserts the invariant that was
  violated — and call out in your summary that a true pre-fix RED couldn't be
  achieved and why. Never imply test-first happened when it didn't.

A flaky test in CI may be a real concurrency bug (in scope — reproduce and fix) or
genuine infrastructure noise (out of scope for a code fix — diagnose and report
rather than patching the test to hide it). Decide which before touching anything.

## Anti-patterns — do not do these

- Editing source before a failing test exists.
- Accepting a red test without checking it failed *for the bug's reason*.
- Continuing after a test unexpectedly passes instead of re-investigating.
- Changing the test to fit the code (loosening assertions, deleting cases).
- Deleting the reproduction test once the fix is in.
- Bundling unrelated refactors into the fix.
- Declaring victory on a green new test without running the wider suite.

## Quick reference

```
1. Reproduce   → state the exact wrong behavior (read logs/trace, form hypothesis)
2. RED         → write test for correct behavior → run → MUST fail for the right reason
                 (passes unexpectedly? STOP, re-investigate)
3. GREEN       → smallest source change to pass → never weaken the test
4. Guard       → new test green + suite green + keep the test (optionally revert-to-confirm)
```
