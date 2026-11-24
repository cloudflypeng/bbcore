// Package state persists accounts, cookies and caches under the data dir.
package state

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Account is one logged-in Bilibili account.
type Account struct {
	Mid          int64             `json:"mid"`
	Name         string            `json:"name,omitempty"`
	Avatar       string            `json:"avatar,omitempty"`
	Cookies      map[string]string `json:"cookies"`
	AccessKey    string            `json:"access_key,omitempty"`
	RefreshToken string            `json:"refresh_token,omitempty"`
	Buvid        string            `json:"buvid,omitempty"`
	AddedAt      int64             `json:"added_at"`
	CheckedAt    int64             `json:"checked_at,omitempty"`
}

// Anonymous holds the device cookies used before login.
type Anonymous struct {
	Cookies   map[string]string `json:"cookies"`
	UpdatedAt int64             `json:"updated_at,omitempty"`
}

// Data is the persisted state document.
type Data struct {
	Version   int                 `json:"version"`
	ActiveMid int64               `json:"active_mid"`
	Accounts  map[string]*Account `json:"accounts"`
	Anonymous *Anonymous          `json:"anonymous"`
}

// Store is a concurrency-safe JSON-file store.
type Store struct {
	mu   sync.RWMutex
	dir  string
	path string
	data Data
}

// Open loads (or creates) the state document inside dir.
func Open(dir string) (*Store, error) {
	if dir == "" {
		return nil, fmt.Errorf("data dir is required")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	s := &Store{dir: dir, path: filepath.Join(dir, "state.json")}
	s.data = Data{
		Version:   1,
		Accounts:  map[string]*Account{},
		Anonymous: &Anonymous{Cookies: map[string]string{}},
	}
	raw, err := os.ReadFile(s.path)
	if err == nil {
		if err := json.Unmarshal(raw, &s.data); err != nil {
			return nil, fmt.Errorf("parse %s: %w", s.path, err)
		}
		if s.data.Accounts == nil {
			s.data.Accounts = map[string]*Account{}
		}
		if s.data.Anonymous == nil {
			s.data.Anonymous = &Anonymous{Cookies: map[string]string{}}
		}
		if s.data.Anonymous.Cookies == nil {
			s.data.Anonymous.Cookies = map[string]string{}
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read %s: %w", s.path, err)
	}
	return s, nil
}

// Dir returns the data directory.
func (s *Store) Dir() string { return s.dir }

func (s *Store) saveLocked() error {
	s.data.Version = 1
	raw, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// Save flushes to disk.
func (s *Store) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked()
}

// AnonymousCookies returns a copy of the anonymous cookie jar.
func (s *Store) AnonymousCookies() map[string]string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]string, len(s.data.Anonymous.Cookies))
	for k, v := range s.data.Anonymous.Cookies {
		out[k] = v
	}
	return out
}

// ActiveAccount returns the active account, or nil when anonymous.
func (s *Store) ActiveAccount() *Account {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.data.ActiveMid == 0 {
		return nil
	}
	return cloneAccount(s.data.Accounts[fmt.Sprintf("%d", s.data.ActiveMid)])
}

// ActiveMid returns the active account mid (0 = anonymous).
func (s *Store) ActiveMid() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.data.ActiveMid
}

// Get returns an account by mid.
func (s *Store) Get(mid int64) *Account {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneAccount(s.data.Accounts[fmt.Sprintf("%d", mid)])
}

// List returns accounts sorted by AddedAt.
func (s *Store) List() []*Account {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Account, 0, len(s.data.Accounts))
	for _, a := range s.data.Accounts {
		out = append(out, cloneAccount(a))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AddedAt < out[j].AddedAt })
	return out
}

// Upsert inserts or replaces an account and persists the store.
func (s *Store) Upsert(acc *Account) error {
	if acc == nil || acc.Mid == 0 {
		return fmt.Errorf("account mid must be non-zero")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if acc.AddedAt == 0 {
		if prev := s.data.Accounts[fmt.Sprintf("%d", acc.Mid)]; prev != nil {
			acc.AddedAt = prev.AddedAt
		} else {
			acc.AddedAt = time.Now().Unix()
		}
	}
	if acc.Cookies == nil {
		acc.Cookies = map[string]string{}
	}
	s.data.Accounts[fmt.Sprintf("%d", acc.Mid)] = cloneAccount(acc)
	return s.saveLocked()
}

// Delete removes an account; if it was active the store falls back to anonymous.
func (s *Store) Delete(mid int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data.Accounts, fmt.Sprintf("%d", mid))
	if s.data.ActiveMid == mid {
		s.data.ActiveMid = 0
	}
	return s.saveLocked()
}

// SetActive switches the default account (0 = anonymous).
func (s *Store) SetActive(mid int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if mid != 0 {
		if _, ok := s.data.Accounts[fmt.Sprintf("%d", mid)]; !ok {
			return fmt.Errorf("account %d not found", mid)
		}
	}
	s.data.ActiveMid = mid
	return s.saveLocked()
}

// SetCookies merges Set-Cookie values into the anonymous jar (mid=0) or an
// account jar and persists.
func (s *Store) SetCookies(mid int64, cookies []*http.Cookie) error {
	if len(cookies) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var jar map[string]string
	if mid == 0 {
		jar = s.data.Anonymous.Cookies
		s.data.Anonymous.UpdatedAt = time.Now().Unix()
	} else {
		acc := s.data.Accounts[fmt.Sprintf("%d", mid)]
		if acc == nil {
			return nil
		}
		if acc.Cookies == nil {
			acc.Cookies = map[string]string{}
		}
		jar = acc.Cookies
		acc.CheckedAt = time.Now().Unix()
	}
	changed := false
	for _, c := range cookies {
		if c == nil || c.Name == "" {
			continue
		}
		if c.MaxAge < 0 || (!c.Expires.IsZero() && c.Expires.Before(time.Now())) {
			if _, ok := jar[c.Name]; ok {
				delete(jar, c.Name)
				changed = true
			}
			continue
		}
		if jar[c.Name] != c.Value {
			jar[c.Name] = c.Value
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return s.saveLocked()
}

// MergeAnonymousCookies merges arbitrary cookie values into the anonymous jar.
func (s *Store) MergeAnonymousCookies(cookies map[string]string) error {
	if len(cookies) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	changed := false
	for k, v := range cookies {
		if s.data.Anonymous.Cookies[k] != v {
			s.data.Anonymous.Cookies[k] = v
			changed = true
		}
	}
	if !changed {
		return nil
	}
	s.data.Anonymous.UpdatedAt = time.Now().Unix()
	return s.saveLocked()
}

// SetBuvid stores the device id on an account.
func (s *Store) SetBuvid(mid int64, buvid string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	acc := s.data.Accounts[fmt.Sprintf("%d", mid)]
	if acc == nil || acc.Buvid == buvid {
		return nil
	}
	acc.Buvid = buvid
	return s.saveLocked()
}

func cloneAccount(a *Account) *Account {
	if a == nil {
		return nil
	}
	clone := *a
	clone.Cookies = make(map[string]string, len(a.Cookies))
	for k, v := range a.Cookies {
		clone.Cookies[k] = v
	}
	return &clone
}
