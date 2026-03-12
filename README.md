# Go Sample Proxy UI

Minimal web UI that copies data from one HTTP service to another:

- Fetches data with **GET** from a source service
- Sends the same data with **POST** to a target service
- Gets access tokens from **Keycloak** (separate tokens for source/target)
- Supports per-service configuration, TLS options, JSON array chunking, and streaming logs in the browser

## Architecture (Mermaid)

```mermaid
flowchart LR
    B[Browser UI] -->|Click button| S[Proxy UI server]

    subgraph Auth
      K1[Keycloak FROM] -->|client_credentials| S
      K2[Keycloak TO] -->|client_credentials| S
    end

    subgraph Data Flow
      S -->|GET with FROM token| SRC[Source service]
      SRC -->|JSON / body| S
      S -->|POST with TO token\n(single body or JSON chunks)| DST[Target service]
    end

    subgraph Config
      C[config.json\n(port, tls, pairs,\nchunking, logging)] --> S
    end

    subgraph Docker
      D[Docker container\n(proxy-ui + config.json)] --> S
    end
```

## Requirements

- Go 1.22+ (for local build), or Docker

## Configuration

All configuration is in `config.json` (copied into the container by default).

```jsonc
{
  "port": 8000,
  "get_snippet_bytes": 1024,
  "json_array_chunk_size": 100,
  "json_array_chunk_delay_ms": 500,
  "keycloak": {
    "token_url": "https://keycloak.example.com/realms/myrealm/protocol/openid-connect/token",
    "client_id": "my-client-id",
    "client_secret": "my-client-secret"
  },
  "tls": {
    "insecure_skip_verify": false,
    "ca_file": ""
  },
  "pairs": [
    {
      "name": "Service A → Service B",
      "from": "https://service-a.example.com/data",
      "to": "https://service-b.example.com/data",
      "from_keycloak": {
        "token_url": "https://keycloak.example.com/realms/realm-a-read/protocol/openid-connect/token",
        "client_id": "client-a-read",
        "client_secret": "secret-a-read"
      },
      "to_keycloak": {
        "token_url": "https://keycloak.example.com/realms/realm-a-write/protocol/openid-connect/token",
        "client_id": "client-a-write",
        "client_secret": "secret-a-write"
      }
    }
  ]
}
```

### Top‑level fields

- **`port`**: HTTP port the UI listens on inside the container.
- **`get_snippet_bytes`**: how many bytes from the GET response body to log as a preview.
- **`json_array_chunk_size`**: if GET returns a JSON array, split it into chunks of this many items and send multiple POST requests; `<=0` disables chunking.
- **`json_array_chunk_delay_ms`**: delay between POST chunk requests, in milliseconds (helps with rate limits / 429).
- **`keycloak`**: default Keycloak client config (used when per-pair config is not provided).
- **`tls.insecure_skip_verify`**: set to `true` only for development or when you really need to skip certificate verification.
- **`tls.ca_file`**: optional path to an additional CA bundle (PEM) for internal/self-signed certificates.

### Pair fields

- **`name`**: button label in the UI.
- **`from`**: source URL (GET).
- **`to`**: target URL (POST).
- **`from_keycloak`**: optional Keycloak config for the source service (overrides global `keycloak`).
- **`to_keycloak`**: optional Keycloak config for the target service (overrides global `keycloak`).

## Running locally (without Docker)

```bash
go run main.go
```

Then open:

```text
http://localhost:8000/
```

Port can be changed via the `port` field in `config.json`.

## Build and Run with Docker

Build image:

```bash
docker build -t go-sample-proxy-ui .
```

Run with default `config.json` bundled into the image:

```bash
docker run --rm -p 8000:8000 go-sample-proxy-ui
```

Run with a custom `config.json` from the host:

```bash
docker run --rm -p 8000:8000 \
  -v "$(pwd)/config.json:/app/config.json:ro" \
  go-sample-proxy-ui
```

If you change the `port` value in `config.json`, make sure to adjust the published port:

```bash
docker run --rm -p 9000:9000 \
  -v "$(pwd)/config.json:/app/config.json:ro" \
  go-sample-proxy-ui
```

Then open `http://localhost:<port>/` in your browser.

