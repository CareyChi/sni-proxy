package credentials

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
)

const (
	credentialVersion = 1
	defaultIterations = 210000
)

var usernamePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{3,64}$`)

type Record struct {
	Version    int    `json:"version"`
	Username   string `json:"username"`
	Salt       string `json:"salt"`
	Hash       string `json:"hash"`
	Iterations int    `json:"iterations"`
}

type Store struct {
	Path       string
	MirrorPath string
}

func NewStore(dataDir string) Store {
	return Store{
		Path:       filepath.Join(dataDir, "credentials.json"),
		MirrorPath: filepath.Join(dataDir, "credentials.mirror.json"),
	}
}

func (store Store) Initialize(username, password string) (string, error) {
	if _, err := os.Stat(store.Path); err == nil {
		return "", errors.New("administrator credentials already exist")
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if username == "" {
		username = "admin"
	}
	return store.Reset(username, password)
}

// Reset returns a generated password only when password is empty. The clear
// text is never persisted and must be displayed to the operator only once.
func (store Store) Reset(username, password string) (string, error) {
	if !usernamePattern.MatchString(username) {
		return "", errors.New("username must be 3-64 letters, digits, dot, underscore, or hyphen")
	}
	generated := ""
	if password == "" {
		value, err := GeneratePassword(10)
		if err != nil {
			return "", err
		}
		password = value
		generated = value
	}
	if len(password) < 10 || len(password) > 256 {
		return "", errors.New("password must be 10-256 bytes")
	}
	salt := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return "", err
	}
	hash := derive(password, salt, defaultIterations)
	record := Record{
		Version:    credentialVersion,
		Username:   username,
		Salt:       base64.RawStdEncoding.EncodeToString(salt),
		Hash:       base64.RawStdEncoding.EncodeToString(hash),
		Iterations: defaultIterations,
	}
	if err := writeRecordAtomic(store.Path, record); err != nil {
		return "", err
	}
	if store.MirrorPath != "" {
		if err := writeRecordAtomic(store.MirrorPath, record); err != nil {
			return "", fmt.Errorf("primary credential updated but mirror sync failed: %w", err)
		}
	}
	return generated, nil
}

func (store Store) Username() (string, error) {
	record, err := store.load()
	if err != nil {
		return "", err
	}
	return record.Username, nil
}

func (store Store) Verify(username, password string) (bool, error) {
	record, err := store.load()
	if err != nil {
		return false, err
	}
	salt, err := base64.RawStdEncoding.DecodeString(record.Salt)
	if err != nil {
		return false, errors.New("credential salt is corrupt")
	}
	want, err := base64.RawStdEncoding.DecodeString(record.Hash)
	if err != nil {
		return false, errors.New("credential hash is corrupt")
	}
	got := derive(password, salt, record.Iterations)
	userEqual := subtle.ConstantTimeCompare([]byte(record.Username), []byte(username))
	hashEqual := subtle.ConstantTimeCompare(want, got)
	return userEqual&hashEqual == 1, nil
}

func (store Store) load() (Record, error) {
	file, err := os.Open(store.Path)
	if err != nil {
		return Record{}, err
	}
	defer file.Close()
	var record Record
	decoder := json.NewDecoder(io.LimitReader(file, 256<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		return Record{}, err
	}
	if record.Version != credentialVersion || !usernamePattern.MatchString(record.Username) || record.Iterations < 100000 || record.Iterations > 1000000 {
		return Record{}, errors.New("credential record is invalid")
	}
	return record, nil
}

func GeneratePassword(length int) (string, error) {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789"
	if length < 10 || length > 256 {
		return "", errors.New("generated password length must be 10-256")
	}
	value := make([]byte, length)
	limit := byte(256 - (256 % len(alphabet)))
	for index := range value {
		for {
			candidate := []byte{0}
			if _, err := io.ReadFull(rand.Reader, candidate); err != nil {
				return "", err
			}
			if candidate[0] < limit {
				value[index] = alphabet[int(candidate[0])%len(alphabet)]
				break
			}
		}
	}
	return string(value), nil
}

func derive(password string, salt []byte, iterations int) []byte {
	initial := sha256.New()
	initial.Write(salt)
	initial.Write([]byte(password))
	value := initial.Sum(nil)
	for index := 1; index < iterations; index++ {
		digest := sha256.New()
		digest.Write(value)
		digest.Write(salt)
		value = digest.Sum(value[:0])
	}
	return value
}

func writeRecordAtomic(path string, record Record) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".credentials-*")
	if err != nil {
		return err
	}
	temporary := file.Name()
	ok := false
	defer func() {
		file.Close()
		if !ok {
			os.Remove(temporary)
		}
	}()
	if err := file.Chmod(0o600); err != nil {
		return err
	}
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(record); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		return err
	}
	ok = true
	return nil
}
