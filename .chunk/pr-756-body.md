## Summary

- **The bug:** syncs from a Mac sent `harness/.venv` to the sidecar. macOS's built-in `rsync` (openrsync) drops a `.gitignore`'s last line when it has no trailing newline, and `uv` writes `.venv/.gitignore` as just `*`. So the sync's `--filter=:- .gitignore` never excluded it.
- **The effect:** every run on the sidecar found a venv built for a macOS Python, removed it and downloaded ~100 MB of packages again. That took `lint` to 53s against a 60s timeout, and the factory's lint check timed out on a fresh sidecar ("the implementer stopped changing the code…").
- **The fix:** the sync now also excludes the paths `git ls-files --ignored` lists, as `RsyncPull` already does. This also covers `.git/info/exclude` and the global excludes file, which rsync never read. Excluded paths are kept on the sidecar, so its own venv survives the next sync.
- **Also:** `lint`'s timeout goes from 60s to 300s, like the other gates. From cold caches `task lint` takes ~110s (golangci-lint alone 97s).

| `lint` on a sidecar | time |
|---|---|
| Before | 53.2s, venv rebuilt every run |
| After, first run | 20.8s, removes the stale Mac venv once |
| After, every run after | **9.6s** |

## Test plan

**Automated**
- [x] New test runs the real local `rsync` with the sync's flags against a repo with uv's newline-less `.venv/.gitignore`: the venv isn't sent, the sidecar's own copy is kept, and other files still sync
- [x] Confirmed with plain openrsync that `--filter=:- .gitignore` alone sends a file under a `*` `.gitignore` with no newline, and leaves it out with one
- [x] `task test` and `task lint` pass

**Manual**
- [x] Before: `chunk validate --remote --cmd` showed the Mac's `pyvenv.cfg` on the sidecar
- [x] After: `chunk validate lint --remote` twice: 20.8s, then 9.6s with no venv rebuild
- [ ] `chunk factory build` passes its lint check on a fresh sidecar
