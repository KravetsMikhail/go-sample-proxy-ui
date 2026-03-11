package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
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
	Port            int           `json:"port"`
	GetSnippetBytes int           `json:"get_snippet_bytes"`
	JSONChunkSize   int           `json:"json_array_chunk_size"`
	Keycloak        KeycloakConfig `json:"keycloak"`
	TLS             TLSConfig      `json:"tls"`
	Pairs           []Pair         `json:"pairs"`
}

type TLSConfig struct {
	InsecureSkipVerify bool   `json:"insecure_skip_verify"`
	CAFile             string `json:"ca_file"`
}

type KeycloakConfig struct {
	TokenURL     string `json:"token_url"`
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
}

type Pair struct {
	Name         string          `json:"name"`
	From         string          `json:"from"`
	To           string          `json:"to"`
	FromKeycloak *KeycloakConfig `json:"from_keycloak,omitempty"`
	ToKeycloak   *KeycloakConfig `json:"to_keycloak,omitempty"`
}

var cfg Config
var httpClient = &http.Client{Timeout: 15 * time.Second}

func main() {
	if err := loadConfig("config.json", &cfg); err != nil {
		log.Fatalf("load config: %v", err)
	}

	if err := configureHTTPClientTLS(&cfg); err != nil {
		log.Fatalf("configure tls: %v", err)
	}

	http.HandleFunc("/", indexHandler)
	http.HandleFunc("/sync", syncHandler)

	addr := fmt.Sprintf(":%d", cfg.Port)
	if cfg.Port == 0 {
		addr = ":8000"
	}
	log.Println("listening on", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}

func configureHTTPClientTLS(cfg *Config) error {
	// Default http.Transport already uses system roots; we only override when needed.
	if !cfg.TLS.InsecureSkipVerify && strings.TrimSpace(cfg.TLS.CAFile) == "" {
		return nil
	}

	tlsCfg := &tls.Config{
		InsecureSkipVerify: cfg.TLS.InsecureSkipVerify, //nolint:gosec // explicitly configured for internal/self-signed setups
	}

	if strings.TrimSpace(cfg.TLS.CAFile) != "" {
		pemBytes, err := os.ReadFile(cfg.TLS.CAFile)
		if err != nil {
			return fmt.Errorf("read ca_file: %w", err)
		}

		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if ok := pool.AppendCertsFromPEM(pemBytes); !ok {
			return fmt.Errorf("ca_file does not contain any valid PEM certificates")
		}
		tlsCfg.RootCAs = pool
	}

	httpClient.Transport = &http.Transport{
		Proxy:           http.ProxyFromEnvironment,
		TLSClientConfig: tlsCfg,
	}
	return nil
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

	fromKC := cfg.Keycloak
	if p.FromKeycloak != nil {
		fromKC = *p.FromKeycloak
	}
	writeLine("[%s] Fetch FROM token", time.Now().Format(time.RFC3339))
	tokenFrom, err := fetchKeycloakToken(ctx, fromKC, writeLine)
	if err != nil {
		writeLine("[%s] FROM token error: %s", time.Now().Format(time.RFC3339), err.Error())
		w.Write([]byte(out.String()))
		return
	}
	writeLine("[%s] FROM token OK", time.Now().Format(time.RFC3339))

	toKC := cfg.Keycloak
	if p.ToKeycloak != nil {
		toKC = *p.ToKeycloak
	}
	writeLine("[%s] Fetch TO token", time.Now().Format(time.RFC3339))
	tokenTo, err := fetchKeycloakToken(ctx, toKC, writeLine)
	if err != nil {
		writeLine("[%s] TO token error: %s", time.Now().Format(time.RFC3339), err.Error())
		w.Write([]byte(out.String()))
		return
	}
	writeLine("[%s] TO token OK", time.Now().Format(time.RFC3339))

	if err := copyOnce(ctx, tokenFrom, tokenTo, p, writeLine); err != nil {
		writeLine("[%s] ERROR: %s", time.Now().Format(time.RFC3339), err.Error())
	} else {
		writeLine("[%s] OK", time.Now().Format(time.RFC3339))
	}
	w.Write([]byte(out.String()))
}

func fetchKeycloakToken(ctx context.Context, kc KeycloakConfig, logf func(format string, args ...any)) (string, error) {
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

	if logf != nil {
		logf("[%s] Token resp status %d", time.Now().Format(time.RFC3339), resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read token body: %w", err)
	}

	const maxSnippet = 512
	snippet := body
	if len(snippet) > maxSnippet {
		snippet = snippet[:maxSnippet]
	}
	if logf != nil {
		logf("[%s] Token raw body (first %d bytes): %s", time.Now().Format(time.RFC3339), len(snippet), string(snippet))
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("keycloak status %d", resp.StatusCode)
	}

	var data struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return "", fmt.Errorf("decode token json: %w", err)
	}

	if logf != nil {
		logf("[%s] Token value: %s", time.Now().Format(time.RFC3339), data.AccessToken)
	}
	return data.AccessToken, nil
}

func copyOnce(ctx context.Context, tokenFrom, tokenTo string, p Pair, logf func(format string, args ...any)) error {
	// GET
	if logf != nil {
		logf("[%s] GET %s", time.Now().Format(time.RFC3339), p.From)
	}
	reqGet, err := http.NewRequestWithContext(ctx, http.MethodGet, p.From, nil)
	if err != nil {
		return err
	}
	reqGet.Header.Set("Authorization", "Bearer "+tokenFrom)

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
	if logf != nil {
		lineCount := 0
		if len(body) > 0 {
			lineCount = bytes.Count(body, []byte{'\n'})
			if body[len(body)-1] != '\n' {
				lineCount++
			}
		}
		logf("[%s] GET body: %d bytes, %d lines", time.Now().Format(time.RFC3339), len(body), lineCount)

		max := cfg.GetSnippetBytes
		if max <= 0 {
			max = 1024
		}
		snippet := body
		if len(snippet) > max {
			snippet = snippet[:max]
		}
		logf("[%s] GET body snippet (first %d bytes): %s", time.Now().Format(time.RFC3339), len(snippet), string(snippet))
	}

	// Попробуем распознать JSON-массив и при необходимости отправить чанками
	contentType := respGet.Header.Get("Content-Type")
	chunkSize := cfg.JSONChunkSize
	if chunkSize <= 0 {
		chunkSize = 0
	}

	trimmed := bytes.TrimSpace(body)
	isJSONArray := len(trimmed) > 0 && trimmed[0] == '['

	if chunkSize > 0 && isJSONArray {
		if logf != nil {
			logf("[%s] Detected JSON array, chunk size %d", time.Now().Format(time.RFC3339), chunkSize)
		}

		var items []json.RawMessage
		if err := json.Unmarshal(trimmed, &items); err != nil {
			if logf != nil {
				logf("[%s] Failed to parse JSON array, fallback to single POST: %s", time.Now().Format(time.RFC3339), err.Error())
			}
		} else {
			total := len(items)
			if logf != nil {
				logf("[%s] JSON array has %d items", time.Now().Format(time.RFC3339), total)
			}
			for start := 0; start < total; start += chunkSize {
				end := start + chunkSize
				if end > total {
					end = total
				}
				chunk := items[start:end]
				chunkBody, err := json.Marshal(chunk)
				if err != nil {
					return fmt.Errorf("marshal chunk %d-%d: %w", start, end, err)
				}
				if logf != nil {
					logf("[%s] POST %s chunk %d-%d (%d items, %d bytes)", time.Now().Format(time.RFC3339), p.To, start, end-1, len(chunk), len(chunkBody))
				}
				reqPost, err := http.NewRequestWithContext(ctx, http.MethodPost, p.To, io.NopCloser(bytes.NewReader(chunkBody)))
				if err != nil {
					return err
				}
				reqPost.Header.Set("Authorization", "Bearer "+tokenTo)
				if contentType != "" {
					reqPost.Header.Set("Content-Type", contentType)
				} else {
					reqPost.Header.Set("Content-Type", "application/json")
				}

				respPost, err := httpClient.Do(reqPost)
				if err != nil {
					return err
				}
				func() {
					defer respPost.Body.Close()
					if logf != nil {
						logf("[%s] POST chunk status %d", time.Now().Format(time.RFC3339), respPost.StatusCode)
					}
					if respPost.StatusCode >= 300 {
						b, _ := io.ReadAll(respPost.Body)
						err = fmt.Errorf("post chunk status %d: %s", respPost.StatusCode, string(b))
					}
				}()
				if err != nil {
					return err
				}
			}
			return nil
		}
	}

	// Обычный случай: один POST с полным телом
	if logf != nil {
		logf("[%s] POST %s", time.Now().Format(time.RFC3339), p.To)
	}
	reqPost, err := http.NewRequestWithContext(ctx, http.MethodPost, p.To, io.NopCloser(bytes.NewReader(body)))
	if err != nil {
		return err
	}
	reqPost.Header.Set("Authorization", "Bearer "+tokenTo)
	if contentType != "" {
		reqPost.Header.Set("Content-Type", contentType)
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