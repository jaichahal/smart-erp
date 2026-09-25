package audit

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// ErrImmutableObject means a compliance-mode object cannot be deleted or overwritten.
var ErrImmutableObject = errors.New("object locked in compliance mode")

// ErrSignDenied means this identity is not allowed to call Sign.
var ErrSignDenied = errors.New("sign denied")

// ErrRetentionBlocked means pruning was refused because the latest backup is not a verified success.
var ErrRetentionBlocked = errors.New("retention prune refused until a verified backup succeeds")

// ObjectStore is the on-prem or off-site bucket. PutCompliance writes once with a retain-until.
type ObjectStore interface {
	PutCompliance(ctx context.Context, key string, body []byte, retainUntil time.Time) error
	Get(ctx context.Context, key string) ([]byte, error)
	Delete(ctx context.Context, key string) error
	Exists(ctx context.Context, key string) (bool, error)
}

type memObj struct {
	body  []byte
	until time.Time
}

// memStore is the in-process compliance bucket used when MinIO is not reachable (CI).
type memStore struct {
	mu   sync.Mutex
	objs map[string]memObj
}

func newMemStore() *memStore {
	return &memStore{objs: map[string]memObj{}}
}

// PutCompliance stores a body and refuses a second write while retention is in force.
func (m *memStore) PutCompliance(_ context.Context, key string, body []byte, retainUntil time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if cur, ok := m.objs[key]; ok && time.Now().Before(cur.until) {
		return ErrImmutableObject
	}
	cp := append([]byte(nil), body...)
	m.objs[key] = memObj{body: cp, until: retainUntil}
	return nil
}

// Get returns a copy of the stored object.
func (m *memStore) Get(_ context.Context, key string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	o, ok := m.objs[key]
	if !ok {
		return nil, os.ErrNotExist
	}
	return append([]byte(nil), o.body...), nil
}

// Delete removes an object whose retention has expired.
func (m *memStore) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	o, ok := m.objs[key]
	if !ok {
		return os.ErrNotExist
	}
	if time.Now().Before(o.until) {
		return ErrImmutableObject
	}
	delete(m.objs, key)
	return nil
}

// Exists reports whether the key is present.
func (m *memStore) Exists(_ context.Context, key string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.objs[key]
	return ok, nil
}

// corrupt flips a stored byte. Tests use it to simulate an off-site copy that does not match.
func (m *memStore) corrupt(key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	o, ok := m.objs[key]
	if !ok || len(o.body) == 0 {
		return
	}
	o.body[0] ^= 0xff
	m.objs[key] = o
}

// DirStore is a write-once directory. The manifest is renamed into place so an
// interrupted write leaves only a ".partial" file.
type DirStore struct {
	root string
	mu   sync.Mutex
}

// NewDirStore returns a directory-backed store.
func NewDirStore(root string) *DirStore {
	return &DirStore{root: root}
}

func (d *DirStore) path(key string) string {
	return filepath.Join(d.root, filepath.FromSlash(key))
}

// PutCompliance writes the object by rename and refuses an overwrite while retention is in force.
func (d *DirStore) PutCompliance(_ context.Context, key string, body []byte, retainUntil time.Time) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	full := d.path(key)
	if _, err := os.Stat(full); err == nil {
		until, rerr := os.ReadFile(full + ".retain")
		if rerr != nil {
			return ErrImmutableObject
		}
		t, perr := time.Parse(time.RFC3339, string(until))
		if perr == nil && time.Now().Before(t) {
			return ErrImmutableObject
		}
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		return err
	}
	tmp := full + ".partial"
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(full+".retain", []byte(retainUntil.UTC().Format(time.RFC3339)), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, full)
}

// Get reads the object.
func (d *DirStore) Get(_ context.Context, key string) ([]byte, error) {
	return os.ReadFile(d.path(key))
}

// Delete removes an object whose retention has expired.
func (d *DirStore) Delete(_ context.Context, key string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	full := d.path(key)
	until, err := os.ReadFile(full + ".retain")
	if err == nil {
		t, perr := time.Parse(time.RFC3339, string(until))
		if perr == nil && time.Now().Before(t) {
			return ErrImmutableObject
		}
	}
	if err := os.Remove(full); err != nil {
		return err
	}
	_ = os.Remove(full + ".retain")
	return nil
}

// Exists reports whether the object file is present.
func (d *DirStore) Exists(_ context.Context, key string) (bool, error) {
	_, err := os.Stat(d.path(key))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}
