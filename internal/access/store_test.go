package access

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"
)

const (
	admin int64 = 100
	guest int64 = 300
)

func newTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "data", "access.json")
	s, err := NewStore(path, []int64{admin})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return s, path
}

func TestIsAllowedAndIsAdmin(t *testing.T) {
	s, _ := newTestStore(t)

	if !s.IsAdmin(admin) || s.IsAdmin(guest) {
		t.Error("only the configured admin must be an admin")
	}
	if !s.IsAllowed(admin) {
		t.Error("admins must be allowed")
	}
	if s.IsAllowed(guest) {
		t.Error("unknown chat must not be allowed")
	}
}

func TestAddAndRemove(t *testing.T) {
	s, _ := newTestStore(t)

	added, err := s.Add(guest, "Wife")
	if err != nil || !added {
		t.Fatalf("Add = (%v, %v), want (true, nil)", added, err)
	}
	if !s.IsAllowed(guest) {
		t.Fatal("added chat must be allowed")
	}

	// Adding again, or adding someone who already has access, is a no-op.
	for _, id := range []int64{guest, admin} {
		if added, err := s.Add(id, "again"); err != nil || added {
			t.Errorf("Add(%d) = (%v, %v), want (false, nil)", id, added, err)
		}
	}

	if err := s.Remove(guest); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if s.IsAllowed(guest) {
		t.Error("removed chat must lose access")
	}
	if err := s.Remove(guest); !errors.Is(err, ErrNotFound) {
		t.Errorf("second Remove error = %v, want ErrNotFound", err)
	}
}

func TestAdminCannotBeRemoved(t *testing.T) {
	s, _ := newTestStore(t)
	if err := s.Remove(admin); !errors.Is(err, ErrProtected) {
		t.Errorf("Remove(admin) error = %v, want ErrProtected", err)
	}
	if !s.IsAllowed(admin) {
		t.Error("admin lost access")
	}
}

func TestPersistenceAcrossRestart(t *testing.T) {
	s, path := newTestStore(t)
	if _, err := s.Add(guest, "Wife"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add(-1001, "Family group"); err != nil {
		t.Fatal(err)
	}
	if err := s.Remove(-1001); err != nil {
		t.Fatal(err)
	}

	// A new Store on the same file must see exactly the same state.
	s2, err := NewStore(path, []int64{admin})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if !s2.IsAllowed(guest) || s2.IsAllowed(-1001) {
		t.Errorf("state after restart wrong: guest=%v removed=%v", s2.IsAllowed(guest), s2.IsAllowed(-1001))
	}
	users := s2.Users()
	if len(users) != 1 || users[0].ChatID != guest || users[0].Name != "Wife" {
		t.Errorf("Users after restart = %+v", users)
	}
}

func TestFileHasPrivateModeAndNoTempLeftovers(t *testing.T) {
	s, path := newTestStore(t)
	if _, err := s.Add(guest, "Wife"); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("file mode = %o, want 600", perm)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("directory has %d entries, want only access.json", len(entries))
	}
}

func TestCorruptedFileIsAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "access.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(path, nil); err == nil {
		t.Fatal("corrupted file must not be silently replaced by an empty list")
	}
	// The broken file must be left untouched for a human to look at.
	data, _ := os.ReadFile(path)
	if string(data) != "{not json" {
		t.Errorf("file was modified: %q", data)
	}
}

func TestMissingFileMeansEmptyList(t *testing.T) {
	s, _ := newTestStore(t)
	if len(s.Users()) != 0 {
		t.Errorf("Users = %v, want empty", s.Users())
	}
}

func TestFailedSaveRollsBack(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "access.json")
	s, err := NewStore(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add(guest, "Wife"); err != nil {
		t.Fatal(err)
	}

	// Make the directory unusable: replace it with a regular file so that creating a temp file fails.
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	if added, err := s.Add(400, "Friend"); err == nil || added {
		t.Fatalf("Add = (%v, %v), want an error", added, err)
	}
	if s.IsAllowed(400) {
		t.Error("chat must not be allowed when the list could not be saved")
	}
	if err := s.Remove(guest); err == nil {
		t.Fatal("Remove must fail when the list could not be saved")
	}
	if !s.IsAllowed(guest) {
		t.Error("chat must keep access when the removal could not be saved")
	}
}

func TestInMemoryStoreWithoutPath(t *testing.T) {
	s, err := NewStore("", []int64{admin})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add(guest, "Wife"); err != nil || !s.IsAllowed(guest) {
		t.Fatalf("in-memory Add failed: %v", err)
	}
}

func TestUsersAreSortedByAddTime(t *testing.T) {
	s, _ := newTestStore(t)
	clock := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return clock }

	// Added in non-sorted order; the same timestamp falls back to ordering by chat ID.
	for _, id := range []int64{30, 10, 20} {
		if _, err := s.Add(id, "u"); err != nil {
			t.Fatal(err)
		}
	}
	clock = clock.Add(time.Hour)
	if _, err := s.Add(5, "later"); err != nil {
		t.Fatal(err)
	}

	var got []int64
	for _, e := range s.Users() {
		got = append(got, e.ChatID)
	}
	if want := []int64{10, 20, 30, 5}; !slices.Equal(got, want) {
		t.Errorf("order = %v, want %v", got, want)
	}
}

func TestRecipientsHasNoDuplicates(t *testing.T) {
	// The same admin ID is listed twice, and later approved again as a guest: it must appear once.
	s, err := NewStore("", []int64{admin, admin})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add(guest, "Wife"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add(admin, "again"); err != nil {
		t.Fatal(err)
	}
	if got, want := s.Recipients(), []int64{admin, guest}; !slices.Equal(got, want) {
		t.Errorf("Recipients = %v, want %v", got, want)
	}
}

// Run with -race: many goroutines add, remove and read at once.
func TestConcurrentUse(t *testing.T) {
	s, path := newTestStore(t)

	var wg sync.WaitGroup
	for i := int64(0); i < 20; i++ {
		wg.Add(1)
		go func(id int64) {
			defer wg.Done()
			chat := 1000 + id
			if _, err := s.Add(chat, "u"); err != nil {
				t.Errorf("Add: %v", err)
			}
			_ = s.IsAllowed(chat)
			_ = s.Recipients()
			if id%2 == 0 {
				if err := s.Remove(chat); err != nil {
					t.Errorf("Remove: %v", err)
				}
			}
		}(i)
	}
	wg.Wait()

	// 10 odd IDs remain, and the file on disk agrees with memory.
	s2, err := NewStore(path, []int64{admin})
	if err != nil {
		t.Fatal(err)
	}
	if got := len(s2.Users()); got != 10 {
		t.Errorf("users on disk = %d, want 10", got)
	}
}
