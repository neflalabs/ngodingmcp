# NgodingMCP

MCP server berbasis Go yang menjadikan sebuah folder *workspace* yang bisa
dikontrol AI lewat tools, sekaligus bisa diedit manusia lewat Web UI.
Transport-nya HTTP (MCP Streamable HTTP) dengan autentikasi token.

```
AI client ──HTTP/JSON-RPC──┐
                           ├──► ngodingmcp ──► workspace/ (file di disk)
Browser (Web UI) ──────────┘
```

## Fitur

- **32 tool inti** yang selalu aktif: baca/tulis/list/pindah/hapus file, edit
  presisi, search & replace, statistik workspace, `validate_syntax`, `count_tokens`.
- **Edit atomik + diff**: `edit_file` menjalankan beberapa edit sekaligus (semua
  berhasil atau tidak ada yang ditulis) dan mengembalikan unified diff.
- **Git lokal + remote**: status/diff/log, init, add, unstage, commit, buat/pindah
  branch, remote, fetch, pull (fast-forward saja), push (tanpa force), merge
  (konflik di-abort otomatis), dan buat repo GitHub lewat `gh`. Tanpa rebase,
  reset, atau hook.
- **`run_command` + background process** (opt-in, `--allow-run`): command dari
  allowlist tanpa shell, plus dev server yang tetap hidup (`bun run dev`).
- **Download & upload**: `download_file` dari URL https (opt-in,
  `--allow-download`), upload drag-and-drop lewat Web UI.
- **Web UI**: file tree, editor, search, log proses, light/dark mode,
  workspace selector (pindah proyek tanpa restart).
- **TUI control panel** untuk start/stop server dan melihat aktivitas tool AI.
- **systemd user service**: `./ngodingmcp -d` agar jalan di background tanpa root.
- **Sandbox**: semua path dikunci di root workspace; file sensitif (`.env`,
  private key, `.git`) diblokir secara default.

Jumlah tool: 32 secara default, 39 dengan `--allow-run` (+ `run_command` dan 6
tool proses), 40 kalau `--allow-download` juga aktif. Daftar lengkap ada di
[docs/tools.md](docs/tools.md).

## Requirements

- Go **1.26.10** atau lebih baru (lihat `go.mod`) untuk build.
- `git` di `PATH` untuk tool git. `gh` (GitHub CLI, sudah `gh auth login`) untuk
  `git_github_create_repo` dan cara termudah push lewat HTTPS. Langkah auth,
  termasuk untuk mode `-d`, ada di [docs/tools.md](docs/tools.md#auth-untuk-remote).
- Linux dengan systemd untuk mode background (`-d`). Mode TUI dan `serve`
  tidak butuh systemd.

## Quick start

Build:

```sh
go build -o ngodingmcp .    # atau: make build (binary statis)
```

Jalankan TUI (control panel interaktif):

```sh
./ngodingmcp
```

Isi workspace root, address, dan token (kosongkan agar dibuat otomatis), lalu
tekan `Enter`. Buka `http://127.0.0.1:8080/` dan masukkan token untuk Web UI.

Atau jalankan di background sebagai systemd user service:

```sh
./ngodingmcp -d          # pasang + start, cetak alamat Web UI dan MCP
./ngodingmcp status      # nyala atau tidak, alamat, workspace
./ngodingmcp logs        # ikuti log service
./ngodingmcp restart     # setelah rebuild binary atau edit file env
./ngodingmcp stop        # stop (tetap enabled untuk login berikutnya)
```

Supaya service tetap jalan setelah logout: `loginctl enable-linger $USER` (sekali saja).

## Menyambungkan MCP client

- Endpoint: `http://127.0.0.1:8080/mcp`
- Auth: `Authorization: Bearer <token>` atau `X-API-Key: <token>` (token yang sama).

Contoh config client yang mendukung HTTP + header:

```json
{
  "mcpServers": {
    "ngodingmcp": {
      "type": "http",
      "url": "http://127.0.0.1:8080/mcp",
      "headers": { "Authorization": "Bearer rahasia-panjang" }
    }
  }
}
```

**OAuth (MCP spec)**: `/mcp` juga berfungsi sebagai OAuth 2.1 resource server sekaligus
authorization server embedded. Client MCP yang mendukung spec bisa discover (`/.well-known/oauth-protected-resource`),
registrasi (DCR), dan authorize dengan PKCE otomatis — lihat [docs/oauth.md](docs/oauth.md).
Token statis tetap diterima sebagai alternatif.

**Sesame** (custom MCP server): Sesame menyambung dari internet, jadi
NgodingMCP perlu diakses lewat HTTPS publik, misalnya di belakang reverse proxy
TLS. Isi URL `https://<domain-kamu>/mcp`, lalu pilih autentikasi token (dikirim
sebagai Bearer) atau API key dengan header `X-API-Key`. Alternatifnya: OAuth (lihat
[docs/oauth.md](docs/oauth.md)) dengan `-public-url https://<domain-kamu>` (atau `-trusted-proxy` jika
proxy dipercaya hanya untuk menurunkan scheme).

## Konfigurasi

- Setelan TUI dan Web UI disimpan di `~/.config/ngodingmcp/config.json`
  (di luar workspace, mode `0600`), termasuk daftar workspace tersimpan.
- Signing key dan daftar client OAuth tersimpan di `~/.config/ngodingmcp/oauth.json`
  (mode `0600`; refresh token tidak pernah ditulis ke disk).
- Service systemd membaca `~/.config/ngodingmcp/env` (format `KEY=value`).
- Urutan prioritas: **flag > environment > config.json**.

Tabel flag dan env lengkap: [docs/configuration.md](docs/configuration.md).

## Catatan keamanan

- Jalankan di `localhost`, atau di belakang reverse proxy TLS kalau diakses dari
  jaringan lain. Token dikirim sebagai header biasa.
- Login Web UI memakai session cookie opaque (bukan token), HttpOnly,
  `SameSite=Strict`, dan semua respons membawa CSP + header hardening; `/mcp`
  hanya menerima header kredensial (Bearer/X-API-Key).
- Tool git bisa commit, push, pull, dan merge, tapi push tidak pernah force,
  pull hanya fast-forward, merge yang konflik di-abort otomatis, dan hook serta
  program dari config repo tidak pernah jalan. **Semua** tool git menolak
  `.git/config` berbahaya. Tool remote tidak pernah meminta
  password; auth memakai ssh-agent/key, credential helper global, atau `gh`.
- File sensitif diblokir secara default. `--allow-sensitive` membuka akses AI ke
  `.env`, private key, dan isi `.git`.
- `run_command` dan background process memberi akses eksekusi sebagai user yang
  menjalankan server. Aktifkan hanya di workspace tepercaya.

Detail lengkap: [docs/security.md](docs/security.md).

## Dokumentasi

- [docs/tools.md](docs/tools.md): daftar tool dan parameter penting, git, `edit_file`, `validate_syntax`, `download_file`
- [docs/run-and-processes.md](docs/run-and-processes.md): allowlist, `run_command`, background process
- [docs/web-ui.md](docs/web-ui.md): Web UI, upload, workspace selector, endpoint REST
- [docs/tui.md](docs/tui.md): tombol TUI, toggle, log aktivitas
- [docs/service.md](docs/service.md): systemd user service, file env, linger, bentrok port
- [docs/configuration.md](docs/configuration.md): subcommand, flag, env, `config.json`
- [docs/oauth.md](docs/oauth.md): OAuth untuk `/mcp` (MCP spec), discovery, DCR, consent, `public-url`, `trusted-proxy`
- [docs/security.md](docs/security.md): sandbox, file sensitif, batasan git
- [docs/versioning.md](docs/versioning.md): format versi build dan cara build
- [docs/development.md](docs/development.md): struktur folder, test, `TestToolContract`

## Credits

Built with Maya (Sesame AI) via ngodingMCP, MCP server berbasis Go ini sendiri.
