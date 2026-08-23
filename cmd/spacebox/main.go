package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"database/sql"
	"github.com/go-chi/chi/v5"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/gmail/v1"
	"google.golang.org/api/option"
	_ "modernc.org/sqlite"
)

const gmailScopes = "https://www.googleapis.com/auth/gmail.modify"

type server struct {
	db         *sql.DB
	oauth      *oauth2.Config
	tokenPath  string
	stateMu    sync.Mutex
	oauthState map[string]time.Time
}

type thread struct {
	ID      string `json:"id"`
	Subject string `json:"subject"`
	From    string `json:"from"`
	Snippet string `json:"snippet"`
	Date    string `json:"date"`
	Unread  bool   `json:"unread"`
}

func main() {
	port := env("SPACEBOX_PORT", "8787")
	dbPath := env("SPACEBOX_DB", filepath.Join(dataDir(), "spacebox.db"))
	tokenPath := env("GMAIL_TOKEN_PATH", filepath.Join(dataDir(), "gmail-token.json"))

	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		log.Fatal(err)
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS sync_state (key TEXT PRIMARY KEY, value TEXT NOT NULL)`); err != nil {
		log.Fatal(err)
	}

	s := &server{
		db:         db,
		tokenPath:  tokenPath,
		oauthState: make(map[string]time.Time),
	}
	if config, err := gmailConfig(); err == nil {
		s.oauth = config
	} else {
		log.Printf("Gmail OAuth disabled: %v", err)
	}

	r := chi.NewRouter()
	r.Get("/healthz", s.health)
	r.Get("/auth/gmail", s.gmailAuth)
	r.Get("/auth/gmail/callback", s.gmailCallback)
	r.Get("/api/threads", s.listThreads)
	r.Get("/api/threads/{id}", s.getThread)
	r.Post("/api/threads/{id}/reply", s.reply)
	r.Post("/api/sync", s.sync)
	r.Handle("/*", http.FileServer(http.Dir(env("SPACEBOX_WEB_DIR", "."))))

	log.Printf("Spacebox listening on http://127.0.0.1:%s", port)
	log.Fatal(http.ListenAndServe("127.0.0.1:"+port, r))
}

func (s *server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "gmailConfigured": s.oauth != nil})
}

func (s *server) gmailAuth(w http.ResponseWriter, r *http.Request) {
	if s.oauth == nil {
		http.Error(w, "Gmail OAuth is not configured", http.StatusNotImplemented)
		return
	}
	state, err := randomString(32)
	if err != nil {
		http.Error(w, "could not create OAuth state", http.StatusInternalServerError)
		return
	}
	s.stateMu.Lock()
	s.oauthState[state] = time.Now().Add(10 * time.Minute)
	s.stateMu.Unlock()
	http.Redirect(w, r, s.oauth.AuthCodeURL(state, oauth2.AccessTypeOffline, oauth2.SetAuthURLParam("prompt", "consent")), http.StatusFound)
}

func (s *server) gmailCallback(w http.ResponseWriter, r *http.Request) {
	if s.oauth == nil {
		http.Error(w, "Gmail OAuth is not configured", http.StatusNotImplemented)
		return
	}
	state := r.URL.Query().Get("state")
	if !s.validState(state) {
		http.Error(w, "invalid or expired OAuth state", http.StatusBadRequest)
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		http.Error(w, "Gmail authorization was denied", http.StatusBadRequest)
		return
	}
	token, err := s.oauth.Exchange(r.Context(), code)
	if err != nil {
		http.Error(w, "could not exchange Gmail authorization code", http.StatusBadGateway)
		return
	}
	if err := saveToken(s.tokenPath, token); err != nil {
		http.Error(w, "could not save Gmail token", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/", http.StatusFound)
}

func (s *server) listThreads(w http.ResponseWriter, r *http.Request) {
	service, err := s.gmail(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	result, err := service.Users.Threads.List("me").MaxResults(50).Do()
	if err != nil {
		writeError(w, err)
		return
	}
	out := make([]thread, 0, len(result.Threads))
	for _, item := range result.Threads {
		t, err := threadFromGmail(r.Context(), service, item.Id)
		if err != nil {
			writeError(w, err)
			return
		}
		out = append(out, t)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *server) getThread(w http.ResponseWriter, r *http.Request) {
	service, err := s.gmail(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	item, err := service.Users.Threads.Get("me", chi.URLParam(r, "id")).Format("full").Do()
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *server) reply(w http.ResponseWriter, r *http.Request) {
	service, err := s.gmail(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	var input struct {
		To      string `json:"to"`
		Subject string `json:"subject"`
		Body    string `json:"body"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&input); err != nil {
		writeErrorStatus(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if input.To == "" || input.Body == "" {
		writeErrorStatus(w, http.StatusBadRequest, "to and body are required")
		return
	}
	raw := fmt.Sprintf("To: %s\r\nSubject: %s\r\nContent-Type: text/plain; charset=\"UTF-8\"\r\n\r\n%s", input.To, input.Subject, input.Body)
	message := &gmail.Message{Raw: base64.RawURLEncoding.EncodeToString([]byte(raw))}
	sent, err := service.Users.Messages.Send("me", message).Do()
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": sent.Id})
}

func (s *server) sync(w http.ResponseWriter, r *http.Request) {
	service, err := s.gmail(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	result, err := service.Users.Threads.List("me").MaxResults(50).Do()
	if err != nil {
		writeError(w, err)
		return
	}
	if _, err := s.db.ExecContext(r.Context(), `INSERT INTO sync_state(key,value) VALUES('last_sync',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, time.Now().UTC().Format(time.RFC3339)); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"threads": len(result.Threads)})
}

func (s *server) gmail(ctx context.Context) (*gmail.Service, error) {
	if s.oauth == nil {
		return nil, errors.New("Gmail OAuth is not configured; set GMAIL_CLIENT_ID, GMAIL_CLIENT_SECRET, and GMAIL_REDIRECT_URL")
	}
	token, err := loadToken(s.tokenPath)
	if err != nil {
		return nil, errors.New("Gmail is not connected; open /auth/gmail first")
	}
	client := s.oauth.Client(ctx, token)
	return gmail.NewService(ctx, option.WithHTTPClient(client))
}

func threadFromGmail(ctx context.Context, service *gmail.Service, id string) (thread, error) {
	item, err := service.Users.Threads.Get("me", id).Format("metadata").MetadataHeaders("Subject", "From", "Date").Do()
	if err != nil {
		return thread{}, err
	}
	t := thread{ID: item.Id, Snippet: html.UnescapeString(item.Snippet)}
	if len(item.Messages) == 0 {
		return t, nil
	}
	last := item.Messages[len(item.Messages)-1]
	for _, h := range last.Payload.Headers {
		switch strings.ToLower(h.Name) {
		case "subject":
			t.Subject = h.Value
		case "from":
			t.From = h.Value
		case "date":
			t.Date = h.Value
		}
	}
	for _, label := range last.LabelIds {
		if label == "UNREAD" {
			t.Unread = true
			break
		}
	}
	return t, nil
}

func (s *server) validState(state string) bool {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	expires, ok := s.oauthState[state]
	delete(s.oauthState, state)
	return ok && time.Now().Before(expires)
}

func gmailConfig() (*oauth2.Config, error) {
	id, secret, redirect := os.Getenv("GMAIL_CLIENT_ID"), os.Getenv("GMAIL_CLIENT_SECRET"), os.Getenv("GMAIL_REDIRECT_URL")
	if id == "" || secret == "" || redirect == "" {
		return nil, errors.New("Gmail credentials are missing")
	}
	return &oauth2.Config{ClientID: id, ClientSecret: secret, Endpoint: google.Endpoint, RedirectURL: redirect, Scopes: []string{gmailScopes}}, nil
}

func saveToken(path string, token *oauth2.Token) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(token)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func loadToken(path string) (*oauth2.Token, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var token oauth2.Token
	if err := json.Unmarshal(data, &token); err != nil {
		return nil, err
	}
	return &token, nil
}

func randomString(size int) (string, error) {
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func dataDir() string {
	if dir := os.Getenv("XDG_DATA_HOME"); dir != "" {
		return filepath.Join(dir, "spacebox")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".spacebox"
	}
	return filepath.Join(home, ".local", "share", "spacebox")
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, err error) {
	writeErrorStatus(w, http.StatusBadGateway, err.Error())
}

func writeErrorStatus(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
