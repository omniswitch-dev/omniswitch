// Package onboarding owns the local first-run profile and setup experience.
package onboarding

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/omniswitch-dev/omniswitch/internal/gatewayconfig"
	"github.com/omniswitch-dev/omniswitch/internal/router"
)

const ProviderKeyEnv = "OMNISWITCH_SETUP_PROVIDER_KEY"

// Profile is local owner state, not a config served by a management API.
// Credentials are stored in a user-private file and never returned by Status.
type Profile struct {
	Version    int        `json:"version"`
	Listen     string     `json:"listen,omitempty"`
	AppKey     string     `json:"app_key"`
	GatewayKey string     `json:"gateway_key"`
	VaultKey   string     `json:"vault_key"`
	Connection Connection `json:"connection"`
}

type Connection struct {
	Provider string `json:"provider"`
	Key      string `json:"key,omitempty"`
	BaseURL  string `json:"base_url,omitempty"`
	Model    string `json:"model"`
}

func Directory() (string, error) {
	if dir := strings.TrimSpace(os.Getenv("OMNISWITCH_HOME")); dir != "" {
		return filepath.Abs(dir)
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "omniswitch"), nil
}

func NewProfile(connection Connection, previous *Profile) (Profile, error) {
	if previous != nil {
		p := *previous
		p.Connection = connection
		return p, nil
	}
	key, err := RandomToken()
	if err != nil {
		return Profile{}, err
	}
	vault, err := RandomToken()
	if err != nil {
		return Profile{}, err
	}
	app, err := RandomToken()
	if err != nil {
		return Profile{}, err
	}
	return Profile{Version: 1, AppKey: "sk-omniswitch-" + app, GatewayKey: "sk-omniswitch-" + key, VaultKey: vault, Connection: connection}, nil
}

func RandomToken() (string, error) {
	data := make([]byte, 32)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return hex.EncodeToString(data), nil
}

func Load(dir string) (*Profile, error) {
	path := filepath.Join(dir, "profile.json")
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("profile must be a regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var profile Profile
	if err := json.Unmarshal(data, &profile); err != nil {
		return nil, fmt.Errorf("cannot read local profile; run omniswitch start --setup to reconnect")
	}
	if profile.Version != 1 || profile.GatewayKey == "" || profile.AppKey == "" || profile.VaultKey == "" || profile.Connection.Model == "" {
		return nil, fmt.Errorf("incomplete local profile; run omniswitch start --setup to reconnect")
	}
	if _, ok := findProvider(profile.Connection.Provider); !ok {
		return nil, fmt.Errorf("unknown saved provider")
	}
	if profile.Listen != "" {
		host, port, err := net.SplitHostPort(profile.Listen)
		number, numErr := strconv.Atoi(port)
		if err != nil || numErr != nil || host != "127.0.0.1" || number < 1 || number > 65535 {
			return nil, fmt.Errorf("local profile must listen on 127.0.0.1 with a valid port")
		}
	}
	return &profile, nil
}

func Save(dir string, profile Profile) error {
	data, err := json.MarshalIndent(profile, "", "  ")
	if err != nil {
		return err
	}
	return privateWrite(dir, "profile.json", append(data, '\n'))
}

// WriteConfig makes a normal GatewayConfig. Provider credentials stay in the
// private profile; startup injects the referenced env variable in-process.
func WriteConfig(dir string, profile Profile, listen string) (string, error) {
	auth, logPayloads := true, false
	connection := profile.Connection
	account := gatewayconfig.ProviderAccount{Name: "@primary", Type: connection.Provider, APIKeyEnv: ProviderKeyEnv, Models: []string{connection.Model}}
	if connection.Provider == "local" {
		account.Type = "custom"
		account.BaseURL = connection.BaseURL
	}
	cfg := gatewayconfig.Config{APIVersion: gatewayconfig.APIVersion, Kind: gatewayconfig.Kind,
		Gateway:   gatewayconfig.Gateway{Listen: listen, DataDir: filepath.Join(dir, "data"), Auth: &auth, LogPayloads: &logPayloads, CacheScope: "api_key"},
		Providers: []gatewayconfig.ProviderAccount{account},
		Routes:    map[string]router.Route{"default": {Variants: []router.Variant{{Provider: "@primary", Model: connection.Model, Weight: 100}}, MaxRetries: 2, Timeout: "60s"}},
	}
	if err := gatewayconfig.Validate(cfg); err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return "", err
	}
	if err := privateWrite(dir, "gateway.json", append(data, '\n')); err != nil {
		return "", err
	}
	return filepath.Join(dir, "gateway.json"), nil
}

func privateWrite(dir, name string, data []byte) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	// Do not follow a replaced profile/config symlink on write.
	target := filepath.Join(dir, name)
	if info, err := os.Lstat(target); err == nil && !info.Mode().IsRegular() {
		return fmt.Errorf("refusing to replace non-regular %s", name)
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	f, err := os.CreateTemp(dir, ".setup-*")
	if err != nil {
		return err
	}
	temporary := f.Name()
	defer os.Remove(temporary)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(temporary, target)
}
