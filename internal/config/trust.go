package config

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
)

type UpdateTrust struct {
	Repository       string `json:"repository"`
	Ed25519PublicKey string `json:"ed25519_public_key"`
}

func LoadUpdateTrust(path string) (UpdateTrust, []byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return UpdateTrust{}, nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o022 != 0 || !ownedByRoot(info) {
		return UpdateTrust{}, nil, errors.New("update trust policy must be a root-owned regular file and not group/world writable")
	}
	file, err := os.Open(path)
	if err != nil {
		return UpdateTrust{}, nil, err
	}
	defer file.Close()
	var policy UpdateTrust
	decoder := json.NewDecoder(io.LimitReader(file, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&policy); err != nil {
		return UpdateTrust{}, nil, err
	}
	if policy.Repository != OfficialUpdateRepository {
		return UpdateTrust{}, nil, errors.New("update trust repository does not match the compiled trust root")
	}
	publicKey, err := base64.RawStdEncoding.DecodeString(policy.Ed25519PublicKey)
	if err != nil || len(publicKey) != ed25519.PublicKeySize {
		return UpdateTrust{}, nil, errors.New("update trust policy has no valid Ed25519 public key")
	}
	return policy, publicKey, nil
}
