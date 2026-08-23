# Spacebox

Spacebox is a prototype unified communications HUD for Omarchy: one
spaceship-style interface for messages from email and social providers.

The first slice includes a frontend prototype with provider filters, search,
conversation selection, and a local reply interaction, plus a Go API with
Gmail OAuth, thread listing, synchronization, and reply endpoints.

Open `index.html` in a browser or serve the directory with:

```bash
python -m http.server 8080
```

## Go backend

Install the Google Cloud OAuth client credentials and set the values from
`.env.example` in your environment. The redirect URL must be registered in
the Google Cloud project. Then run:

```bash
cp .env.example .env
# Edit .env with your real credentials; never commit it.
set -a
source .env
set +a
go run ./cmd/spacebox
```

Open `http://127.0.0.1:8787/auth/gmail` to connect Gmail. The API serves the
frontend from `SPACEBOX_WEB_DIR` and exposes:

```text
GET  /api/threads
GET  /api/threads/:id
POST /api/threads/:id/reply
POST /api/sync
```

Tokens are stored with `0600` permissions under
`~/.local/share/spacebox/gmail-token.json` by default.
