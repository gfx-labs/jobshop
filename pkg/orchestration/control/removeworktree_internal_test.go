package control

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gfx-labs/jobshop/pkg/config"
	"github.com/gfx-labs/jobshop/pkg/domain"
)

// levelLog records the level of each message.
type levelLog struct {
	mu   sync.Mutex
	msgs map[string]slog.Level
}

func (l *levelLog) Enabled(context.Context, slog.Level) bool { return true }
func (l *levelLog) WithAttrs([]slog.Attr) slog.Handler       { return l }
func (l *levelLog) WithGroup(string) slog.Handler            { return l }
func (l *levelLog) Handle(_ context.Context, r slog.Record) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.msgs[r.Message] = r.Level
	return nil
}

// TestRemoveWorktreeLogLevel: removing a closed session's worktree is
// routine (Info); refusing an open ticket's worktree stays a Warn.
func TestRemoveWorktreeLogLevel(t *testing.T) {
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
	}
	repo := t.TempDir()
	git(repo, "init", "-q")
	git(repo, "commit", "-q", "--allow-empty", "-m", "init")

	root := config.Root{Dir: t.TempDir()}
	cfg := &config.Config{Pools: map[string]config.Pool{"dev": {Repo: repo}}}
	rec := &levelLog{msgs: map[string]slog.Level{}}
	c := &Controller{root: root, log: slog.New(rec)}

	tk := domain.Ticket{ID: "js-s-1", Pool: "dev", Status: domain.StatusOpen}
	work := c.sessionCwdIn(cfg, tk)
	require.NoError(t, os.MkdirAll(filepath.Dir(work), 0o755))
	git(repo, "worktree", "add", "-q", "--detach", work)

	c.removeWorktree(cfg, tk, "test")
	assert.Equal(t, slog.LevelWarn, rec.msgs["refusing to remove worktree of live session"])
	assert.NotContains(t, rec.msgs, "removing session worktree")
	assert.DirExists(t, work)

	tk.Status = domain.StatusClosed
	c.removeWorktree(cfg, tk, "test")
	assert.Equal(t, slog.LevelInfo, rec.msgs["removing session worktree"])
	assert.NotContains(t, rec.msgs, "git worktree remove", "the remove succeeded")
	assert.NoDirExists(t, work)
}
