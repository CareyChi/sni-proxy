package credentials

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"

	"golang.org/x/crypto/argon2"
)

const (
	credentialVersion  = 2
	algorithmArgon2id  = "argon2id"
	defaultMemory      = 64 * 1024 // KiB
	defaultTime        = 3
	defaultParallelism = 2
	defaultHashLength  = 32
	generatedLength    = 20

	legacyVersion    = 1
	legacyIterations = 210000
)

var usernamePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{3,64}$`)

type Record struct {
	Version     int    `json:"version"`
	Username    string `json:"username"`
	Algorithm   string `json:"algorithm,omitempty"`
	Memory      uint32 `json:"memory_kib,omitempty"`
	Time        uint32 `json:"time,omitempty"`
	Parallelism uint8  `json:"parallelism,omitempty"`
	Salt        string `json:"salt"`
	Hash        string `json:"hash"`

	// Iterations exists only so installations using the version-1 custom KDF
	// can authenticate long enough for an administrator to reset credentials.
	Iterations int `json:"iterations,omitempty"`
}

type Store struct {
	Path string
}

func NewStore(dataDir string) Store {
	return Store{Path: filepath.Join(dataDir, "credentials.json")}
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
		value, err := GeneratePassword(generatedLength)
		if err != nil {
			return "", err
		}
		password, generated = value, value
	}
	if len(password) < 10 || len(password) > 256 {
		return "", errors.New("password must be 10-256 bytes")
	}
	salt := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return "", err
	}
	hash := argon2.IDKey([]byte(password), salt, defaultTime, defaultMemory, defaultParallelism, defaultHashLength)
	record := Record{
		Version: credentialVersion, Username: username, Algorithm: algorithmArgon2id,
		Memory: defaultMemory, Time: defaultTime, Parallelism: defaultParallelism,
		Salt: base64.RawStdEncoding.EncodeToString(salt), Hash: base64.RawStdEncoding.EncodeToString(hash),
	}
	if err := writeRecordAtomic(store.Path, record); err != nil {
		return "", err
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
	if err != nil || len(salt) < 16 || len(salt) > 64 {
		return false, errors.New("credential salt is corrupt")
	}
	want, err := base64.RawStdEncoding.DecodeString(record.Hash)
	if err != nil || len(want) < 16 || len(want) > 64 {
		return false, errors.New("credential hash is corrupt")
	}
	var got []byte
	switch record.Version {
	case credentialVersion:
		got = argon2.IDKey([]byte(password), salt, record.Time, record.Memory, record.Parallelism, uint32(len(want)))
	case legacyVersion:
		got = deriveLegacy(password, salt, record.Iterations)
	default:
		return false, errors.New("credential version is unsupported")
	}
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
	if decoder.Decode(&struct{}{}) != io.EOF {
		return Record{}, errors.New("credential record must contain exactly one JSON object")
	}
	if !usernamePattern.MatchString(record.Username) {
		return Record{}, errors.New("credential record is invalid")
	}
	switch record.Version {
	case credentialVersion:
		if record.Algorithm != algorithmArgon2id || record.Memory < 8*1024 || record.Memory > 256*1024 || record.Time < 1 || record.Time > 10 || record.Parallelism < 1 || record.Parallelism > 16 {
			return Record{}, errors.New("Argon2id credential parameters are invalid")
		}
	case legacyVersion:
		if record.Iterations < 100000 || record.Iterations > 1000000 {
			return Record{}, errors.New("legacy credential parameters are invalid")
		}
	default:
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

func deriveLegacy(password string, salt []byte, iterations int) []byte {
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
	reference, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		reference, err = os.Stat(filepath.Dir(path))
	}
	if err != nil {
		return err
	}
	// A root CLI reset must not turn the daemon-readable credential file into
	// a root-owned file. Preserve the existing file owner, or for first-time
	// initialization inherit the service-owned data directory's owner.
	if err := matchOwnership(file, reference); err != nil {
		return err
	}
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
