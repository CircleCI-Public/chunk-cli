# CLI boundary review

Review only the user-facing CLI boundary: Cobra flag registration and plumbing, argument handling, stdout/stderr, JSON shape,
exit status, and compatibility when new flags are absent. Do not use subagents. Report only high or medium findings with a
command that demonstrates the failure. Do not review internal style or ask for unrelated CLI behavior.
