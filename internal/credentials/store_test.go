package credentials

import (
	"os"
	"testing"
)

func TestResetAndVerify(t *testing.T) {
	store := NewStore(t.TempDir())
	password, err := store.Initialize("admin", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(password) != generatedLength {
		t.Fatalf("generated password length = %d", len(password))
	}
	ok, err := store.Verify("admin", password)
	if err != nil || !ok {
		t.Fatalf("Verify = %v, %v", ok, err)
	}
	ok, err = store.Verify("admin", "incorrect-password")
	if err != nil || ok {
		t.Fatalf("incorrect password Verify = %v, %v", ok, err)
	}
	record, err := store.load()
	if err != nil {
		t.Fatal(err)
	}
	if record.Version != credentialVersion || record.Algorithm != algorithmArgon2id || record.Memory == 0 {
		t.Fatalf("unexpected credential record: %#v", record)
	}
}

func TestCredentialRecordRejectsTrailingJSON(t *testing.T) {
	store := NewStore(t.TempDir())
	if _, err := store.Initialize("admin", "correct-horse-battery-staple"); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(store.Path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("{}\n"); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Verify("admin", "correct-horse-battery-staple"); err == nil {
		t.Fatal("credential record with trailing JSON was accepted")
	}
}
