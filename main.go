package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
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
	Name string `json:"name"`
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
	var buttons strings.Builder
	for i, p := range cfg.Pairs {
		label := p.Name
		if strings.TrimSpace(label) == "" {
			label = p.From + " -> " + p.To
		}
		fmt.Fprintf(&buttons, `<button onclick="syncOne(%d)">%s</button>`+"\n", i, html.EscapeString(label))
	}

	io.WriteString(w, `
<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="utf-8"/>
  <title>Proxy UI</title>
  <style>
    body { font-family: system-ui, -apple-system, Segoe UI, Roboto, Arial, sans-serif; margin: 16px; }
    .row { display: flex; gap: 8px; flex-wrap: wrap; margin-bottom: 12px; }
    button { padding: 8px 12px; cursor: pointer; }
    #log { border: 1px solid #ddd; background: #0b1020; color: #e8e8e8; padding: 10px; min-height: 240px; white-space: pre-wrap; }
  </style>
</head>
<body>
  <div class="row">
`+buttons.String()+`
  </div>
  <div class="row">
    <button onclick="clearLog()">Clear log</button>
  </div>
  <pre id="log"></pre>
  <script>
    function ts() {
      return new Date().toISOString();
    }
    function appendLog(s) {
      const log = document.getElementById('log');
      // newest first (also reverse multiline blocks)
      const lines = String(s).replace(/\r\n/g, '\n').split('\n');
      if (lines.length && lines[lines.length - 1] === '') lines.pop();
      const normalized = lines.reverse().join('\n') + (lines.length ? '\n' : '');
      log.textContent = normalized + log.textContent;
      log.scrollTop = 0;
    }
    function clearLog() {
      document.getElementById('log').textContent = '';
    }
    async function syncOne(i) {
      appendLog('[' + ts() + '] Click: #' + i + '\n');
      try {
        const res = await fetch('/sync?i=' + encodeURIComponent(i), { method: 'POST' });
        const txt = await res.text();
        appendLog(txt);
      } catch (e) {
        appendLog('Error: ' + e + '\n');
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

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	ctx := r.Context()

	iStr := r.URL.Query().Get("i")
	if iStr == "" {
		http.Error(w, "missing query param i", http.StatusBadRequest)
		return
	}
	i, err := strconv.Atoi(iStr)
	if err != nil || i < 0 || i >= len(cfg.Pairs) {
		http.Error(w, "invalid i", http.StatusBadRequest)
		return
	}
	p := cfg.Pairs[i]

	var out strings.Builder
	writeLine := func(format string, args ...any) {
		fmt.Fprintf(&out, format+"\n", args...)
	}

	label := p.Name
	if strings.TrimSpace(label) == "" {
		label = p.From + " -> " + p.To
	}
	writeLine("[%s] Start: %s", time.Now().Format(time.RFC3339), label)

	writeLine("[%s] Fetch token", time.Now().Format(time.RFC3339))
	token, err := fetchKeycloakToken(ctx, cfg.Keycloak)
	if err != nil {
		writeLine("[%s] Token error: %s", time.Now().Format(time.RFC3339), err.Error())
		w.Write([]byte(out.String()))
		return
	}
	writeLine("[%s] Token OK", time.Now().Format(time.RFC3339))

	if err := copyOnce(ctx, token, p, writeLine); err != nil {
		writeLine("[%s] ERROR: %s", time.Now().Format(time.RFC3339), err.Error())
	} else {
		writeLine("[%s] OK", time.Now().Format(time.RFC3339))
	}
	w.Write([]byte(out.String()))
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

func copyOnce(ctx context.Context, token string, p Pair, logf func(format string, args ...any)) error {
	// GET
	if logf != nil {
		logf("[%s] GET %s", time.Now().Format(time.RFC3339), p.From)
	}
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

	if logf != nil {
		logf("[%s] GET status %d", time.Now().Format(time.RFC3339), respGet.StatusCode)
	}
	body, err := io.ReadAll(respGet.Body)
	if err != nil {
		return err
	}

	// POST (данные один в один)
	if logf != nil {
		logf("[%s] POST %s", time.Now().Format(time.RFC3339), p.To)
	}
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

	if logf != nil {
		logf("[%s] POST status %d", time.Now().Format(time.RFC3339), respPost.StatusCode)
	}
	if respPost.StatusCode >= 300 {
		b, _ := io.ReadAll(respPost.Body)
		return fmt.Errorf("post status %d: %s", respPost.StatusCode, string(b))
	}

	return nil
}