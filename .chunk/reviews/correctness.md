Review the changes on this branch for correctness bugs.

Focus on logic errors, unhandled error paths, off-by-one mistakes, and
concurrency problems (data races, goroutine leaks, missing context
cancellation). For each finding, give the file and line, the concrete input
or state that triggers it, and what goes wrong.
