# NgodingMCP

MCP server berbasis Go yang memperlakukan sebuah folder sebagai *workspace* teks
yang bisa dikontrol AI lewat tools, sekaligus bisa diedit manusia lewat UI web.
Transport-nya HTTP (MCP Streamable HTTP), dengan autentikasi token.

```
AI client ──HTTP/JSON-RPC──┐
                           ├──► ngodingmcp ──► workspace/ (file di disk)
Browser (UI editor) ───────┘
```

## Fitur

- **26 MCP tools**: `read_file`, `write_file`, `replace_text`, `edit_file`, `replace_lines`,
  `insert_text`, `delete_lines`, `list_files`, `search_text`, `search_and_replace`, `file_info`,
  `workspace_stats`, `validate_syntax`, `count_tokens`, `mkdir_path`, `move_path`, `delete_path`,
  `git_status`, `git_diff`, `git_log`, `git_init`, `git_add`, `git_unstage`, `git_commit`,
  `git_create_branch`, `git_switch_branch`
- **`edit_file`**: beberapa edit exact-text dalam satu langkah atomik (semua berhasil
  atau tidak ada yang ditulis), plus unified diff. `dry_run: true` untuk preview saja.
- **Git read-only**: `git_status`, `git_diff` (opsi `path`, `staged`, `stat`, `ref`),
  `git_log` (opsi `path`, `limit`). Bila workspace bukan repo git, tool membalas
  pesan penjelasan (arahkan ke `git_init`), bukan error.
- **Git write (modul inti, selalu aktif)**: `git_init` (branch awal `main`),
  `git_add` (`paths` atau `all: true`; file ignored tidak pernah di-stage, file
  sensitif seperti `.env`/private key dilewati dan dilaporkan), `git_unstage`
  (file di workspace tidak berubah), `git_commit` (`message`, opsional
  `author_name` + `author_email`), `git_create_branch` (opsi `start`, `switch`),
  `git_switch_branch`. Batas aman: **tidak ada** push, pull, fetch, merge,
  rebase, reset --hard, clean, checkout file, hapus branch, amend, atau force.
  Hook repo (`core.hooksPath`, husky) dan GPG signing tidak pernah dijalankan.
  Commit ditolak bila ada file sensitif yang ter-stage. Hanya bekerja bila root
  workspace = root repo (workspace di dalam repo lain ditolak, kecuali `git_init`).
- **`validate_syntax`**: parse penuh untuk Go dan JSON; cek delimiter (kurung
  seimbang, string/template literal/regex/komentar tertutup) untuk JS/TS/CSS.
  Hasil memuat field `check`: `parser`, `delimiters`, atau `none`.
- **`run_command` (opt-in)**: eksekusi command dari allowlist tanpa shell, dengan
  timeout dan batas output. Tidak aktif kecuali `--allow-run` diberikan.
- **`download_file` (opt-in)**: unduh file teks/biner (mis. gambar WebP) dari URL
  https ke workspace. Tidak aktif kecuali `--allow-download` atau toggle di TUI.
- **Upload lewat web UI**: drag-and-drop atau pilih file, tentukan folder tujuan.
- **Background processes (opt-in)**: `start_process`, `list_processes`,
  `process_logs`, `stop_process`, `restart_process`, `stop_all_processes` untuk
  dev server dan proses long-running lain, lengkap dengan readiness check,
  cursor log, dan panel di TUI/web UI.
- **Web UI** bawaan (embedded, tanpa dependency frontend): file tree, editor,
  search, buat/rename/hapus file, save via `Ctrl+S`, light/dark mode, dan
  viewer log proses dengan tombol stop/restart
- **TUI control panel**: atur root/address/token, toggle run & file sensitif,
  start/stop, lihat log live plus aktivitas tool AI per baris
- **Sandbox**: semua path relatif ke root, `..` dan symlink keluar root ditolak
- **Write atomik**: tulis ke temp file lalu `rename`
- **Batas ukuran baca** dan deteksi file biner
- **Auth token** untuk MCP/REST (`Authorization: Bearer` / `X-API-Key`) dan
  login cookie untuk browser

## Cara tercepat: TUI

Ada control panel interaktif, jadi kamu tidak perlu hafal flag:

```sh
cd ngodingmcp
go build -o ngodingmcp .
./ngodingmcp
```

Tanpa argumen di terminal, `ngodingmcp` langsung membuka TUI. Kamu juga bisa
memanggilnya eksplisit dengan `./ngodingmcp tui`.

Di dalam TUI:

| Tombol | Fungsi |
|---|---|
| `Tab` / `Shift+Tab` | pindah field |
| `Space` | ubah toggle yang sedang disorot (`run_command` + processes, atau file sensitif) |
| `Enter` | start / stop server |
| `Ctrl+R` | restart |
| `Ctrl+O` | buka UI di browser |
| `Ctrl+X` | hentikan semua background process |
| `Ctrl+T` | tampilkan/sembunyikan token |
| `Ctrl+L` | bersihkan log |
| `Ctrl+Q` | keluar (server ikut dimatikan) |

Isi *Workspace root*, *Listen address*, dan *Auth token* (kosongkan token
agar dibuat otomatis). Toggle **Enable run_command & processes** dengan `Space`;
daftar command yang diizinkan sudah di-predefine di kode (lihat bagian
*Eksekusi command*). Tekan `Enter` untuk start, `Ctrl+R` untuk restart.

Toggle **Allow sensitive files** (default mati) sama dengan flag
`--allow-sensitive`: saat dinyalakan, TUI menampilkan peringatan merah karena AI
bisa membaca/menulis `.env`, private key, dan isi `.git`.

Saat server jalan, TUI menampilkan satu baris ringkasan, misalnya
`tools: 27 active · run: on · sensitive guard: on`, dan setiap tool call dari AI
muncul di log sebagai satu baris ringkas:

```
09:41:02 ▸ edit_file internal/app/app.go ✓ 4ms
09:41:05 ▸ run_command go test ./... ✗ exit 1 3.2s
```

Yang dicatat hanya nama tool, target utama (path, command, atau nama proses),
berhasil/gagal, dan durasi. Isi file, teks edit, env, token, dan pesan error
tidak pernah ditulis ke log. Baris yang sama juga muncul di log `serve`.

Semua setting disimpan di `~/.config/ngodingmcp/config.json` dan dipakai lagi
pada run berikutnya — termasuk saat menjalankan `ngodingmcp serve` tanpa flag.
Config ini hanya menyimpan flag enable; allowlist tidak bisa diubah dari sini.
Config berada **di luar workspace** agar agent tidak bisa mengubahnya. Karena
config menyimpan token dalam plaintext, file-nya ditulis dengan permission
`0600` dan foldernya `0700`; config lama yang lebih longgar ikut diperketat
saat disimpan.

## Build & jalankan (CLI)

```sh
cd ngodingmcp
go build -o ngodingmcp .

./ngodingmcp serve \
  --addr 127.0.0.1:8080 \
  --root /path/ke/workspace \
  --token rahasia-panjang
```

Subcommand yang tersedia:

| Command | Fungsi |
|---|---|
| `ngodingmcp` | Server; di terminal interaktif membuka TUI |
| `ngodingmcp serve [flags]` | Start server HTTP secara eksplisit |
| `ngodingmcp tui [flags]` | Buka control panel (`--root`, `--addr`, `--token` sebagai preset) |
| `ngodingmcp version` | Cetak versi (juga `-v`, `--version`) |
| `ngodingmcp help` | Tampilkan bantuan (juga `-h`, `--help`) |

Flag lama tetap kompatibel: `ngodingmcp --addr ... --root ...` masih menjalankan
server. Subcommand yang tidak dikenal akan ditolak dengan pesan error.

Jika `--token` kosong, server membuat token acak dan mencetaknya ke stderr.
`--root` dibuat otomatis bila belum ada.

Konfigurasi lewat environment juga bisa. `serve` membaca config TUI lebih dulu,
lalu environment menimpa config, dan flag menimpa keduanya:

| Flag | Env | Default |
|---|---|---|
| `--addr` | `NGODINGMCP_ADDR` | `127.0.0.1:8080` |
| `--root` | `NGODINGMCP_ROOT` | `~/ngodingmcp-workspace` |
| `--token` | `NGODINGMCP_TOKEN` | dibuat acak |
| `--max-file-bytes` | – | `2097152` (2 MiB) |
| `--allow-run` | `NGODINGMCP_ALLOW_RUN` | `false` |
| `--run-allow` | `NGODINGMCP_RUN_ALLOWLIST` | kosong (pakai predefined) |
| `--run-allow-exact` | `NGODINGMCP_RUN_ALLOWLIST_EXACT` | kosong (pakai predefined) |
| `--run-timeout` | `NGODINGMCP_RUN_TIMEOUT` | `1m` |
| `--allow-sensitive` | `NGODINGMCP_ALLOW_SENSITIVE` | `false` (file sensitif diblokir) |
| `--allow-download` | `NGODINGMCP_ALLOW_DOWNLOAD` | `false` |
| `--download-allow-host` | `NGODINGMCP_DOWNLOAD_ALLOW_HOSTS` | kosong (semua host publik) |
| `--download-max-bytes` | `NGODINGMCP_DOWNLOAD_MAX_BYTES` | `20971520` (20 MiB) |
| `--upload-max-bytes` | `NGODINGMCP_UPLOAD_MAX_BYTES` | `20971520` (20 MiB per file) |

Buka `http://127.0.0.1:8080/`, masukkan token, lalu edit seperti editor biasa.

### Download file dari URL (opt-in)

`write_file` hanya menerima teks. Untuk file biner, aktifkan `download_file`
(toggle **Enable download_file** di TUI, atau):

```bash
./ngodingmcp serve --allow-download
# opsional: batasi host dan ukuran
./ngodingmcp serve --allow-download --download-allow-host "*.example.com" --download-max-bytes 52428800
```

Argumen: `source_url` (wajib, https), `path` (wajib, relatif ke root),
`overwrite` (default `false`), `sha256` (opsional, 64 hex). Hasil: `path`,
`bytes`, `sha256`, `contentType` (hanya informasi dari server).

Pengaman:

- hanya `https://`, tanpa `user:pass@`; redirect maks. 5 dan tiap hop dicek ulang;
- alamat loopback, privat, link-local (termasuk metadata cloud `169.254.169.254`),
  CGNAT/Tailscale (`100.64.0.0/10`) dan range reserved ditolak **saat dial**, jadi
  DNS rebinding dan redirect tidak bisa melewatinya; proxy dari environment diabaikan;
- batas ukuran dihitung dari byte yang benar-benar dibaca, bukan `Content-Length`;
  timeout total 2 menit;
- path tujuan lewat sandbox dan guard file sensitif; ditulis atomik (temp file +
  rename), tidak ada file setengah jadi bila gagal;
- log aktivitas hanya mencatat host dan path tujuan, tidak pernah query string/token.

Allowlist host kosong secara default karena host URL bertanda tangan (signed URL)
dari MCP client tidak diketahui sebelumnya.

### Upload lewat web UI

Tombol **↑ upload** di sidebar membuka dialog: drop file atau klik *choose files*,
isi folder tujuan (relatif ke root), centang *overwrite* bila perlu. Endpoint:
`POST /api/upload?dir=<folder>&overwrite=true|false` (multipart, field `file`,
boleh banyak; maks. 20 file per request), wajib login/token. Aturannya sama dengan
`download_file`: sandbox path, guard file sensitif, batas ukuran per file
(`--upload-max-bytes`), write atomik, nama file harus nama polos (tanpa `/`, `\`,
`..`). File yang sudah ada dibalas `409` kecuali `overwrite=true`; terlalu besar
`413`; path sensitif/di luar root `403`. Respons memuat hasil per file.

### Eksekusi command (opt-in)

`run_command` hanya tersedia bila diaktifkan. Cukup toggle di TUI, atau:

```sh
./ngodingmcp serve --allow-run
```

Saat enable, policy memakai daftar **predefined di kode** (`internal/runner/policy.go`):

| Prefix (argumen tambahan bebas) | Exact (harus sama persis) |
|---|---|
| `go build` | `bun install --frozen-lockfile` |
| `go test` | `bun test` |
| `go vet` | `bun run build` |
| `gofmt -l` | `bun run dev` |
| `git status` | |
| `git diff` | |
| `git log` | |

Override hanya jika perlu (advanced):

```sh
./ngodingmcp serve --allow-run --run-allow "go build,go test,make"
./ngodingmcp serve --allow-run --run-allow-exact "bun install --frozen-lockfile"
```

Config hanya menyimpan enable flag:

```json
{
  "run": { "enabled": true }
}
```

Precedence: **flag > environment > config**. Workspace tidak pernah dipakai
sebagai sumber policy.

Aturannya:

- **Prefix**: `go test ./...` cocok dengan entri `go test`.
- **Exact**: `bun install --frozen-lockfile` **hanya** cocok persis;
  `bun install paket-lain` ditolak.
- Tidak pernah lewat shell — metacharacter tidak diinterpretasi.
- `cwd` selalu workspace root, timeout default 60 detik (bisa diubah per request,
  dibatasi oleh `--run-timeout`).
- Output digabung stdout+stderr dan dipotong pada 256 KiB.
- Exit code non-zero tetap dikembalikan sebagai hasil (bukan error protokol),
  supaya AI bisa membaca kegagalan build/test.
- Saat timeout, seluruh process group dibunuh (termasuk child process).
- Tanpa `--allow-run`, tool ini tidak terdaftar sama sekali (tetap 20 tools).
  Dengan `--allow-run`, totalnya 27 tool: 20 editor/git + `run_command` + 6 tool proses.

> Jangan mendaftarkan command yang sama di prefix dan exact sekaligus: entri
> prefix akan membuka kembali argumen bebas. Untuk installer gunakan **hanya**
> `--run-allow-exact`.

> Allowlist adalah batas kepercayaan. Entri seperti `go test` tetap bisa
> menjalankan kode dari workspace; aktifkan hanya untuk workspace yang dipercaya.

### Background process (opt-in)

`run_command` bersifat request/response: proses harus selesai dalam timeout.
Untuk dev server atau watcher yang harus tetap hidup, gunakan tool proses yang
ikut aktif bersama `--allow-run`:

| Tool | Fungsi |
|---|---|
| `start_process` | jalankan command allowlisted di background; bisa menunggu log/port siap |
| `list_processes` | status semua proses (starting/running/stopping/exited, exit code, runtime) |
| `process_logs` | baca stdout+stderr, tail atau incremental lewat cursor |
| `stop_process` | SIGTERM, lalu SIGKILL setelah grace period (bisa non-blocking) |
| `restart_process` | stop lalu start ulang dengan argv/cwd/env yang sama |
| `stop_all_processes` | hentikan semua proses sekaligus |

Contoh alur untuk `bun run dev` (sudah ada di predefined exact policy), dengan
readiness check supaya `start_process` baru kembali ketika server benar-benar
siap:

```json
{
  "command": "bun run dev",
  "name": "dev",
  "wait_for_log": "ready|listening",
  "wait_for_port": 3000,
  "timeout_seconds": 20
}
```

Lalu baca log secara incremental:

```json
{ "name": "dev", "after_seq": 0, "tail_lines": 100 }
```

Response berisi `nextSeq`; panggilan berikutnya cukup mengirim
`{"name":"dev","after_seq": <nextSeq>}` untuk mendapatkan baris baru saja.
Timestamp per baris aktif secara default (`timestamps: false` untuk mematikan).
Output ANSI dan progress bar (`\r`) sudah dibersihkan.

Parameter `start_process` lain:

| Field | Keterangan |
|---|---|
| `cwd` | direktori relatif di dalam workspace (default root) |
| `env` | hanya `PORT`, `HOST`, `HOSTNAME`, `NODE_ENV`, `BUN_ENV`, `CI` |
| `wait_for_log` | regex Go yang harus muncul di output |
| `wait_for_port` | port TCP yang harus menerima koneksi |
| `timeout_seconds` | timeout readiness (default 10, maks 60) |

`stop_process` menerima `wait: false` untuk langsung kembali; eskalasi SIGKILL
tetap berjalan di background. `restart_process` memakai ulang konfigurasi proses.

Batasan desain:

- Command divalidasi oleh allowlist yang **sama** dengan `run_command`; tidak ada
  shell, dan `cwd` tidak bisa keluar dari workspace.
- Maksimal 8 proses berjalan bersamaan dan 64 entri terlacak; entri yang sudah
  exit dihapus otomatis saat penuh.
- Log per proses disimpan di ring buffer 128 KiB dengan cursor per baris.
- Nama proses: huruf, angka, `_`, `-` (maks 32 karakter); kosong = `proc-N`.
  Nama otomatis tidak akan bentrok dengan nama manual.
- `stop_process` mengirim sinyal ke seluruh process group/tree, jadi child
  process ikut berhenti (Job Object di Windows).
- Saat server di-shutdown, semua proses yang dikelola ikut dihentikan (grace 3
  detik, lalu force kill).
- Proses **tidak** bertahan setelah server mati; ini disengaja agar tidak ada
  orphan process.
- TUI menampilkan daftar proses dan `Ctrl+X` untuk stop semua. Di web UI, panel
  Processes punya tombol restart dan stop per proses; klik prosesnya untuk
  membuka log (refresh tiap detik lewat cursor `nextSeq`, stderr berwarna
  merah, opsi follow) lengkap dengan tombol restart/stop.
- Endpoint REST web UI (perlu auth): `GET /api/processes/{name}/logs?tail=200`
  atau `?after=<nextSeq>`, `POST /api/processes/{name}/stop`,
  `POST /api/processes/{name}/restart`.

> `bun run dev` hanya masuk akal lewat `start_process`. Jika dipanggil lewat
> `run_command`, ia akan tetap berjalan sampai timeout lalu dibunuh.

## Menghubungkan MCP client

Endpoint: `POST http://127.0.0.1:8080/mcp`

Contoh konfigurasi (format klien MCP yang mendukung HTTP + header):

```json
{
  "mcpServers": {
    "ngodingmcp": {
      "type": "http",
      "url": "http://127.0.0.1:8080/mcp",
      "headers": {
        "Authorization": "Bearer rahasia-panjang"
      }
    }
  }
}
```

## Catatan keamanan

- Token dikirim sebagai header biasa. Jalankan di `localhost`, atau taruh di
  belakang reverse proxy TLS bila diakses dari jaringan lain.
- Root workspace adalah batas keamanan. Jangan menaruh symlink ke luar root bila
  tidak yakin; symlink semacam itu tetap ditolak saat diakses.
- Tool `write_file` bersifat destruktif (menimpa file). Gunakan `replace_text`
  untuk edit kecil.
- `run_command` dan background process memberi akses eksekusi sebagai user yang
  menjalankan server. Aktifkan hanya dengan allowlist minimal dan di workspace
  tepercaya.
- `start_process`/`stop_process` dapat menyalakan dan mematikan proses; seluruh
  proses dimatikan saat server shutdown.
- **File sensitif diblokir secara default**: `.env` dan `.env.*` (kecuali template
  seperti `.env.example`, `.env.sample`, `.env.template`, `.env.dist`), folder
  `.git`, `.ssh`, `.aws`, `.gnupg`, file `.npmrc`, `.pypirc`, `.netrc`,
  `.git-credentials`, `.htpasswd`, `id_rsa`/`id_dsa`/`id_ecdsa`/`id_ed25519`, dan
  ekstensi `.pem`, `.key`, `.p12`, `.pfx`, `.jks`, `.keystore`. File ini tidak bisa
  dibaca/ditulis/diedit, disembunyikan dari list, search, stats, dan count, dan
  delete rekursif ditolak bila foldernya berisi file tersebut. Matikan dengan
  `--allow-sensitive` (atau `NGODINGMCP_ALLOW_SENSITIVE=true`) hanya bila perlu.
- **Error tanpa path host**: pesan error tool tidak pernah memuat path absolut
  workspace di host; path diganti menjadi path relatif terhadap root.
- Tool git hanya read-only dan tidak menjalankan program dari config repo
  (fsmonitor, pager, external diff, textconv dimatikan).

## Struktur

```
ngodingmcp/
├── main.go                     # entrypoint: `ngodingmcp` (server) / `ngodingmcp tui`
├── internal/
│   ├── app/                    # wiring server bersama (dipakai CLI & TUI)
│   ├── auth/                   # token auth (Bearer/X-API-Key/cookie)
│   ├── config/                 # setting TUI + run policy (di luar workspace)
│   ├── tui/                    # control panel (bubbletea)
│   ├── runner/                 # eksekusi command allowlisted (opt-in)
│   ├── procman/                # background process manager (opt-in)
│   ├── workspace/              # operasi file tersandbox + atomic write,
│   │                           #   blokir file sensitif, edit_file + diff, cek delimiter
│   ├── fetch/                  # download_file: https + guard IP saat dial
│   ├── gitread/                # git status/diff/log read-only
│   ├── gitwrite/               # git init/add/unstage/commit/branch (tanpa push/force/hook)
│   ├── mcpserver/              # definisi MCP tools, satu file per domain:
│   │   ├── server.go           #   Options, konstruktor, daftar tool aktif, middleware
│   │   ├── helpers.go          #   textResult, preset anotasi, helper kecil
│   │   ├── safe_error.go       #   scrub path host dari error
│   │   ├── activity.go         #   log aktivitas per tool call
│   │   ├── tools_file.go       #   read/write/list/info/mkdir/move/delete
│   │   ├── tools_edit.go       #   replace_text, edit_file, insert/delete/replace_lines
│   │   ├── tools_search.go     #   search_text, search_and_replace
│   │   ├── tools_inspect.go    #   workspace_stats, validate_syntax, count_tokens
│   │   ├── tools_git.go        #   git read-only + git write
│   │   ├── tools_run.go        #   run_command (opt-in)
│   │   ├── tools_process.go    #   background process tools (opt-in)
│   │   ├── tools_download.go   #   download_file (opt-in)
│   │   └── testdata/tools.golden.json  # kontrak semua tool (TestToolContract)
│   └── webui/                  # REST API (termasuk /api/upload) + embedded UI
│       └── web/                # index.html, login.html, app.js, style.css
└── internal/*/*_test.go        # config, gitwrite, mcpserver, procman, tui, workspace
```

## Test

```sh
go test ./...
```

Nama, deskripsi, anotasi, dan schema semua tool dikunci oleh `TestToolContract`
(`internal/mcpserver/testdata/tools.golden.json`). Kalau kontrak tool memang sengaja
diubah, perbarui snapshot lalu review diff-nya:

```sh
go test ./internal/mcpserver -run TestToolContract -args -update
```
