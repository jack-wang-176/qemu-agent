package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/jack-wang-176/qemu-agent/internal/llm"
)

type Meta struct {
	ID        string
	TraceID   string
	ModelRef  llm.ModelRef
	UpdatedAt time.Time
}

type Store interface {
	Save(context.Context, *Session) error
	Load(context.Context, string) (*Session, error)
	Delete(context.Context, string) error
	List(context.Context) ([]Meta, error)
}

/* store direction for certain session.*/
type FileStore struct {
	dir string
}

func (f *FileStore) Delete(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if id == "" {
		return errors.New("invalid session id")
	}
	return os.Remove(filepath.Join(f.dir, id+".json"))
}

func (f *FileStore) List(ctx context.Context) ([]Meta, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(f.dir)
	if os.IsNotExist(err) {
		return []Meta{}, nil
	}
	if err != nil {
		return nil, err
	}
	result := make([]Meta, 0)
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		id := entry.Name()[:len(entry.Name())-len(filepath.Ext(entry.Name()))]
		s, err := f.Load(ctx, id)
		if err != nil {
			continue
		}
		result = append(result, Meta{ID: s.ID, TraceID: s.TraceID, ModelRef: s.ModelRef, UpdatedAt: s.UpdatedAt})
	}
	return result, nil
}

func NewFileStore(dir string) *FileStore {
	return &FileStore{
		dir: dir,
	}
}

/* save file into dir in filestore, write in tmp first, if err occure
 * then delete broken file and no real json produce
 */
func (f *FileStore) Save(ctx context.Context, s *Session) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s == nil || s.ID == "" {
		return errors.New("invalid session")
	}
	if err := os.MkdirAll(f.dir, 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal session: %w", err)
	}
	dst := filepath.Join(f.dir, s.ID+".json")
	tmp, err := os.CreateTemp(f.dir, s.ID+".json.tmp-*")
	if err != nil {
		return fmt.Errorf("create session temp file: %w", err)
	}
	tmpName := tmp.Name()
	committed := false
	defer func() {
		_ = tmp.Close()
		if !committed {
			_ = os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(0600); err != nil {
		return fmt.Errorf("set session temp permissions: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("write session temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync session temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close session temp file: %w", err)
	}
	if err := os.Rename(tmpName, dst); err != nil {
		return fmt.Errorf("commit session file: %w", err)
	}
	committed = true
	return nil
}

func (f *FileStore) Load(ctx context.Context, id string) (*Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if id == "" {
		return nil, errors.New("invalid session id")
	}
	data, err := os.ReadFile(filepath.Join(f.dir, id+".json"))
	if err != nil {
		return nil, err
	}
	var sess Session
	err = json.Unmarshal(data, &sess)
	if err != nil {
		return nil, err
	}
	return &sess, nil
}
