package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

type Config struct {
	Keycloak KeycloakConfig `json:"keycloak"`
	Pairs    []Pair         `json:"pairs"`
}

type KeycloakConfig struct {
	TokenURL     string `json:"token_url"`
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
}

type Pair struct {
	From string `json:"from"`
	To   string `json:"to"`
}

var cfg Config
var httpClient = &http.Client{Timeout: 15 * time.Second}

func main() {
	if err := loadConfig("config.json", &cfg); err != nil {
		log.Fatalf("load config: %v", err)
	}

	http.HandleFunc("/", indexHandler)
	http.HandleFunc("/sync", syncHandler)

	log.Println("listening on :8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}

func loadConfig(path string, out interface{}) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return json.NewDecoder(f).Decode(out)
}

func indexHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	io.WriteString(w, `
<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="utf-8"/>
  <title>Proxy UI</title>
</head>
<body>
  <button onclick="sync()">Sync services</button>
  <pre id="log"></pre>
  <script>
    async function sync() {
      const log = document.getElementById('log');
      log.textContent = 'Running...\n';
      try {
        const res = await fetch('/sync', { method: 'POST' });
        const txt = await res.text();
        log.textContent += txt;
      } catch (e) {
        log.textContent += 'Error: ' + e;
      }
    }
  </script>
</body>
</html>
`)
}

func syncHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "only POST", http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()
	token, err := fetchKeycloakToken(ctx, cfg.Keycloak)
	if err != nil {
		http.Error(w, "token error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	for _, p := range cfg.Pairs {
		if err := copyOnce(ctx, token, p); err != nil {
			w.Write([]byte("pair from " + p.From + " to " + p.To + ": " + err.Error() + "\n"))
		} else {
			w.Write([]byte("OK " + p.From + " -> " + p.To + "\n"))
		}
	}
}

func fetchKeycloakToken(ctx context.Context, kc KeycloakConfig) (string, error) {
	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	form.Set("client_id", kc.ClientID)
	form.Set("client_secret", kc.ClientSecret)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, kc.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("keycloak status %d: %s", resp.StatusCode, string(b))
	}

	var data struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return "", err
	}
	return data.AccessToken, nil
}

func copyOnce(ctx context.Context, token string, p Pair) error {
	// GET
	reqGet, err := http.NewRequestWithContext(ctx, http.MethodGet, p.From, nil)
	if err != nil {
		return err
	}
	reqGet.Header.Set("Authorization", "Bearer "+token)

	respGet, err := httpClient.Do(reqGet)
	if err != nil {
		return err
	}
	defer respGet.Body.Close()

	body, err := io.ReadAll(respGet.Body)
	if err != nil {
		return err
	}

	// POST (данные один в один)
	reqPost, err := http.NewRequestWithContext(ctx, http.MethodPost, p.To, io.NopCloser(bytes.NewReader(body)))
	if err != nil {
		return err
	}
	reqPost.Header.Set("Authorization", "Bearer "+token)
	// Если нужно сохранить тип, можно пробросить Content-Type:
	if ct := respGet.Header.Get("Content-Type"); ct != "" {
		reqPost.Header.Set("Content-Type", ct)
	}

	respPost, err := httpClient.Do(reqPost)
	if err != nil {
		return err
	}
	defer respPost.Body.Close()

	if respPost.StatusCode >= 300 {
		b, _ := io.ReadAll(respPost.Body)
		return fmt.Errorf("post status %d: %s", respPost.StatusCode, string(b))
	}

	return nil
}