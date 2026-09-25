package identity

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/lestrrat-go/jwx/v2/jwa"
	"github.com/lestrrat-go/jwx/v2/jwk"
	"github.com/lestrrat-go/jwx/v2/jwt"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
)

// localKMS is the dev stand-in for a KMS or Vault Transit key (05 "Identity").
// The private key is stored by the migrator-owned table and used only to sign;
// production swaps this type for a client whose private key never leaves the KMS.
type localKMS struct {
	pool *pgxpool.Pool
	now  func() time.Time
}

type signingKey struct {
	kid  string
	priv jwk.Key
	pub  jwk.Key
}

func (k *localKMS) signer(ctx context.Context) (signingKey, error) {
	var kid string
	var pkcs8 []byte
	err := k.pool.QueryRow(ctx, `SELECT kid, private_pkcs8 FROM erp.identity_signing_keys WHERE retired_at IS NULL ORDER BY created_at DESC LIMIT 1`).Scan(&kid, &pkcs8)
	if err == pgx.ErrNoRows {
		return k.generate(ctx)
	}
	if err != nil {
		return signingKey{}, err
	}
	return keyFromPKCS8(kid, pkcs8)
}

func (k *localKMS) generate(ctx context.Context) (signingKey, error) {
	raw, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return signingKey{}, err
	}
	pkcs8, err := x509.MarshalPKCS8PrivateKey(raw)
	if err != nil {
		return signingKey{}, err
	}
	sk, err := keyFromRaw(raw)
	if err != nil {
		return signingKey{}, err
	}
	pubJSON, err := json.Marshal(sk.pub)
	if err != nil {
		return signingKey{}, err
	}
	p := rls.Principal{UserID: "system", CompanyID: platformCompanyID, Roles: []string{"system"}}
	err = rls.Tx(ctx, k.pool, sqlPrincipal(p), func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO erp.identity_signing_keys(kid, public_jwk, private_pkcs8) VALUES ($1,$2,$3)`, sk.kid, pubJSON, pkcs8)
		return err
	})
	if err != nil {
		return signingKey{}, err
	}
	return sk, nil
}

func keyFromPKCS8(kid string, pkcs8 []byte) (signingKey, error) {
	parsed, err := x509.ParsePKCS8PrivateKey(pkcs8)
	if err != nil {
		return signingKey{}, err
	}
	raw, ok := parsed.(*ecdsa.PrivateKey)
	if !ok {
		return signingKey{}, fmt.Errorf("signing key %s is not ECDSA", kid)
	}
	sk, err := keyFromRaw(raw)
	if err != nil {
		return signingKey{}, err
	}
	sk.kid = kid
	if err := sk.priv.Set(jwk.KeyIDKey, kid); err != nil {
		return signingKey{}, err
	}
	if err := sk.pub.Set(jwk.KeyIDKey, kid); err != nil {
		return signingKey{}, err
	}
	return sk, nil
}

func keyFromRaw(raw *ecdsa.PrivateKey) (signingKey, error) {
	priv, err := jwk.FromRaw(raw)
	if err != nil {
		return signingKey{}, err
	}
	pub, err := jwk.PublicKeyOf(priv)
	if err != nil {
		return signingKey{}, err
	}
	sum, err := pub.Thumbprint(crypto.SHA256)
	if err != nil {
		return signingKey{}, err
	}
	kid := base64.RawURLEncoding.EncodeToString(sum)
	if err := priv.Set(jwk.KeyIDKey, kid); err != nil {
		return signingKey{}, err
	}
	if err := priv.Set(jwk.AlgorithmKey, jwa.ES256); err != nil {
		return signingKey{}, err
	}
	if err := pub.Set(jwk.KeyIDKey, kid); err != nil {
		return signingKey{}, err
	}
	if err := pub.Set(jwk.AlgorithmKey, jwa.ES256); err != nil {
		return signingKey{}, err
	}
	return signingKey{kid: kid, priv: priv, pub: pub}, nil
}

func (k *localKMS) sign(ctx context.Context, tok jwt.Token) (string, error) {
	sk, err := k.signer(ctx)
	if err != nil {
		return "", err
	}
	signed, err := jwt.Sign(tok, jwt.WithKey(jwa.ES256, sk.priv))
	if err != nil {
		return "", err
	}
	return string(signed), nil
}

func (k *localKMS) keySet(ctx context.Context) (jwk.Set, error) {
	rows, err := k.pool.Query(ctx, `SELECT public_jwk FROM erp.identity_signing_keys WHERE retired_at IS NULL OR retired_at > $1`, k.now().Add(-15*time.Minute))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	set := jwk.NewSet()
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		key, err := jwk.ParseKey(raw)
		if err != nil {
			return nil, err
		}
		if err := set.AddKey(key); err != nil {
			return nil, err
		}
	}
	return set, rows.Err()
}

func (k *localKMS) jwks(ctx context.Context) (json.RawMessage, error) {
	set, err := k.keySet(ctx)
	if err != nil {
		return nil, err
	}
	b, err := json.Marshal(set)
	return b, err
}

func (s *Service) parseAccess(ctx context.Context, raw string) (jwt.Token, error) {
	set, err := s.kms.keySet(ctx)
	if err != nil {
		return nil, err
	}
	return jwt.Parse([]byte(raw), jwt.WithKeySet(set), jwt.WithValidate(false), jwt.WithIssuer(s.issuer), jwt.WithAudience(s.audience))
}
