# Development

## Struktur

```
ngodingmcp/
├── main.go                     # entrypoint + routing subcommand: TUI (default), serve, tui,
│                               #   version, help; -d/status/logs/restart/stop ke service_cmd.go
├── service_cmd.go              # subcommand -d, stop, restart, status, logs
├── AGENTS.md                   # panduan proyek, juga resource ngodingmcp://agents
├── Makefile                    # build, release, version, test, clean
├── docs/                       # dokumentasi detail (dirujuk dari README)
├── internal/
│   ├── app/                    # wiring server bersama (dipakai CLI & TUI), workspace manager
│   │   └── core.go             #   NewCore: inti MCP (workspace + tools) tanpa HTTP/auth/UI/TUI
│   ├── auth/                   # token auth (Bearer/X-API-Key) + session store UI (opaque)
│   ├── oauth/                  # embedded OAuth 2.1 AS+RS untuk /mcp (MCP spec)
│                               #   RFC 9728/8414 metadata, DCR (RFC 7591), PKCE,
│                               #   ES256 access token, consent screen, `oauth.json`
│   ├── buildinfo/              # versi binary: CalVer + commit git + waktu build
│   ├── config/                 # setting TUI, daftar workspace, run policy (di luar workspace)
│   ├── tui/                    # control panel (bubbletea)
│   ├── service/                # systemd user service: unit + env file, systemctl/journalctl
│   │                           #   lewat interface Commander (test tidak memanggil systemd)
│   ├── runner/                 # eksekusi command allowlisted (opt-in)
│   ├── procman/                # background process manager (opt-in)
│   ├── workspace/              # operasi file tersandbox + atomic write, pindah root saat jalan,
│   │                           #   blokir file sensitif, edit_file + diff, cek delimiter
│   ├── fetch/                  # download_file: https + guard IP saat dial (interface Workspace sempit)
│   ├── gitread/                # git status/diff/log read-only (interface Workspace sempit)
│   ├── gitwrite/               # git init/add/commit/branch + remote/fetch/pull/push/merge, gh repo & actions (tanpa force/hook; interface Workspace sempit)
│   ├── mcpserver/              # definisi MCP tools, satu file per domain:
│   │   ├── server.go           #   Options (tool + identitas), konstruktor, daftar tool, middleware
│   │   ├── workspace.go        #   interface Workspace: batas antara MCP dan implementasi workspace
│   │   ├── helpers.go          #   textResult, preset anotasi, helper kecil
│   │   ├── register.go         #   addTextTool / addUntypedTool: isi hasil dan outputSchema
│   │   ├── modules.go          #   modul tool inti yang bisa dimatikan (--disable-tools)
│   │   ├── safe_error.go       #   scrub path host dari error, tahan workspace selama tool call
│   │   ├── activity.go         #   log aktivitas per tool call
│   │   ├── resources.go        #   resource ngodingmcp://agents (AGENTS.md root)
│   │   ├── tools_file.go       #   read/write/list/info/mkdir/move/delete
│   │   ├── tools_read.go       #   outline, read_symbol, read_files
│   │   ├── tools_edit.go       #   replace_text, edit_file, insert/delete/replace_lines
│   │   ├── tools_search.go     #   search_text, search_and_replace
│   │   ├── tools_inspect.go    #   workspace_stats, validate_syntax, count_tokens
│   │   ├── tools_git.go        #   git read-only + git write
│   │   ├── tools_git_remote.go #   git remote/fetch/pull/push/merge, git_github_create_repo & actions
│   │   ├── tools_run.go        #   run_command (opt-in)
│   │   ├── tools_process.go    #   background process tools (opt-in)
│   │   ├── tools_download.go   #   download_file (opt-in)
│   │   └── testdata/tools.golden.json  # kontrak semua 46 tool (TestToolContract)
│   └── webui/                  # REST API (termasuk /api/upload, /api/workspaces) + embedded UI
│       └── web/                # index.html, login.html, app.js, login.js, theme.js, style.css
├── main_test.go, service_cmd_test.go
└── internal/*/*_test.go        # app, buildinfo, config, fetch, gitwrite, mcpserver, procman,
                                #   oauth, runner, service, tui, webui, workspace
```

Folder `TaskDev/` (catatan pengembangan) dan `dist/` di-gitignore.

## Core vs appliance

Konstruksi inti MCP sengaja dipisah dari lapisan *appliance* (HTTP, auth, Web
UI, TUI, systemd) supaya bisa dipakai ulang:

* `app.NewCore(Config)` menghasilkan `Core{Workspace, MCPServer, Runner,
  Processes, Downloader, ToolCount, Logger, Disabled}` tanpa membuka listener
  dan tanpa mengimpor auth/UI/service. `app.New` lalu menggabungkan `Core`
  dengan token/OAuth, Web UI, mux HTTP, dan transport Streamable HTTP
  `Stateless: true`.
* `mcpserver.NewWithOptions(ws Workspace, version, Options)` menerima
  **interface**, bukan `*workspace.Workspace` konkret. `fetch`, `gitread`, dan
  `gitwrite` juga punya interface sempit masing-masing; tipe konkret
  `workspace.Workspace` memenuhinya.
* `mcpserver.Options` berisi opsi tool (`Runner`, `Processes`, `Downloader`,
  `Disabled`, `Logger`) dan opsi identitas (`Name`, `Title`, `Instructions`,
  `AgentsURI`). Semua opsi identitas punya default yang sama seperti sebelumnya
  (`ngodingmcp`, `NgodingMCP`, teks editor, `ngodingmcp://agents`), jadi perilaku
  server tidak berubah.
* `*mcp.Server` yang dikembalikan bersifat transport-agnostic: bisa dijalankan
  lewat HTTP maupun `mcp.StdioTransport`. `ngodingmcp` sendiri tetap memakai
  HTTP stateless.

## Test

```sh
go test ./...        # atau: make test
make check           # cek yang sama dengan CI: gofmt, go vet, staticcheck, go test -race
```

`make check` menjalankan staticcheck lewat `go run` dengan versi yang dikunci
(`STATICCHECK_VERSION` di Makefile, sama dengan `.github/workflows/ci.yml`),
jadi tidak perlu install staticcheck dan `go.mod` tidak berubah. Kalau versinya
dinaikkan, ubah di kedua tempat.

Build, versi, dan `make clean`: lihat [versioning.md](versioning.md).

Nama, deskripsi, anotasi, dan schema semua tool dikunci oleh `TestToolContract`
(`internal/mcpserver/testdata/tools.golden.json`, 46 tool dengan semua opt-in
aktif). Kalau kontrak tool memang sengaja diubah, perbarui snapshot lalu review
diff-nya:

```sh
go test ./internal/mcpserver -run TestToolContract -args -update
```

Menambah tool baru: tulis di file `tools_<domain>.go` yang sesuai, daftarkan di
`server.go`, lalu perbarui snapshot di atas dan jumlah tool di
[tools.md](tools.md) dan README. Daftarkan lewat `addTextTool` kalau hasilnya
isi file/listing/log, `addUntypedTool` kalau hasilnya konfirmasi, dan
`mcp.AddTool` hanya untuk record kecil yang memang butuh `outputSchema` (lihat
[tools.md](tools.md#isi-hasil-tool)).

Ukuran `tools/list` dijaga `TestToolsListBudget` (`tools_budget_test.go`):
test gagal kalau daftar tool melewati budget. Rincian per tool dan per field:

```sh
go test ./internal/mcpserver -run 'TestToolsListTokens|TestToolsListBudget' -v
```
