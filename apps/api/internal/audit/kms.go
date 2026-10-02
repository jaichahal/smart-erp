package audit

import (
	"crypto/ed25519"
	"crypto/sha256"
	"fmt"
)

// LocalKMS simulates an off-prem asymmetric key. Verify is public. Sign is
// granted only to the anchor worker identity.
type LocalKMS struct {
	keyID string
	priv  ed25519.PrivateKey
	pub   ed25519.PublicKey
	allow map[string]bool
}

const (
	// IdentityAnchorWorker may call Sign.
	IdentityAnchorWorker = "anchor-worker"
	// IdentityDBOperator is the database operator. Sign is denied.
	IdentityDBOperator = "database-operator"
)

// NewLocalKMS builds a key from a 32-byte seed.
func NewLocalKMS(keyID string, seed []byte) (*LocalKMS, error) {
	if len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("anchor signing seed must be %d bytes", ed25519.SeedSize)
	}
	priv := ed25519.NewKeyFromSeed(seed)
	pub, ok := priv.Public().(ed25519.PublicKey)
	if !ok {
		return nil, fmt.Errorf("ed25519 public key has unexpected type")
	}
	return &LocalKMS{
		keyID: keyID,
		priv:  priv,
		pub:   pub,
		allow: map[string]bool{IdentityAnchorWorker: true},
	}, nil
}

// KeyID is the public identifier stored on each anchor.
func (k *LocalKMS) KeyID() string { return k.keyID }

// PublicKey is the key anyone can use to verify an anchor.
func (k *LocalKMS) PublicKey() ed25519.PublicKey { return k.pub }

// Sign signs payload for identity. The database operator is denied.
func (k *LocalKMS) Sign(identity string, payload []byte) ([]byte, error) {
	if k == nil || !k.allow[identity] {
		return nil, fmt.Errorf("%w for identity %s", ErrSignDenied, identity)
	}
	return ed25519.Sign(k.priv, payload), nil
}

// Verify reports whether sig is a valid signature by this key.
func (k *LocalKMS) Verify(payload, sig []byte) bool {
	if k == nil {
		return false
	}
	return ed25519.Verify(k.pub, payload, sig)
}

// SeedFromString hashes an arbitrary secret into an ed25519 seed.
func SeedFromString(s string) []byte {
	sum := sha256.Sum256([]byte(s))
	return sum[:]
}
