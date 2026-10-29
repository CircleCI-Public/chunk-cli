**Problem.** `chunk watch` gives a newly launched daemon 5s to answer `/ping`. But the daemon answered nothing until it had polled every project, which includes a CircleCI API call per org. When that took longer than 5s, `chunk watch` reported the daemon as unavailable, and every dashboard refresh after that launched another daemon, which failed and added 5s more.

**Fix.** The daemon now answers `/ping` as soon as it is listening and runs its first poll in the background. `/snapshot` waits for that poll, so the dashboard still opens with data. If the caller gives up first, it gets a 503.

Fixes FACT-650.

🤖 Generated with [Claude Code](https://claude.com/claude-code)
