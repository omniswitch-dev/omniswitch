package onboarding

import (
	"context"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
)

//go:embed static/*
var assets embed.FS

type Checker func(context.Context, Connection) (Connection, error)

type Server struct {
	Origin    string
	Token     string
	Directory string
	Previous  *Profile
	Env       func(string) string
	Check     Checker
	Done      chan Profile
	mu        sync.Mutex
	complete  bool
}

func NewServer(dir, origin string, previous *Profile, env func(string) string) (*Server, error) {
	token, err := RandomToken()
	if err != nil {
		return nil, err
	}
	return &Server{Directory: dir, Origin: origin, Token: token, Previous: previous, Env: env, Check: Probe, Done: make(chan Profile, 1)}, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
	origin, _ := url.Parse(s.Origin)
	if r.Host != origin.Host {
		http.Error(w, "invalid host", http.StatusForbidden)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/") {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if subtle.ConstantTimeCompare([]byte(token), []byte(s.Token)) != 1 {
			http.Error(w, "reopen the setup link from your terminal", http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodGet && r.Header.Get("Origin") != s.Origin {
			http.Error(w, "invalid origin", http.StatusForbidden)
			return
		}
		switch r.URL.Path {
		case "/api/setup":
			if r.Method != http.MethodGet {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			choices := make([]Provider, len(providers))
			copy(choices, providers)
			for i := range choices {
				choices[i].Detected = providerKey(choices[i], s.Env) != ""
			}
			respond(w, 200, map[string]any{"providers": choices})
		case "/api/connect":
			s.connect(w, r)
		default:
			http.NotFound(w, r)
		}
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	path, contentType := "", ""
	switch r.URL.Path {
	case "/":
		path = "static/index.html"
		contentType = "text/html; charset=utf-8"
	case "/setup.js":
		path = "static/setup.js"
		contentType = "text/javascript; charset=utf-8"
	case "/setup.css":
		path = "static/setup.css"
		contentType = "text/css; charset=utf-8"
	default:
		http.NotFound(w, r)
		return
	}
	data, err := assets.ReadFile(path)
	if err != nil {
		http.Error(w, "setup UI unavailable", 500)
		return
	}
	w.Header().Set("Content-Type", contentType)
	if r.Method == http.MethodGet {
		_, _ = w.Write(data)
	}
}

func (s *Server) connect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16384)
	var request struct {
		Provider string `json:"provider"`
		Key      string `json:"key"`
		BaseURL  string `json:"base_url"`
		Model    string `json:"model"`
		UseEnv   bool   `json:"use_env"`
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		respond(w, 400, map[string]string{"error": "invalid setup request"})
		return
	}
	spec, ok := findProvider(request.Provider)
	if !ok {
		respond(w, 400, map[string]string{"error": "choose a supported provider"})
		return
	}
	key := strings.TrimSpace(request.Key)
	if request.UseEnv {
		key = providerKey(spec, s.Env)
	}
	if len(key) > 4096 || strings.ContainsAny(key, "\r\n") {
		respond(w, 400, map[string]string{"error": "enter a valid provider key"})
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.complete {
		respond(w, 409, map[string]string{"error": "setup is already complete"})
		return
	}
	connection, err := s.Check(r.Context(), Connection{Provider: spec.ID, Key: key, BaseURL: request.BaseURL, Model: request.Model})
	if err != nil {
		respond(w, 400, map[string]string{"error": err.Error()})
		return
	}
	profile, err := NewProfile(connection, s.Previous)
	if err == nil {
		parsed, _ := url.Parse(s.Origin)
		profile.Listen = parsed.Host
	}
	if err == nil {
		err = Save(s.Directory, profile)
	}
	if err != nil {
		respond(w, 500, map[string]string{"error": "could not save your local setup; check directory permissions"})
		return
	}
	s.complete = true
	respond(w, 200, map[string]any{"gateway_key": profile.GatewayKey, "app_key": profile.AppKey, "provider": spec.Name, "model": "default", "upstream_model": profile.Connection.Model, "demo": spec.ID == "demo", "base_url": s.Origin + "/v1"})
	s.Done <- profile
}

func respond(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func (s *Server) Link() string { return fmt.Sprintf("%s/#setup=%s", s.Origin, s.Token) }
