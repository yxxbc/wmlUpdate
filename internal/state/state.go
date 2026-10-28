// Package state persists updater bookkeeping: installed version, update
// history, backups and an in-flight install journal.
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

const schemaVersion = 1

// State is stored as state.json inside the app's state directory.
type State struct {
	SchemaVersion    int       `json:"schema_version"`
	AppID            string    `json:"app_id"`
	InstalledVersion string    `json:"installed_version,omitempty"`
	Channel          string    `json:"channel,omitempty"`
	LastCheck        time.Time `json:"last_check,omitempty"`
	LastUpdate       *Record   `json:"last_update,omitempty"`
	Backups          []Backup  `json:"backups,omitempty"`
	Pending          *Pending  `json:"pending,omitempty"`
}

// Record describes a successfully installed update.
type Record struct {
	Version         string    `json:"version"`
	PreviousVersion string    `json:"previous_version,omitempty"`
	InstalledAt     time.Time `json:"installed_at"`
	Strategy        string    `json:"strategy"`
	Artifact        string    `json:"artifact,omitempty"`
	SHA256          string    `json:"sha256,omitempty"`
}

// Backup describes a retained previous installation used by rollback.
type Backup struct {
	ID        string    `json:"id"`
	Path      string    `json:"path"`
	Target    string    `json:"target"`
	Version   string    `json:"version"`
	Strategy  string    `json:"strategy"`
	CreatedAt time.Time `json:"created_at"`
}

// Pending is the crash-recovery journal for an install transaction.
type Pending struct {
	ID       string    `json:"id"`
	Target   string    `json:"target"`
	Stage    string    `json:"stage,omitempty"`
	Backup   string    `json:"backup,omitempty"`
	Version  string    `json:"version"`
	Previous string    `json:"previous_version,omitempty"`
	Strategy string    `json:"strategy"`
	Phase    string    `json:"phase"`
	Started  time.Time `json:"started_at"`
}

// Store owns the state directory.
type Store struct {
	Dir string
}

func Open(dir string) (*Store, error) {
	if dir == "" {
		return nil, errors.New("state directory is empty")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create state directory: %w", err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(dir, 0o700); err != nil {
			return nil, fmt.Errorf("secure state directory permissions: %w", err)
		}
	}
	return &Store{Dir: dir}, nil
}

func (s *Store) Path() string { return filepath.Join(s.Dir, "state.json") }

// Subdir returns a subdirectory of the state directory, creating it.
func (s *Store) Subdir(name string) (string, error) {
	dir := filepath.Join(s.Dir, name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create %s directory: %w", name, err)
	}
	return dir, nil
}

// Load reads state. A missing file returns a fresh state.
func (s *Store) Load(appID string) (*State, error) {
	data, err := os.ReadFile(s.Path())
	if errors.Is(err, os.ErrNotExist) {
		return &State{SchemaVersion: schemaVersion, AppID: appID}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read state: %w", err)
	}
	var st State
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, fmt.Errorf("parse state %s: %w", s.Path(), err)
	}
	if st.SchemaVersion != schemaVersion {
		return nil, fmt.Errorf("unsupported state schema %d", st.SchemaVersion)
	}
	if st.AppID != "" && appID != "" && st.AppID != appID {
		return nil, fmt.Errorf("state belongs to app %q, not %q", st.AppID, appID)
	}
	if st.AppID == "" {
		st.AppID = appID
	}
	return &st, nil
}

// Save atomically writes state.
func (s *Store) Save(st *State) error {
	if st == nil {
		return errors.New("nil state")
	}
	st.SchemaVersion = schemaVersion
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("encode state: %w", err)
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(s.Dir, ".state-*.tmp")
	if err != nil {
		return fmt.Errorf("create state temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod state temp file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write state temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync state temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close state temp file: %w", err)
	}
	if err := os.Rename(tmpName, s.Path()); err != nil {
		return fmt.Errorf("replace state: %w", err)
	}
	syncDir(s.Dir)
	return nil
}

func syncDir(dir string) {
	f, err := os.Open(dir)
	if err != nil {
		return
	}
	_ = f.Sync()
	_ = f.Close()
}
