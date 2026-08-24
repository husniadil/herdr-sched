package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"

	"github.com/husniadil/herdr-sched/internal/codes"
)

// Secrets is every webhook's HMAC key, kept in a file of its OWN and never in
// the store document.
//
// Note 2 settles that the secret is shown once at creation and never listed
// again. Verifying an HMAC needs the key itself, so it cannot be hashed away —
// which leaves where it is kept as the only thing that can carry the rule. A
// redaction inside `dump` would be one line anybody could later forget; a file
// `dump` does not read cannot be forgotten. Every door renders the store
// document and nothing else, so no door can print a secret.
//
// The cost is that a trigger's row and its secret are two writes rather than
// one. The secret is written FIRST: a crash between them leaves a key with no
// trigger, which is inert and which `doctor` counts, rather than a webhook
// nobody can sign for.
type Secrets struct {
	// Path is the file the keys are written to. An empty path is in-memory,
	// which is what a test that does not care uses.
	Path string

	mu   sync.Mutex
	held map[string]string
}

// SecretsMode is what the file is created with. It holds the one thing in this
// plugin that is worth anything to anyone else (§3.5).
const SecretsMode = 0o600

// OpenSecrets reads the keys at path, or starts an empty set when there is no
// file yet.
func OpenSecrets(path string) (*Secrets, error) {
	s := &Secrets{Path: path, held: map[string]string{}}
	if path == "" {
		return s, nil
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, codes.Errorf(codes.Unavailable, "read the webhook secrets %s: %v", path, err)
	}
	if err := json.Unmarshal(raw, &s.held); err != nil {
		return nil, codes.Errorf(codes.Unavailable,
			"the webhook secrets %s are not readable JSON: %v", path, err)
	}
	if s.held == nil {
		s.held = map[string]string{}
	}
	return s, nil
}

// Get is one trigger's key. The second answer is false for a trigger that has
// none, which is a webhook whose key was lost rather than one that verifies
// anything that arrives.
func (s *Secrets) Get(id string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	secret, ok := s.held[id]
	return secret, ok
}

// IDs is every trigger that has a key, for the orphan count doctor reports.
func (s *Secrets) IDs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.held))
	for id := range s.held {
		out = append(out, id)
	}
	return out
}

// Set writes one trigger's key.
func (s *Secrets) Set(id, secret string) error {
	return s.update(func(held map[string]string) { held[id] = secret })
}

// Delete drops one trigger's key, for good. A key with no trigger left to use
// it is a secret kept for nothing.
func (s *Secrets) Delete(id string) error {
	return s.update(func(held map[string]string) { delete(held, id) })
}

func (s *Secrets) update(fn func(map[string]string)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := map[string]string{}
	for k, v := range s.held {
		next[k] = v
	}
	fn(next)
	if err := s.write(next); err != nil {
		return err
	}
	s.held = next
	return nil
}

// write puts the keys on disk whole, through a temp file in the same
// directory, the way the store document is written and for the same reason.
func (s *Secrets) write(held map[string]string) error {
	if s.Path == "" {
		return nil
	}
	body, err := json.MarshalIndent(held, "", "  ")
	if err != nil {
		return codes.Errorf(codes.Unexpected, "render the webhook secrets: %v", err)
	}
	body = append(body, '\n')
	dir := filepath.Dir(s.Path)
	tmp, err := os.CreateTemp(dir, filepath.Base(s.Path)+".*")
	if err != nil {
		return codes.Errorf(codes.Unavailable, "write the webhook secrets in %s: %v", dir, err)
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(SecretsMode); err != nil {
		tmp.Close()
		return codes.Errorf(codes.Unavailable, "restrict %s to this user: %v", tmp.Name(), err)
	}
	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		return codes.Errorf(codes.Unavailable, "write %s: %v", tmp.Name(), err)
	}
	if err := tmp.Close(); err != nil {
		return codes.Errorf(codes.Unavailable, "close %s: %v", tmp.Name(), err)
	}
	if err := os.Rename(tmp.Name(), s.Path); err != nil {
		return codes.Errorf(codes.Unavailable, "put the webhook secrets at %s: %v", s.Path, err)
	}
	return nil
}
