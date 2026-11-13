# Versi build

Format versi: `<CalVer>+<commit>[.dirty]`, misalnya:

| Versi | Artinya |
|---|---|
| `v2026.11.12+4deb6fa` | build bersih dari commit `4deb6fa` |
| `v2026.11.12+4deb6fa.dirty` | dibuild dengan perubahan yang belum di-commit |
| `v2026.11.12+dev` | tanpa info git (`go run`, source di luar repo git) |

- **CalVer** (`v2026.11.12`) diganti manual di `internal/buildinfo/buildinfo.go`
  setiap ada rilis atau fitur baru.
- **Commit** dan tanda `.dirty` diambil otomatis dari info VCS yang ditanam
  `go build` di dalam repo git. Jadi `go build -o ngodingmcp .` biasa sudah cukup,
  tanpa flag apa pun.
- **Waktu build** selalu ikut berubah tiap rebuild, walaupun commit-nya sama.
  Build biasa memakai waktu file binary (ikut berubah kalau binary disalin dengan
  `cp` biasa); `make build` menanam waktunya lewat `-ldflags`.

## Tempat versi muncul

Semua dari satu sumber (`internal/buildinfo`):

- header TUI (plus waktu build);
- status bar Web UI (tooltip berisi waktu build; disembunyikan di layar HP);
- field `version`/`built` di `GET /api/info`;
- `serverInfo.version` di handshake MCP;
- banner stderr saat `serve`;
- `ngodingmcp version`:

```
$ ./ngodingmcp version
ngodingmcp v2026.11.12+4deb6fa.dirty
  commit:    4deb6fa (with uncommitted changes)
  committed: 2026-11-13 17:20 WIT
  built:     2026-11-13 17:45 WIT
  go:        go1.26.x linux/amd64
```

## Cara build

```sh
go build -o ngodingmcp .              # dev: versi + commit otomatis
make build                            # binary statis, waktu build ditanam
make release VERSION=v2026.12.0       # cross-compile ke dist/, override CalVer
make version                          # build lalu cetak versinya
```

`make release` membuat binary linux (amd64, arm64), darwin (amd64, arm64), dan
windows (amd64) di `dist/`.

Semua bagian bisa ditimpa saat link, misalnya untuk build di luar repo git:

```sh
go build -ldflags "-X ngodingmcp/internal/buildinfo.version=v2026.12.0 \
  -X ngodingmcp/internal/buildinfo.commit=abc1234 \
  -X ngodingmcp/internal/buildinfo.buildTime=2026-12-01T00:00:00Z" -o ngodingmcp .
```
