package agriculture

import (
	"crypto/ed25519"
	"encoding/base64"
	"io"
	"os"
)

type serviceKeyConfig struct {
	ID          string `json:"kid"`
	PublicKey   string `json:"public_key"`
	PrivateSeed string `json:"private_seed,omitempty"`
	NotBefore   string `json:"not_before"`
	NotAfter    string `json:"not_after"`
	Revoked     bool   `json:"revoked"`
}

type serviceIdentityConfig struct {
	GISBaseURL           string             `json:"gis_base_url"`
	PlatformSigningKeyID string             `json:"platform_signing_kid"`
	PlatformKeys         []serviceKeyConfig `json:"platform_keys"`
	GISKeys              []serviceKeyConfig `json:"gis_keys"`
}

func configuredKeyring(config []serviceKeyConfig, signingID string, verifyOnly bool) (*ServiceKeyring, error) {
	keys := make([]ServiceIdentityKey, 0, len(config))
	for _, entry := range config {
		public, err := base64.StdEncoding.Strict().DecodeString(entry.PublicKey)
		if err != nil {
			return nil, ErrServiceIdentity
		}
		before, err := identityTime(entry.NotBefore)
		if err != nil {
			return nil, ErrServiceIdentity
		}
		after, err := identityTime(entry.NotAfter)
		if err != nil {
			return nil, ErrServiceIdentity
		}
		var private ed25519.PrivateKey
		if entry.PrivateSeed != "" {
			if verifyOnly {
				return nil, ErrServiceIdentity
			}
			seed, err := base64.StdEncoding.Strict().DecodeString(entry.PrivateSeed)
			if err != nil || len(seed) != ed25519.SeedSize {
				return nil, ErrServiceIdentity
			}
			private = ed25519.NewKeyFromSeed(seed)
		}
		keys = append(keys, ServiceIdentityKey{ID: entry.ID, PublicKey: public, PrivateKey: private, NotBefore: before, NotAfter: after, Revoked: entry.Revoked})
	}
	return NewServiceKeyring(keys, signingID)
}

func loadServiceIdentity(path string) (*GISClient, *ServiceKeyring, error) {
	// Configuration failures expose neither file paths nor malformed secret data.
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, ErrServiceIdentity
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, (64<<10)+1))
	if err != nil {
		return nil, nil, ErrServiceIdentity
	}
	var config serviceIdentityConfig
	if strictIdentityJSON(raw, &config, 64<<10) != nil || config.PlatformSigningKeyID == "" {
		return nil, nil, ErrServiceIdentity
	}
	platformKeys, err := configuredKeyring(config.PlatformKeys, config.PlatformSigningKeyID, false)
	if err != nil {
		return nil, nil, ErrServiceIdentity
	}
	gisKeys, err := configuredKeyring(config.GISKeys, "", true)
	if err != nil {
		return nil, nil, ErrServiceIdentity
	}
	// Each direction has independent key material, not merely different labels.
	for _, platform := range platformKeys.keys {
		for _, gis := range gisKeys.keys {
			if platform.PublicKey.Equal(gis.PublicKey) {
				return nil, nil, ErrServiceIdentity
			}
		}
	}
	client, err := NewGISClient(config.GISBaseURL, platformKeys)
	if err != nil {
		return nil, nil, ErrServiceIdentity
	}
	return client, gisKeys, nil
}
