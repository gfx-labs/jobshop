# Test socket paths (JOB-217, 2026-11-16)

A unix socket path is limited to 108 bytes on Linux (104 on macOS). An
agent session's scratch `TMPDIR` is longer, so `t.TempDir()` plus a
socket name made `bind` fail with `invalid argument` in the cli tests.

`controltest.ShortDir(t)` (pkg/orchestration/control/controltest) makes a
private directory of at most 50 bytes, removed at cleanup. It uses
`TMPDIR` when a temp dir under it fits, else `/tmp`. `controltest.NewRoot`
uses it, and the cli tests that listen on a socket (`gh_test.go`,
`linear_refresh_test.go`) take their socket path from it. Production socket
paths are unchanged.

Use it for any test directory that a socket is bound in. No `TMPDIR`
override is needed: `TMPDIR=<150 byte path> go test ./pkg/transport/cli`
passes.

Not covered: tests inside package `control` (internal, so they cannot
import controltest) still call `os.MkdirTemp("", "js")` and follow `TMPDIR`.
