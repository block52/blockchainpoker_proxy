package gateway

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"os"
	"sync"
)

// Creds are the web2 credentials we hold for one b52 user's blockchain.poker account.
type Creds struct {
	Name      string `json:"name"`
	AuthToken string `json:"authToken"`
	Secret    string `json:"secret"`
}

type CredStore interface {
	Get(addr string) (Creds, bool)
	Put(addr string, c Creds) error
}

// FileStore keeps all creds in one AES-256-GCM encrypted file. Key = SHA-256(passphrase).
type FileStore struct {
	mu   sync.Mutex
	path string
	aead cipher.AEAD
	m    map[string]Creds
}

func NewFileStore(path, passphrase string) (*FileStore, error) {
	if passphrase == "" {
		return nil, errors.New("gateway: empty store key")
	}
	k := sha256.Sum256([]byte(passphrase))
	blk, err := aes.NewCipher(k[:])
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(blk)
	if err != nil {
		return nil, err
	}
	s := &FileStore{path: path, aead: aead, m: map[string]Creds{}}
	raw, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return s, nil
	case err != nil:
		return nil, err
	}
	ns := aead.NonceSize()
	if len(raw) < ns {
		return nil, errors.New("gateway: store file truncated")
	}
	pt, err := aead.Open(nil, raw[:ns], raw[ns:], nil)
	if err != nil {
		return nil, errors.New("gateway: cannot decrypt store (wrong key?)")
	}
	return s, json.Unmarshal(pt, &s.m)
}

func (s *FileStore) Get(addr string) (Creds, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.m[addr]
	return c, ok
}

func (s *FileStore) Put(addr string, c Creds) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[addr] = c
	pt, _ := json.Marshal(s.m)
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	out := s.aead.Seal(nonce, nonce, pt, nil)
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, out, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// MemStore is for tests.
type MemStore struct {
	mu sync.Mutex
	M  map[string]Creds
}

func (m *MemStore) Get(a string) (Creds, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.M[a]
	return c, ok
}
func (m *MemStore) Put(a string, c Creds) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.M == nil {
		m.M = map[string]Creds{}
	}
	m.M[a] = c
	return nil
}
