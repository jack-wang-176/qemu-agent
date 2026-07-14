package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
)

func (s *Session) SnapShot() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sessionPath := getSessionPath()
	if err := os.MkdirAll(sessionPath, 0755); err != nil {
		//todo add log deal
		return err
	}
	filePath := path.Join(sessionPath, fmt.Sprintf("%s.json", s.Id))
	data, err := json.MarshalIndent(s, "", " ")
	if err != nil {
		//todo add log deal
		return err
	}
	if err = os.WriteFile(filePath, data, 0644); err != nil {
		return err
	}
	return nil
}

func Load(id string) (*Session, error) {
	filePath := path.Join(getSessionPath(), fmt.Sprintf("%s.json", id))
	data, err := os.ReadFile(filePath)
	if err != nil {
		//todo add log deal
		return nil, err
	}
	var s Session
	err = json.Unmarshal(data, &s)
	if err != nil {
		//todo add log deal
		return nil, err
	}
	return &s, nil
}

func getSessionPath() string {
	filePath, err := os.UserHomeDir()
	if err != nil {
		fullPath := path.Join(os.TempDir(), "qemu-agent", "session")
		//todo add log deal
		return fullPath
	}
	return path.Join(filePath, "qemu-agent", "session")
}
