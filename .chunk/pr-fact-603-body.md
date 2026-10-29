## Summary

FACT-603. An implementer turn hung for 25 minutes and left nothing to show why. This:

- Logs each implementer turn's exec command ID and sidecar
- Warns when a turn reports nothing for 5 minutes, naming its last step
- Tells the implementer not to start servers, watch modes or background processes
- Caps one Bash command at 5 minutes (Claude Code's default cap is 10)

## Test plan

- `task test` (with `-race`) and `task lint` pass
- New tests cover the command ID, the quiet warning and the command cap
- Ran a real `chunk factory build` with this branch: the command ID shows up as the turn starts, and the run passed after 1 round
  ```
  2026-10-26T20:55:12Z info  implementer: command 317262e7-48ec-4416-8771-657e8e7a3d6d on sidecar cde7b5e0-ff22-43a9-949b-469e532e7247
  2026-10-26T20:59:17Z end   result passed after 1 round(s)
  ```
