# Versi build

Format versi: `<CalVer>+<commit>[.dirty]`, misalnya:

| Versi | Artinya |
|---|---|
| `v2026.11.3+4deb6fa` | build rilis (tag `v2026.11.3` atau `VERSION=v2026.11.3`) dari commit `4deb6fa` |
| `v2026.11.19-dev+4deb6fa` | build development dari commit yang dibuat 19 November 2026 (UTC) |
| `v2026.11.19-dev+4deb6fa.dirty` | sama, tapi dibuild dengan perubahan yang belum di-commit |
| `v0.0.0+dev` | tanpa info git (`go run`, `go test`, source di luar repo git) |

- **CalVer** tidak pernah diganti manual. Urutannya:
  1. tag git eksak `v20*` di HEAD (dibaca `make build`/`make release`);
  2. `VERSION=...` yang diberikan ke `make` (dipakai workflow rilis:
     `make release VERSION=v2026.12.0`);
  3. kalau dua-duanya tidak ada: `v<tanggal commit>-dev`, dihitung di
     `internal/buildinfo` dari tanggal commit HEAD (UTC). Karena diambil dari
     commit, bukan dari jam build, `go build -o ngodingmcp .` dan `make build`
     menghasilkan versi yang sama untuk commit yang sama (selama HEAD belum
     punya tag rilis, lihat [Rilis](#rilis)).
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
ngodingmcp v2026.11.13-dev+4deb6fa.dirty
  commit:    4deb6fa (with uncommitted changes)
  committed: 2026-11-13 17:20 WIT
  built:     2026-11-13 17:45 WIT
  go:        go1.26.x linux/amd64
```

## Cara build

```sh
go build -o ngodingmcp .              # dev: v<tanggal commit>-dev + commit otomatis
make build                            # binary statis, waktu build ditanam; versi sama dengan go build
make version                          # build lalu cetak versinya
```

## Rilis

Workflow `release.yml` menentukan CalVer rilis `vYYYY.MM.N`: setiap push ke
`main` (selain perubahan `docs/` dan `README.md`) memakai nomor berikutnya di
bulan itu, push tag `v20*` memakai tag itu, dan run manual boleh diberi versi.
Workflow membuat tagnya lalu menjalankan `make release VERSION=<versi>`.

Tag rilis dibuat di GitHub. Setelah `git fetch --tags`, `make build` di commit
yang sudah dirilis ikut memakai versi rilisnya, sedangkan `go build` biasa tetap
menulis `v<tanggal commit>-dev` karena `go build` tidak membaca tag.

Secara lokal caranya sama:

```sh
make release VERSION=v2026.12.0       # cross-compile ke dist/ dengan CalVer itu
git tag v2026.12.0 && make build      # atau: tag dulu, make build memakai tag eksak di HEAD
```

`make release` membuat binary linux (amd64, arm64), apple (amd64, arm64), dan
windows (amd64) di `dist/`.

Semua bagian bisa ditimpa saat link, misalnya untuk build di luar repo git:

```sh
go build -ldflags "-X ngodingmcp/internal/buildinfo.version=v2026.12.0 \
  -X ngodingmcp/internal/buildinfo.commit=abc1234 \
  -X ngodingmcp/internal/buildinfo.buildTime=2026-12-01T00:00:00Z" -o ngodingmcp .
```

## Bersih-bersih

```sh
make clean
```

Menghapus `dist/` dan binary `./ngodingmcp`. Kalau `ngodingmcp.service`
(`ngodingmcp -d`) menjalankan binary dari folder ini, binary-nya **tidak**
dihapus supaya service tidak rusak; `make clean` mencetak pesan `kept ./ngodingmcp`.
Untuk menggantinya cukup `make build` (atau `go build -o ngodingmcp .`) lalu
`ngodingmcp restart`.
