// Package lock serializes operations that mutate an installation.
package lock

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/gofrs/flock"
)

// Lock is an advisory per-application update lock.
type Lock struct {
	file *flock.Flock
}

// Acquire waits up to wait for an exclusive lock in dir.
func Acquire(ctx context.Context, dir string, wait time.Duration) (*Lock, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create lock directory: %w", err)
	}
	path := filepath.Join(dir, "update.lock")
	f := flock.New(path)

	lockCtx := ctx
	if wait > 0 {
		var cancel context.CancelFunc
		lockCtx, cancel = context.WithTimeout(ctx, wait)
		defer cancel()
	}
	ok, err := f.TryLockContext(lockCtx, 200*time.Millisecond)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if lockCtx.Err() != nil {
			return nil, fmt.Errorf("another update is already running (lock %s)", path)
		}
		return nil, fmt.Errorf("acquire update lock: %w", err)
	}
	if !ok {
		return nil, fmt.Errorf("another update is already running (lock %s)", path)
	}
	return &Lock{file: f}, nil
}

// Release unlocks the file.
func (l *Lock) Release() error {
	if l == nil || l.file == nil {
		return nil
	}
	return l.file.Unlock()
}
