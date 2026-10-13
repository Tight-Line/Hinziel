// Package store keeps runs on the filesystem: one directory per run with its
// state (run.json), log and checkouts.
package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/Tight-Line/hinziel/run"
)

var (
	ErrNotFound = errors.New("run not found")
	ErrLocked   = errors.New("run is being executed by another process")
)

// FS is a run.Store rooted at a directory.
type FS struct {
	root string
}

var _ run.Store = (*FS)(nil)

// New opens (creating if needed) a store rooted at root.
func New(root string) (*FS, error) {
	if err := os.MkdirAll(filepath.Join(root, "runs"), 0o755); err != nil {
		return nil, err
	}
	return &FS{root: root}, nil
}

// NewID returns a new run ID that sorts by creation time.
func (s *FS) NewID(now time.Time) run.ID {
	b := make([]byte, 3)
	rand.Read(b)
	return run.ID(now.UTC().Format("20060102-150405") + "-" + hex.EncodeToString(b))
}

// Dir is the run's directory (checkouts, log, lock).
func (s *FS) Dir(id run.ID) string {
	return filepath.Join(s.root, "runs", string(id))
}

// LogPath is the run's log file.
func (s *FS) LogPath(id run.ID) string {
	return filepath.Join(s.Dir(id), "log.txt")
}

func (s *FS) statePath(id run.ID) string {
	return filepath.Join(s.Dir(id), "run.json")
}

// Save writes the run atomically, so readers never see a partial file.
func (s *FS) Save(_ context.Context, r *run.Run) error {
	dir := s.Dir(r.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "run.json.*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), s.statePath(r.ID))
}

// Load reads a run. A missing run returns ErrNotFound.
func (s *FS) Load(_ context.Context, id run.ID) (*run.Run, error) {
	data, err := os.ReadFile(s.statePath(id))
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%s: %w", id, ErrNotFound)
	}
	if err != nil {
		return nil, err
	}
	var r run.Run
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("%s: %w", id, err)
	}
	return &r, nil
}

// List returns matching runs, newest first.
func (s *FS) List(ctx context.Context, f run.Filter) ([]run.Snapshot, error) {
	entries, err := os.ReadDir(filepath.Join(s.root, "runs"))
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() > entries[j].Name() })

	var out []run.Snapshot
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		r, err := s.Load(ctx, run.ID(e.Name()))
		if errors.Is(err, ErrNotFound) {
			continue // directory without state yet
		}
		if err != nil {
			return nil, err
		}
		if !matches(r, f) {
			continue
		}
		out = append(out, r.Snapshot)
		if f.Limit > 0 && len(out) == f.Limit {
			break
		}
	}
	return out, nil
}

func matches(r *run.Run, f run.Filter) bool {
	if f.Factory != "" && r.Factory != f.Factory {
		return false
	}
	if f.Ticket != nil {
		t := r.Request.Ticket
		if t.ID != f.Ticket.ID || (f.Ticket.Tracker != "" && t.Tracker != f.Ticket.Tracker) {
			return false
		}
	}
	if f.EnvKey != "" && r.Request.EnvKey != f.EnvKey {
		return false
	}
	if len(f.Status) > 0 && !slices.Contains(f.Status, r.Status) {
		return false
	}
	return true
}

// ConfirmActions records that a human did these actions of the run. The
// process executing the run holds its lock, so a confirmation made meanwhile
// can't change run.json; it goes here, and the engine applies it before the
// next step (ConfirmedActions).
func (s *FS) ConfirmActions(id run.ID, actionIDs []string) error {
	f, err := os.OpenFile(s.confirmedPath(id), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	_, err = io.WriteString(f, strings.Join(actionIDs, "\n")+"\n")
	return errors.Join(err, f.Close())
}

// ConfirmedActions returns the IDs of actions confirmed with ConfirmActions.
func (s *FS) ConfirmedActions(id run.ID) ([]string, error) {
	b, err := os.ReadFile(s.confirmedPath(id))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return strings.Fields(string(b)), nil
}

func (s *FS) confirmedPath(id run.ID) string {
	return filepath.Join(s.Dir(id), "actions-confirmed")
}

// Lock takes an exclusive lock on the run for this process. It returns
// ErrLocked if another live process holds it. The lock is released by the
// returned function, or by the OS when the process exits, so a crashed run
// is never stuck as locked.
func (s *FS) Lock(id run.ID) (unlock func(), err error) {
	dir := s.Dir(id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, "lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("%s: %w", id, ErrLocked)
		}
		return nil, err
	}
	return func() {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}
