// Package access keeps the list of Telegram chats that may read the sensor data.
//
// It has no Telegram dependency, so the rules (who is an admin, who may be removed,
// what survives a restart) can be unit-tested without a bot.
package access

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"
)

var (
	// ErrProtected is returned when someone tries to remove an admin.
	ErrProtected = errors.New("chat is an admin (set in TELEGRAM_ADMIN_CHAT_IDS)")
	// ErrNotFound is returned when the chat is not in the dynamic list.
	ErrNotFound = errors.New("chat is not in the access list")
)

// Entry is a chat that was approved by an admin at runtime.
type Entry struct {
	ChatID  int64     `json:"chat_id"`
	Name    string    `json:"name"`
	AddedAt time.Time `json:"added_at"`
}

// fileFormat is what is stored on disk. The version lets us change the format later.
type fileFormat struct {
	Version int     `json:"version"`
	Users   []Entry `json:"users"`
}

// Store is safe for concurrent use.
type Store struct {
	path string // "" = keep everything in memory only
	now  func() time.Time

	mu     sync.RWMutex
	admins map[int64]struct{} // from TELEGRAM_ADMIN_CHAT_IDS, never removable from the bot
	users  map[int64]Entry    // approved at runtime, persisted in path
}

// NewStore loads the approved chats from path (a missing file means an empty list).
// A file that exists but cannot be parsed is an error: silently starting with an empty list
// and overwriting it later would throw away everybody's access.
func NewStore(path string, admins []int64) (*Store, error) {
	s := &Store{
		path:   path,
		now:    time.Now,
		admins: toSet(admins),
		users:  make(map[int64]Entry),
	}
	if path == "" {
		return s, nil
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create directory for access file: %w", err)
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read access file: %w", err)
	}
	var f fileFormat
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parse access file %s: %w", path, err)
	}
	for _, e := range f.Users {
		s.users[e.ChatID] = e
	}
	return s, nil
}

// IsAdmin reports whether the chat may approve and remove users.
func (s *Store) IsAdmin(chatID int64) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.admins[chatID]
	return ok
}

// IsAllowed reports whether the chat may read the sensor data (admins always may).
func (s *Store) IsAllowed(chatID int64) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.allowedLocked(chatID)
}

func (s *Store) allowedLocked(chatID int64) bool {
	if _, ok := s.admins[chatID]; ok {
		return true
	}
	_, ok := s.users[chatID]
	return ok
}

// Add approves a chat and saves the list. It reports false if the chat already had access.
// If saving fails the chat is not added, so memory and disk never disagree.
func (s *Store) Add(chatID int64, name string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.allowedLocked(chatID) {
		return false, nil
	}
	s.users[chatID] = Entry{ChatID: chatID, Name: name, AddedAt: s.now().UTC()}
	if err := s.saveLocked(); err != nil {
		delete(s.users, chatID)
		return false, err
	}
	return true, nil
}

// Remove revokes access of a chat that was approved at runtime.
func (s *Store) Remove(chatID int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.admins[chatID]; ok {
		return ErrProtected
	}
	old, ok := s.users[chatID]
	if !ok {
		return ErrNotFound
	}
	delete(s.users, chatID)
	if err := s.saveLocked(); err != nil {
		s.users[chatID] = old
		return err
	}
	return nil
}

// Users returns the chats approved at runtime, oldest first.
func (s *Store) Users() []Entry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.usersLocked()
}

func (s *Store) usersLocked() []Entry {
	out := slices.Collect(maps.Values(s.users))
	slices.SortFunc(out, func(a, b Entry) int {
		if c := a.AddedAt.Compare(b.AddedAt); c != 0 {
			return c
		}
		return cmpInt64(a.ChatID, b.ChatID)
	})
	return out
}

// Admins returns the admin chat IDs in ascending order.
func (s *Store) Admins() []int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return slices.Sorted(maps.Keys(s.admins))
}

// Recipients returns every chat that has access (used for "sensor offline" alerts), without duplicates.
func (s *Store) Recipients() []int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()

	all := make(map[int64]struct{}, len(s.admins)+len(s.users))
	for id := range s.admins {
		all[id] = struct{}{}
	}
	for id := range s.users {
		all[id] = struct{}{}
	}
	return slices.Sorted(maps.Keys(all))
}

// saveLocked writes the list atomically: a temporary file in the same directory is flushed to disk
// and then renamed over the real one, so a crash in the middle never leaves a half-written file.
func (s *Store) saveLocked() error {
	if s.path == "" {
		return nil
	}
	data, err := json.MarshalIndent(fileFormat{Version: 1, Users: s.usersLocked()}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode access list: %w", err)
	}

	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".access-*.tmp") // created with mode 0600
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("sync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		cleanup()
		return fmt.Errorf("replace access file: %w", err)
	}
	return nil
}

func toSet(ids []int64) map[int64]struct{} {
	m := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		m[id] = struct{}{}
	}
	return m
}

func cmpInt64(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}
