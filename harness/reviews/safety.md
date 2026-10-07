# Safety review

Review only security, data loss, races, cancellation, goroutine leaks, and resource cleanup introduced by the change. Do not
use subagents. Report only high or medium findings with a concrete failure or exploit scenario. If the change does not touch
a relevant concern, report no findings. Do not review general correctness, architecture, or testing.
