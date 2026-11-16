package controltest

import (
	"os"
	"testing"
)

// maxShortDir is the longest directory ShortDir returns. A unix socket
// path is limited to 108 bytes on Linux (104 on macOS); a controller root
// adds sessions/<id>/cli.sock (about 40) below its dir, so a dir up to 50
// leaves room under 100.
const maxShortDir = 50

// fallbackBase is where ShortDir goes when TMPDIR is too long.
const fallbackBase = "/tmp"

// tempSuffix is the longest name os.MkdirTemp adds to the base with the
// prefix ShortDir uses: "/" + "js" + up to 10 random digits.
const tempSuffix = 13

// ShortDir makes a private directory whose path is short enough to hold
// unix sockets, removed when the test ends. It lives under TMPDIR, or
// under /tmp when TMPDIR is so long (an agent session's scratch dir) that
// bind would fail with "invalid argument". Use it, not t.TempDir, for any
// directory a test binds a socket in.
func ShortDir(t testing.TB) string {
	t.Helper()
	dir, err := os.MkdirTemp(shortBase(os.TempDir()), "js")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// shortBase returns tmp when a temp dir under it stays within
// maxShortDir, else fallbackBase.
func shortBase(tmp string) string {
	if len(tmp)+tempSuffix <= maxShortDir {
		return tmp
	}
	return fallbackBase
}
