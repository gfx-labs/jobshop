package controltest

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestShortBase(t *testing.T) {
	assert.Equal(t, "/tmp", shortBase("/tmp"))
	assert.Equal(t, "/var/tmp", shortBase("/var/tmp"))
	long := "/" + strings.Repeat("a", 150)
	assert.Equal(t, fallbackBase, shortBase(long))
}

// TestShortDirLongTMPDIR: with a 150 byte TMPDIR, ShortDir still gives a
// directory a unix socket can bind in, and removes it at cleanup.
func TestShortDirLongTMPDIR(t *testing.T) {
	long := filepath.Join(t.TempDir(), strings.Repeat("d", 150))
	require.NoError(t, os.MkdirAll(long, 0o700))
	t.Setenv("TMPDIR", long)

	var dir string
	t.Run("use", func(t *testing.T) {
		dir = ShortDir(t)
		assert.LessOrEqual(t, len(dir), maxShortDir)
		ln, err := net.Listen("unix", filepath.Join(dir, "s"))
		require.NoError(t, err)
		require.NoError(t, ln.Close())
	})
	_, err := os.Stat(dir)
	assert.ErrorIs(t, err, os.ErrNotExist, "removed when the test ends")
}
