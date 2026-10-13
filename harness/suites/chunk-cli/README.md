# chunk-cli review suite

Cases for `chunk factory eval`, taken from this repository's history. Run it
from a full clone of chunk-cli, since each case's revision must be there:

```bash
chunk factory eval --suite harness/suites/chunk-cli [prompts-dir]
```

Each case is one commit on main, as its patch against its first parent, with
its commit message as the request.

**Cases with a known problem** are commits that shipped a bug a later commit
fixed. The expected problem is the line the fix changed, in the file as the
buggy commit left it:

| Case | Commit | Fixed by | Problem |
|---|---|---|---|
| `validate-project-root` | #624 | #650 | promoted git root not passed on to command registration |
| `validate-project-root` | #624 | not yet | a snapshot run's `--project` is the live tree, while its result is filed as the snapshot's |
| `telemetry-error-message` | #471 | #626 | error messages, which embed file paths, sent with telemetry |
| `telemetry-error-message` | #471 | #609 | failure events never sent: the sender is closed only in `PersistentPostRunE`, which cobra skips on error |

The second problem in each case was found by the first eval run, not by the
fix that made the case: the reviews kept reporting it, and reading the code
confirmed it. Expect more of that. A finding the suite does not list is worth
reading before it counts against a prompt, and one that holds up belongs here.

**Clean cases** are commits nothing later had to fix: #654 and #616. A high or
medium finding on one would have sent accepted work back. That they were
accepted does not prove they are sound, so read such findings before counting
them against a prompt.

The first run also reported that `init-hook-symlink` writes through a
dangling symlink, which the guard it adds does not cover. That one is not
listed until someone decides whether it is a bug.

The suite is kept small so a run is cheap: a few more of each kind are easy
to find in the history, and #636, fixed by #640, is a good bug case if its
54 KB patch is affordable.

Some of these bugs predate prompts in `.chunk/reviews` and may have shaped
them, so scores for those prompts may be flattering.
