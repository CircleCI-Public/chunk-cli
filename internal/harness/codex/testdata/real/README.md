`codex-exec-host-missing.jsonl` is **reconstructed**, not captured: it follows the
event shapes of `../../../testdata/real/codex-exec-pass.jsonl` (codex-cli 0.162.0) and
quotes the summary of the pilot run that hit the missing `codex-code-mode-host`
(agent message, path scrubbed to `/home/circleci/...`). The failed command's output
line is an assumption about how Codex words the error. Swap in the real scrubbed log
when there is one; the parser test only needs the phrase to appear.
