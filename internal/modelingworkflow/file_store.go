package modelingworkflow

import (
	"context"
	"crypto/md5"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type FileStore struct {
	dir string
}

func (s *FileStore) Load(ctx context.Context, key BindingKey) (Binding, bool, error) {
	if err := ctx.Err(); err != nil {
		return Binding{}, false, err
	}
	hashKey, err := hashkeyCal(key)
	if err != nil {
		return Binding{}, false, err
	}
	data, err := os.ReadFile(filepath.Join(s.dir, hashKey+".json"))
	if err != nil {
		return Binding{}, false, err
	}
	var binding Binding
	err = json.Unmarshal(data, &binding)
	if err != nil {
		return Binding{}, false, err
	}
	return binding, true, nil
}

func (s *FileStore) CompareAndSave(ctx context.Context, bind Binding, expected int) (Binding, error) {
	if err := ctx.Err(); err != nil {
		return Binding{}, err
	}
	if err := os.MkdirAll(s.dir, 0700); err != nil {
		return Binding{}, err
	}
	data, err := json.MarshalIndent(bind, "", "  ")
	if err != nil {
		return Binding{}, fmt.Errorf("marshal fileStore; %w", err)
	}
	hashKey, err := hashkeyCal(bind.Key)
	if err := os.MkdirAll(s.dir, 0700); err != nil {
		return Binding{}, err
	}
	dst := filepath.Join(s.dir, hashKey+".json")
	tmp, err := os.CreateTemp(s.dir, hashKey+".json.tmp-*")
	if err != nil {
		return Binding{}, fmt.Errorf("marshal fileStore; %w", err)
	}
	tmpName := tmp.Name()
	commited := false
	defer func() {
		_ = tmp.Close()
		if !commited {
			_ = os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(0600); err != nil {
		return Binding{}, err
	}
	if _, err = tmp.Write(data); err != nil {
		return Binding{}, err
	}
	if err := tmp.Sync(); err != nil {
		return Binding{}, err
	}
	if err := tmp.Close(); err != nil {
		return Binding{}, err
	}
	if err := os.Rename(tmpName, dst); err != nil {
		return Binding{}, err
	}
	commited = true
	return bind, nil
}

func (s *FileStore) Delete(ctx context.Context, key BindingKey, expected int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	binding, flag, err := s.Load(ctx, key)
	if err != nil || !flag {
		return fmt.Errorf("no corresponding key: %w", err)
	}
	if binding.Version != expected {
		return fmt.Errorf("expected version not equal with binding version")
	}
	hashkey, err := hashkeyCal(key)
	if err != nil {
		return err
	}
	return os.Remove(filepath.Join(s.dir, hashkey+".json"))
}

func hashkeyCal(key BindingKey) (string, error) {
	byte, err := json.Marshal(key)
	if err != nil {
		return "", err
	}
	hash := md5.Sum(byte)
	return fmt.Sprintf("%x", hash), nil
}
