package agriculture

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"time"

	"yingce/backend/internal/agricanonical"
)

var ErrServiceIdentity = errors.New("agriculture: invalid service identity")

const (
	delegationType          = "weavesense-agri-delegation+jws"
	serviceAssertionType    = "weavesense-agri-service+jws"
	canonicalPayloadVersion = "rfc8785-safe-v1"
)

// Keys are loaded only from deployment configuration, never from token URLs.
// All mutable key byte slices are copied by NewServiceKeyring.
type ServiceIdentityKey struct {
	ID         string
	PublicKey  ed25519.PublicKey
	PrivateKey ed25519.PrivateKey
	NotBefore  time.Time
	NotAfter   time.Time
	Revoked    bool
}

type ServiceKeyring struct {
	keys      map[string]ServiceIdentityKey
	signingID string
}

func NewServiceKeyring(keys []ServiceIdentityKey, signingID string) (*ServiceKeyring, error) {
	if len(keys) == 0 || len(keys) > 32 {
		return nil, ErrServiceIdentity
	}
	out := &ServiceKeyring{keys: map[string]ServiceIdentityKey{}, signingID: signingID}
	for _, key := range keys {
		if !ownershipID.MatchString(key.ID) || len(key.PublicKey) != ed25519.PublicKeySize || key.NotBefore.IsZero() || !key.NotAfter.After(key.NotBefore) {
			return nil, ErrServiceIdentity
		}
		if _, exists := out.keys[key.ID]; exists {
			return nil, ErrServiceIdentity
		}
		if len(key.PrivateKey) > 0 && (len(key.PrivateKey) != ed25519.PrivateKeySize || !bytes.Equal(key.PrivateKey.Public().(ed25519.PublicKey), key.PublicKey)) {
			return nil, ErrServiceIdentity
		}
		if len(key.PrivateKey) > 0 && !bytes.Equal(ed25519.NewKeyFromSeed(key.PrivateKey.Seed()), key.PrivateKey) {
			return nil, ErrServiceIdentity
		}
		key.PublicKey = append(ed25519.PublicKey(nil), key.PublicKey...)
		key.PrivateKey = append(ed25519.PrivateKey(nil), key.PrivateKey...)
		out.keys[key.ID] = key
	}
	if signingID != "" {
		key, ok := out.keys[signingID]
		if !ok || len(key.PrivateKey) != ed25519.PrivateKeySize || key.Revoked {
			return nil, ErrServiceIdentity
		}
	}
	return out, nil
}

type compactHeader struct {
	Alg   string `json:"alg"`
	Type  string `json:"typ"`
	KeyID string `json:"kid"`
}

func strictIdentityJSON(raw []byte, target any, max int) error {
	if len(raw) == 0 || len(raw) > max {
		return ErrServiceIdentity
	}
	// The shared bounded parser also rejects duplicate decoded keys, invalid
	// Unicode and numeric ambiguities that encoding/json alone would accept.
	if _, err := agricanonical.Canonicalize(raw); err != nil {
		return ErrServiceIdentity
	}
	// encoding/json matches struct fields case-insensitively even with
	// DisallowUnknownFields. Validate the exact JSON tags recursively first so
	// ALG/alg or tenant_id/Tenant_ID cannot alias the same protocol field.
	var value any
	shapeDecoder := json.NewDecoder(bytes.NewReader(raw))
	shapeDecoder.UseNumber()
	if shapeDecoder.Decode(&value) != nil || !identityExactJSONKeys(value, reflect.TypeOf(target)) {
		return ErrServiceIdentity
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(target) != nil {
		return ErrServiceIdentity
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return ErrServiceIdentity
	}
	return nil
}

func identityExactJSONKeys(value any, typ reflect.Type) bool {
	if typ == nil {
		return false
	}
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if value == nil {
		return false
	}
	switch typ.Kind() {
	case reflect.Struct:
		object, ok := value.(map[string]any)
		if !ok {
			return false
		}
		fields := map[string]reflect.Type{}
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			if field.PkgPath != "" {
				continue
			}
			name := strings.Split(field.Tag.Get("json"), ",")[0]
			if name == "-" {
				continue
			}
			if name == "" || field.Anonymous {
				return false
			}
			fields[name] = field.Type
		}
		for key, nested := range object {
			fieldType, ok := fields[key]
			if !ok || !identityExactJSONKeys(nested, fieldType) {
				return false
			}
		}
		return true
	case reflect.Slice, reflect.Array:
		array, ok := value.([]any)
		if !ok {
			return false
		}
		for _, nested := range array {
			if !identityExactJSONKeys(nested, typ.Elem()) {
				return false
			}
		}
		return true
	case reflect.String:
		_, ok := value.(string)
		return ok
	case reflect.Bool:
		_, ok := value.(bool)
		return ok
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Float32, reflect.Float64:
		_, ok := value.(json.Number)
		return ok
	default:
		return false
	}
}

func decodeIdentitySegment(segment string) ([]byte, error) {
	if segment == "" {
		return nil, ErrServiceIdentity
	}
	for i := 0; i < len(segment); i++ {
		c := segment[i]
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return nil, ErrServiceIdentity
		}
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(segment)
	if err != nil || base64.RawURLEncoding.EncodeToString(raw) != segment {
		return nil, ErrServiceIdentity
	}
	return raw, nil
}

func (r *ServiceKeyring) signingKey(now, expiry time.Time) (ServiceIdentityKey, error) {
	if r == nil {
		return ServiceIdentityKey{}, ErrServiceIdentity
	}
	key, ok := r.keys[r.signingID]
	if !ok || key.Revoked || len(key.PrivateKey) != ed25519.PrivateKeySize || now.Before(key.NotBefore) || !now.Before(key.NotAfter) || expiry.After(key.NotAfter) {
		return ServiceIdentityKey{}, ErrServiceIdentity
	}
	return key, nil
}

func (r *ServiceKeyring) sign(typ string, claims any, now, expiry time.Time) (string, error) {
	if typ != delegationType && typ != serviceAssertionType {
		return "", ErrServiceIdentity
	}
	key, err := r.signingKey(now, expiry)
	if err != nil {
		return "", err
	}
	header, _ := json.Marshal(compactHeader{Alg: "EdDSA", Type: typ, KeyID: key.ID})
	payload, err := json.Marshal(claims)
	if err != nil || len(payload) > 128<<10 {
		return "", ErrServiceIdentity
	}
	encoded := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	token := encoded + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(key.PrivateKey, []byte(encoded)))
	if len(token) > 192<<10 {
		return "", ErrServiceIdentity
	}
	return token, nil
}

func (r *ServiceKeyring) verify(token, typ string, now time.Time, target any) (ServiceIdentityKey, error) {
	if r == nil || len(token) > 192<<10 {
		return ServiceIdentityKey{}, ErrServiceIdentity
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return ServiceIdentityKey{}, ErrServiceIdentity
	}
	decode := decodeIdentitySegment
	headerBytes, err := decode(parts[0])
	if err != nil {
		return ServiceIdentityKey{}, ErrServiceIdentity
	}
	var header compactHeader
	if strictIdentityJSON(headerBytes, &header, 2<<10) != nil || header.Alg != "EdDSA" || header.Type != typ {
		return ServiceIdentityKey{}, ErrServiceIdentity
	}
	key, ok := r.keys[header.KeyID]
	if !ok || key.Revoked || now.Before(key.NotBefore) || !now.Before(key.NotAfter) {
		return ServiceIdentityKey{}, ErrServiceIdentity
	}
	signature, err := decode(parts[2])
	if err != nil || len(signature) != ed25519.SignatureSize || !ed25519.Verify(key.PublicKey, []byte(parts[0]+"."+parts[1]), signature) {
		return ServiceIdentityKey{}, ErrServiceIdentity
	}
	payload, err := decode(parts[1])
	if err != nil || strictIdentityJSON(payload, target, 128<<10) != nil {
		return ServiceIdentityKey{}, ErrServiceIdentity
	}
	return key, nil
}

func identityTime(value string) (time.Time, error) {
	t, err := time.Parse("2006-01-02T15:04:05Z", value)
	if err != nil || t.Format("2006-01-02T15:04:05Z") != value {
		return time.Time{}, ErrServiceIdentity
	}
	return t, nil
}

func validateIdentityTimes(issued, expires string, now time.Time, key ServiceIdentityKey) error {
	i, err := identityTime(issued)
	if err != nil {
		return err
	}
	e, err := identityTime(expires)
	if err != nil {
		return err
	}
	if !e.After(i) || e.Sub(i) > 30*time.Second || i.After(now.Add(5*time.Second)) || !now.Before(e) || i.Before(key.NotBefore) || e.After(key.NotAfter) {
		return ErrServiceIdentity
	}
	return nil
}
