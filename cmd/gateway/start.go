package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/omniswitch-dev/omniswitch/internal/onboarding"
	"github.com/omniswitch-dev/omniswitch/internal/store"
)

func prepareManagedStart(args []string, stdout, stderr io.Writer) (bool, int) {
	flags := flag.NewFlagSet("omniswitch start", flag.ContinueOnError)
	flags.SetOutput(stderr)
	noBrowser := flags.Bool("no-browser", false, "Print a local setup link instead of opening a browser")
	demo := flags.Bool("demo", false, "Create a simulated, no-key demo on first launch")
	setup := flags.Bool("setup", false, "Reconnect the provider in your saved local profile")
	port := flags.Int("port", 8080, "Local port (0 chooses an available port)")
	directory := flags.String("home", "", "Directory for this local profile")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return false, 0
		}
		return false, 2
	}
	if flags.NArg() != 0 || *port < 0 || *port > 65535 {
		fmt.Fprintln(stderr, "start: use flags only and a port between 0 and 65535")
		return false, 2
	}
	dir := *directory
	if dir == "" {
		var err error
		dir, err = onboarding.Directory()
		if err != nil {
			fmt.Fprintln(stderr, "start: cannot locate your configuration directory")
			return false, 1
		}
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		fmt.Fprintln(stderr, "start: invalid profile directory")
		return false, 1
	}
	profile, err := onboarding.Load(dir)
	if err != nil && !os.IsNotExist(err) && !*setup {
		fmt.Fprintln(stderr, "start:", err)
		return false, 1
	}
	if *demo && profile != nil && profile.Connection.Provider != "demo" {
		fmt.Fprintln(stderr, "start: this profile has a real provider; use --home with another directory for a demo")
		return false, 2
	}
	listen := net.JoinHostPort("127.0.0.1", strconv.Itoa(*port))
	explicitPort := false
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "port" {
			explicitPort = true
		}
	})
	if !explicitPort && profile != nil && profile.Listen != "" {
		listen = profile.Listen
	}
	listener, err := net.Listen("tcp", listen)
	if err != nil {
		fmt.Fprintln(stderr, "start: local port is busy; stop the other gateway or use --port 8081")
		return false, 1
	}
	listen = listener.Addr().String()
	origin := "http://" + listen
	openedSetup := false
	if profile == nil && *demo {
		p, err := onboarding.NewProfile(onboarding.Connection{Provider: "demo", Model: "demo-model"}, nil)
		if err != nil {
			_ = listener.Close()
			fmt.Fprintln(stderr, "start: could not create demo profile")
			return false, 1
		}
		profile = &p
	} else if profile == nil || *setup {
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		wizard, err := onboarding.NewServer(dir, origin, profile, os.Getenv)
		if err != nil {
			stop()
			_ = listener.Close()
			fmt.Fprintln(stderr, "start: could not initialize setup")
			return false, 1
		}
		server := &http.Server{Handler: wizard, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 30 * time.Second}
		failures := make(chan error, 1)
		go func() { failures <- server.Serve(listener) }()
		fmt.Fprintln(stdout, "Welcome to OmniSwitch. Choose a provider to get started.")
		fmt.Fprintln(stdout, "Setup:", wizard.Link())
		if !*noBrowser {
			openedSetup = openBrowser(wizard.Link()) == nil
		}
		select {
		case p := <-wizard.Done:
			profile = &p
		case <-ctx.Done():
			_ = server.Close()
			stop()
			return false, 0
		case serveErr := <-failures:
			_ = serveErr
			_ = server.Close()
			stop()
			fmt.Fprintln(stderr, "start: setup server stopped")
			return false, 1
		}
		shutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_ = server.Shutdown(shutdown)
		cancel()
		stop()
	} else {
		_ = listener.Close()
	}
	// The demo path has not started an HTTP server, so release its reservation.
	_ = listener.Close()
	profile.Listen = listen
	if err := onboarding.Save(dir, *profile); err != nil {
		fmt.Fprintln(stderr, "start: could not save your local profile")
		return false, 1
	}
	config, err := onboarding.WriteConfig(dir, *profile, listen)
	if err != nil {
		fmt.Fprintln(stderr, "start: could not write gateway configuration")
		return false, 1
	}
	// Managed starts use their own SQLite state and settings. Advanced server
	// environment/config behavior remains available through explicit `serve`.
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(name, "OMNISWITCH_") {
			_ = os.Unsetenv(name)
		}
	}
	for name, value := range map[string]string{"OMNISWITCH_CONFIG": config, "OMNISWITCH_AUTH": "true", "OMNISWITCH_LISTEN": listen, "OMNISWITCH_DATA": filepath.Join(dir, "data"), "OMNISWITCH_BOOTSTRAP_API_KEY": profile.GatewayKey, "OMNISWITCH_VAULT_KEY": profile.VaultKey, onboarding.ProviderKeyEnv: profile.Connection.Key, "OMNISWITCH_SETUP_APP_KEY": profile.AppKey} {
		if err := os.Setenv(name, value); err != nil {
			fmt.Fprintln(stderr, "start: could not initialize managed settings")
			return false, 1
		}
	}
	fmt.Fprintln(stdout, "Gateway:", origin)
	fmt.Fprintln(stdout, "Model: default · Provider:", profile.Connection.Provider)
	if profile.Connection.Provider == "demo" {
		fmt.Fprintln(stdout, "Demo mode: simulated responses, no provider calls or charges.")
	}
	fmt.Fprintln(stdout, "Next launch: omniswitch · Change provider: omniswitch start --setup")
	if !openedSetup && !*noBrowser {
		go openWhenReady(origin, profile.GatewayKey)
	}
	return true, 0
}

func openWhenReady(origin, key string) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	client := &http.Client{Timeout: time.Second}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			request, err := http.NewRequestWithContext(ctx, http.MethodGet, origin+"/readyz", nil)
			if err != nil {
				return
			}
			response, err := client.Do(request)
			if err == nil {
				_ = response.Body.Close()
				if response.StatusCode == 200 {
					_ = openBrowser(origin + "/#omniswitch-key=" + key)
					return
				}
			}
		}
	}
}

func openBrowser(target string) error {
	var command *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		command = exec.Command("open", target)
	case "windows":
		command = exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	default:
		command = exec.Command("xdg-open", target)
	}
	return command.Run()
}

func ensureManagedAppKey(ctx context.Context, st *store.Store, settings runtimeSettings) error {
	raw := os.Getenv("OMNISWITCH_SETUP_APP_KEY")
	if raw == "" {
		return nil
	}
	if !settings.authEnabled {
		return fmt.Errorf("managed app keys require authentication")
	}
	hash := sha256.Sum256([]byte(raw))
	keyHash := hex.EncodeToString(hash[:])
	if _, err := st.GetAPIKeyByHash(ctx, keyHash); err == nil {
		return nil
	}
	return st.InsertAPIKey(ctx, store.APIKey{ID: "key_local_" + keyHash[:16], Name: "Local application", KeyHash: keyHash, KeyPrefix: "sk-omniswitch-…", Role: "viewer", AllowedModels: []string{"default"}, RateLimit: 120, CreatedAt: time.Now().UTC(), Enabled: true})
}
