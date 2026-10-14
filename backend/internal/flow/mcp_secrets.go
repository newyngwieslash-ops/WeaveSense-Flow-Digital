package flow

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"yingce/backend/internal/model"

	"gorm.io/gorm"
)

// Tenant-saved MCP credentials (PRD §9.3 "租户配置自己的凭据", §10 凭据加密).
//
// Envelope encryption: each secret value is sealed with its own random 256-bit
// data key (AES-GCM); the data key is sealed with the key-encryption key from
// FLOW_SECRETS_KEK (base64 of 32 bytes). Associated data binds
// tenant/workspace/secret id, so a row copied to another tenant or secret does
// not decrypt. A connection references a secret as "secret:<id>"; operator
// env:/oauth: references keep working as the fallback.

const secretRefPrefix = "secret:"

var (
	ErrSecretNotFound = errors.New("flow: secret not found")
	ErrSecretKEK      = errors.New("flow: FLOW_SECRETS_KEK is not configured (base64 of 32 bytes)")
	ErrSecretInUse    = errors.New("flow: secret is referenced by a connection")
)

// SecretKEK is the platform key-encryption key and its id (sha256 prefix of the key).
type SecretKEK struct {
	Key []byte
	ID  string
}

// KEKFromEnv reads FLOW_SECRETS_KEK.
func KEKFromEnv() (SecretKEK, error) { return ParseKEK(os.Getenv("FLOW_SECRETS_KEK")) }

func ParseKEK(b64 string) (SecretKEK, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
	if err != nil || len(raw) != 32 {
		return SecretKEK{}, ErrSecretKEK
	}
	sum := sha256.Sum256(raw)
	return SecretKEK{Key: raw, ID: "kek_" + hex.EncodeToString(sum[:6])}, nil
}

func gcmSeal(key, plain, aad []byte) ([]byte, error) {
	blk, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	g, err := cipher.NewGCM(blk)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, g.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return g.Seal(nonce, nonce, plain, aad), nil
}

func gcmOpen(key, sealed, aad []byte) ([]byte, error) {
	blk, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	g, err := cipher.NewGCM(blk)
	if err != nil {
		return nil, err
	}
	if len(sealed) < g.NonceSize() {
		return nil, errors.New("flow: sealed value too short")
	}
	return g.Open(nil, sealed[:g.NonceSize()], sealed[g.NonceSize():], aad)
}

func secretAAD(tenantID, workspaceID, id string) []byte {
	return []byte("flow-mcp-secret\x00" + tenantID + "\x00" + workspaceID + "\x00" + id)
}

// SealSecret envelope-encrypts value for the given row identity.
func SealSecret(kek SecretKEK, tenantID, workspaceID, id, value string) (ciphertext, wrappedDEK []byte, err error) {
	dek := make([]byte, 32)
	if _, err = rand.Read(dek); err != nil {
		return nil, nil, err
	}
	aad := secretAAD(tenantID, workspaceID, id)
	if ciphertext, err = gcmSeal(dek, []byte(value), aad); err != nil {
		return nil, nil, err
	}
	if wrappedDEK, err = gcmSeal(kek.Key, dek, aad); err != nil {
		return nil, nil, err
	}
	return ciphertext, wrappedDEK, nil
}

// OpenSecret decrypts a stored secret row.
func OpenSecret(kek SecretKEK, row model.FlowMCPSecret) (string, error) {
	if row.KeyID != kek.ID {
		return "", fmt.Errorf("flow: secret %s was sealed with key %s, current key is %s", row.ID, row.KeyID, kek.ID)
	}
	aad := secretAAD(row.TenantID, row.WorkspaceID, row.ID)
	dek, err := gcmOpen(kek.Key, row.WrappedDEK, aad)
	if err != nil {
		return "", errors.New("flow: secret data key does not unwrap")
	}
	v, err := gcmOpen(dek, row.Ciphertext, aad)
	if err != nil {
		return "", errors.New("flow: secret does not decrypt")
	}
	return string(v), nil
}

// MCPSecrets is the tenant credential store.
type MCPSecrets struct {
	DB  *gorm.DB
	KEK func() (SecretKEK, error)
}

func (s MCPSecrets) kek() (SecretKEK, error) {
	if s.KEK != nil {
		return s.KEK()
	}
	return KEKFromEnv()
}

// MCPSecretInput is the write shape; Value is accepted but never echoed back.
type MCPSecretInput struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

func validSecretValue(v string) error {
	if strings.TrimSpace(v) == "" || len(v) > 8192 || strings.ContainsAny(v, "\r\n") {
		return fmt.Errorf("%w: secret value must be 1–8192 chars on one line", ErrMCPInvalid)
	}
	return nil
}

func (s MCPSecrets) Create(scope Scope, userID string, in MCPSecretInput) (model.FlowMCPSecret, error) {
	if err := validSecretValue(in.Value); err != nil {
		return model.FlowMCPSecret{}, err
	}
	kek, err := s.kek()
	if err != nil {
		return model.FlowMCPSecret{}, err
	}
	id := NewID("sec_")
	ct, wk, err := SealSecret(kek, scope.TenantID, scope.WorkspaceID, id, in.Value)
	if err != nil {
		return model.FlowMCPSecret{}, err
	}
	name := strings.TrimSpace(in.Name)
	if name == "" || len(name) > 120 {
		return model.FlowMCPSecret{}, fmt.Errorf("%w: name must be 1–120 chars", ErrMCPInvalid)
	}
	now := time.Now().UTC()
	row := model.FlowMCPSecret{ID: id, TenantID: scope.TenantID, WorkspaceID: scope.WorkspaceID, Name: name, Ciphertext: ct, WrappedDEK: wk,
		KeyID: kek.ID, Version: 1, CreatedBy: userID, CreatedAt: now, UpdatedAt: now}
	return row, s.DB.Create(&row).Error
}

func (s MCPSecrets) get(scope Scope, id string) (model.FlowMCPSecret, error) {
	var r model.FlowMCPSecret
	if err := s.DB.First(&r, "id = ? AND tenant_id = ? AND workspace_id = ?", id, scope.TenantID, scope.WorkspaceID).Error; err != nil {
		return r, ErrSecretNotFound
	}
	return r, nil
}

func (s MCPSecrets) List(scope Scope) ([]model.FlowMCPSecret, error) {
	var rows []model.FlowMCPSecret
	err := s.DB.Select("id", "tenant_id", "workspace_id", "name", "key_id", "version", "created_by", "created_at", "updated_at").
		Where("tenant_id = ? AND workspace_id = ?", scope.TenantID, scope.WorkspaceID).Order("created_at").Find(&rows).Error
	return rows, err
}

// Rotate replaces the value (new data key) and bumps the version; name may change.
func (s MCPSecrets) Rotate(scope Scope, id string, in MCPSecretInput) (model.FlowMCPSecret, error) {
	r, err := s.get(scope, id)
	if err != nil {
		return r, err
	}
	if err := validSecretValue(in.Value); err != nil {
		return r, err
	}
	kek, err := s.kek()
	if err != nil {
		return r, err
	}
	ct, wk, err := SealSecret(kek, r.TenantID, r.WorkspaceID, r.ID, in.Value)
	if err != nil {
		return r, err
	}
	upd := map[string]any{"ciphertext": ct, "wrapped_dek": wk, "key_id": kek.ID, "version": r.Version + 1, "updated_at": time.Now().UTC()}
	if n := strings.TrimSpace(in.Name); n != "" && len(n) <= 120 {
		upd["name"] = n
	}
	if err := s.DB.Model(&r).Updates(upd).Error; err != nil {
		return r, err
	}
	return s.get(scope, id)
}

// Delete refuses while a non-revoked connection still references the secret.
func (s MCPSecrets) Delete(scope Scope, id string) error {
	if _, err := s.get(scope, id); err != nil {
		return err
	}
	var n int64
	s.DB.Model(&model.FlowMCPConnection{}).Where("tenant_id = ? AND credential_ref = ? AND status <> ?", scope.TenantID, secretRefPrefix+id, "revoked").Count(&n)
	if n > 0 {
		return ErrSecretInUse
	}
	return s.DB.Where("id = ? AND tenant_id = ? AND workspace_id = ?", id, scope.TenantID, scope.WorkspaceID).Delete(&model.FlowMCPSecret{}).Error
}

// Resolve decrypts the secret a connection references; the secret must belong to
// the connection's own tenant and workspace.
func (s MCPSecrets) Resolve(c model.FlowMCPConnection) (string, error) {
	id, ok := strings.CutPrefix(c.CredentialRef, secretRefPrefix)
	if !ok {
		return "", fmt.Errorf("%w: not a secret reference", ErrMCPInvalid)
	}
	r, err := s.get(Scope{TenantID: c.TenantID, WorkspaceID: c.WorkspaceID}, id)
	if err != nil {
		return "", fmt.Errorf("%w: credential secret not found in this workspace", ErrMCPInvalid)
	}
	kek, err := s.kek()
	if err != nil {
		return "", err
	}
	return OpenSecret(kek, r)
}
