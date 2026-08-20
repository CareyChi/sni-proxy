package credentials

import "testing"

func TestResetAndVerify(t *testing.T) {
	store := NewStore(t.TempDir())
	password, err := store.Initialize("admin", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(password) != 10 {
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
}
