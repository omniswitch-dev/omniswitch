package vault

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/omniswitch-dev/omniswitch/internal/store"
)

// Vault manages encrypted provider API keys, mapping virtual keys to real
// provider credentials. This allows developers to use "vk-team-frontend"
// style keys without ever seeing the actual provider API keys.
type Vault struct {
	store     *store.Store
	masterKey []byte // 32-byte AES-256 key derived from passphrase
}

// VirtualKey represents a stored virtual key mapping.
type VirtualKey struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	ProviderType string            `json:"provider_type"`
	ProviderName string            `json:"provider_name"`
	BaseURL      string            `json:"base_url,omitempty"`
	CreatedAt    time.Time         `json:"created_at"`
	UpdatedAt    time.Time         `json:"updated_at"`
	Enabled      bool              `json:"enabled"`
	Metadata     map[string]string `json:"metadata,omitempty"`
	// ProviderKey is only populated on creation (never returned after).
	ProviderKey string `json:"provider_key,omitempty"`
}

// New creates a new Vault. The passphrase is used to derive the AES-256 encryption key.
// If passphrase is empty, a random key is generated (keys won't survive restarts).
func New(st *store.Store, passphrase string) *Vault {
	var masterKey []byte
	if passphrase == "" {
		masterKey = make([]byte, 32)
		if _, err := rand.Read(masterKey); err != nil {
			// Fallback to deterministic key (not secure, but functional).
			h := sha256.Sum256([]byte("sentinel-default-key"))
			masterKey = h[:]
		}
	} else {
		h := sha256.Sum256([]byte(passphrase))
		masterKey = h[:]
	}
	return &Vault{store: st, masterKey: masterKey}
}

// Store stores a virtual key mapping with the provider key encrypted.
func (v *Vault) Store(ctx context.Context, vk VirtualKey) error {
	encrypted, err := v.encrypt(vk.ProviderKey)
	if err != nil {
		return fmt.Errorf("encrypt provider key: %w", err)
	}
	return v.store.InsertVirtualKey(ctx, store.VirtualKeyRecord{
		ID:           vk.ID,
		Name:         vk.Name,
		ProviderType: vk.ProviderType,
		ProviderName: vk.ProviderName,
		BaseURL:      vk.BaseURL,
		EncryptedKey: encrypted,
		CreatedAt:    vk.CreatedAt,
		UpdatedAt:    vk.UpdatedAt,
		Enabled:      vk.Enabled,
		MetadataJSON: marshalMeta(vk.Metadata),
	})
}

// Resolve looks up a virtual key by name and returns the decrypted provider API key.
func (v *Vault) Resolve(ctx context.Context, name string) (providerKey string, providerType string, baseURL string, err error) {
	record, err := v.store.GetVirtualKey(ctx, name)
	if err != nil {
		return "", "", "", fmt.Errorf("virtual key %q not found: %w", name, err)
	}
	if !record.Enabled {
		return "", "", "", fmt.Errorf("virtual key %q is disabled", name)
	}
	decrypted, err := v.decrypt(record.EncryptedKey)
	if err != nil {
		return "", "", "", fmt.Errorf("decrypt provider key: %w", err)
	}
	return decrypted, record.ProviderType, record.BaseURL, nil
}

// List returns all virtual keys (without decrypted provider keys).
func (v *Vault) List(ctx context.Context) ([]VirtualKey, error) {
	records, err := v.store.ListVirtualKeys(ctx)
	if err != nil {
		return nil, err
	}
	keys := make([]VirtualKey, len(records))
	for i, r := range records {
		keys[i] = VirtualKey{
			ID:           r.ID,
			Name:         r.Name,
			ProviderType: r.ProviderType,
			ProviderName: r.ProviderName,
			BaseURL:      r.BaseURL,
			CreatedAt:    r.CreatedAt,
			UpdatedAt:    r.UpdatedAt,
			Enabled:      r.Enabled,
			Metadata:     parseMeta(r.MetadataJSON),
		}
	}
	return keys, nil
}

// Rotate replaces the provider key for an existing virtual key.
func (v *Vault) Rotate(ctx context.Context, name, newProviderKey string) error {
	encrypted, err := v.encrypt(newProviderKey)
	if err != nil {
		return fmt.Errorf("encrypt provider key: %w", err)
	}
	return v.store.RotateVirtualKey(ctx, name, encrypted)
}

// Revoke disables a virtual key without deleting it.
func (v *Vault) Revoke(ctx context.Context, name string) error {
	return v.store.DisableVirtualKey(ctx, name)
}

func (v *Vault) encrypt(plaintext string) (string, error) {
	block, err := aes.NewCipher(v.masterKey)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	ciphertext := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return hex.EncodeToString(ciphertext), nil
}

func (v *Vault) decrypt(ciphertextHex string) (string, error) {
	ciphertext, err := hex.DecodeString(ciphertextHex)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(v.masterKey)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonceSize := gcm.NonceSize()
	if len(ciphertext) < nonceSize {
		return "", fmt.Errorf("ciphertext too short")
	}
	nonce, ciphertext := ciphertext[:nonceSize], ciphertext[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}

func marshalMeta(m map[string]string) string {
	if len(m) == 0 {
		return "{}"
	}
	data, _ := json.Marshal(m)
	return string(data)
}

func parseMeta(s string) map[string]string {
	s = strings.TrimSpace(s)
	if s == "" || s == "{}" {
		return nil
	}
	var m map[string]string
	_ = json.Unmarshal([]byte(s), &m)
	return m
}
