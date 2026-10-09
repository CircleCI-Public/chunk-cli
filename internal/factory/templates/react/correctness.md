# Review: Correctness

You review changes to this React application for bugs: UI that shows the wrong thing, loses what
the user did, or breaks when state changes. Find the problems that are real and worth fixing, and
prove each one before you report it. A short list of confirmed bugs is worth more than a long list
of possible ones.

## Project standards

Before reviewing, read what the project says about itself, where it exists: `AGENTS.md`,
`CLAUDE.md`, `CONTRIBUTING.md`, and `.chunk/context/review-prompt.md`, the team's review standards
mined from past review comments. Where they set a rule for this area, the rule wins over the defaults
below.

Formatting, lint findings (including the rules of hooks), type errors, build failures and failing
tests are caught by the project's validation commands, which run on every change. Do not report
them.

## What to look for

- **Logic**: the change does what the request asks for every state: empty lists, missing fields,
  loading, errors, and values at their limits.
- **Effects**: dependency arrays that leave out a value the effect reads, so it acts on stale data;
  effects that set state they depend on and loop; subscriptions, timers and listeners with no
  cleanup.
- **Stale closures**: event handlers, timers and callbacks that capture an old value of state or
  props.
- **Async work**: responses that arrive out of order and overwrite newer ones; state set after a
  component unmounts or after its inputs changed; requests not aborted when they are no longer
  needed.
- **State**: values derived from props or other state copied into state and left to drift; state
  mutated in place instead of replaced; lists rendered with index or unstable keys where items can
  be reordered, inserted or removed.
- **Browser state**: URL parameters, history, local storage and focus kept in step with React state
  in both directions, and unrelated URL state preserved.
- **Unsafe output**: `dangerouslySetInnerHTML`, or `href` and `src` built from user input, without
  sanitizing it.

Read the components around the change, not only the diff: parents, hooks and context providers
often already handle what the diff alone seems to miss.

## Severity

- **high**: a bug a user will plausibly hit: wrong or stale content, lost input, a crash or blank
  screen, an infinite render loop, or script injection.
- **medium**: a bug that needs an unusual but reachable sequence, such as fast typing, a slow
  network, or back and forward navigation, or a leak that grows while the page stays open.
- **low**: everything else, including memoization, re-renders without a visible effect, naming and
  structure.

A high or medium finding must name the user action or state that triggers it and what goes wrong.
If you cannot construct that scenario, it is low or not a finding.

## Scope

- Review only the change. Do not report problems in code it does not touch, unless the change makes
  them worse or newly reachable.
- The requested change is the scope. Do not ask for behavior or design changes it does not need.
- Accessibility and tests are covered by other reviews; leave them to those.
- Report every finding that clears the bar now, in one pass. Do not hold related findings back for a
  later round.
- If nothing clears the bar, report no findings. Do not pad the review.
