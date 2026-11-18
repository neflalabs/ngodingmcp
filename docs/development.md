# Development

## Struktur

```
ngodingmcp/
├── main.go                     # entrypoint + routing subcommand: TUI (default), serve, tui,
│                               #   version, help; -d/status/logs/restart/stop ke service_cmd.go
├── service_cmd.go              # subcommand -d, stop, restart, status, logs
├── Makefile                    # build, release, version, test, clean
├── docs/                       # dokumentasi detail (dirujuk dari README)
├── internal/
│   ├── app/                    # wiring server bersama (dipakai CLI & TUI), workspace manager
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
│   ├── fetch/                  # download_file: https + guard IP saat dial
│   ├── gitread/                # git status/diff/log read-only
│   ├── gitwrite/               # git init/add/commit/branch + remote/fetch/pull/push/merge, gh repo & actions (tanpa force/hook)
│   ├── mcpserver/              # definisi MCP tools, satu file per domain:
│   │   ├── server.go           #   Options, konstruktor, daftar tool aktif, middleware
│   │   ├── helpers.go          #   textResult, preset anotasi, helper kecil
│   │   ├── safe_error.go       #   scrub path host dari error, tahan workspace selama tool call
│   │   ├── activity.go         #   log aktivitas per tool call
│   │   ├── tools_file.go       #   read/write/list/info/mkdir/move/delete
│   │   ├── tools_edit.go       #   replace_text, edit_file, insert/delete/replace_lines
│   │   ├── tools_search.go     #   search_text, search_and_replace
│   │   ├── tools_inspect.go    #   workspace_stats, validate_syntax, count_tokens
│   │   ├── tools_git.go        #   git read-only + git write
│   │   ├── tools_git_remote.go #   git remote/fetch/pull/push/merge, git_github_create_repo & actions
│   │   ├── tools_run.go        #   run_command (opt-in)
│   │   ├── tools_process.go    #   background process tools (opt-in)
│   │   ├── tools_download.go   #   download_file (opt-in)
│   │   └── testdata/tools.golden.json  # kontrak semua 43 tool (TestToolContract)
│   └── webui/                  # REST API (termasuk /api/upload, /api/workspaces) + embedded UI
│       └── web/                # index.html, login.html, app.js, login.js, theme.js, style.css
├── main_test.go, service_cmd_test.go
└── internal/*/*_test.go        # app, buildinfo, config, fetch, gitwrite, mcpserver, procman,
                                #   oauth, runner, service, tui, webui, workspace
```

Folder `TaskDev/` (catatan pengembangan) dan `dist/` di-gitignore.

## Test

```sh
go test ./...        # atau: make test
```

Nama, deskripsi, anotasi, dan schema semua tool dikunci oleh `TestToolContract`
(`internal/mcpserver/testdata/tools.golden.json`, 43 tool dengan semua opt-in
aktif). Kalau kontrak tool memang sengaja diubah, perbarui snapshot lalu review
diff-nya:

```sh
go test ./internal/mcpserver -run TestToolContract -args -update
```

Menambah tool baru: tulis di file `tools_<domain>.go` yang sesuai, daftarkan di
`server.go`, lalu perbarui snapshot di atas dan jumlah tool di
[tools.md](tools.md) dan README.
