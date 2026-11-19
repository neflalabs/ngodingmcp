# AGENTS.md

Panduan singkat untuk AI yang bekerja di repo ini. File ini juga disajikan
sebagai resource MCP `ngodingmcp://agents`.

## Apa ini

`ngodingmcp` adalah server MCP yang mengekspos workspace di disk sebagai tool
editor, pencarian, inspect, dan git. Ada TUI (default), Web UI, dan mode
`serve`. Tool proses dan download bersifat opt-in.

## Struktur

- `main.go`, `service_cmd.go` — entrypoint dan subcommand service.
- `internal/app` — wiring server bersama CLI/TUI dan workspace manager.
- `internal/mcpserver` — definisi tool, satu file per domain (`tools_*.go`);
  `server.go` memilih tool aktif, `register.go` mengisi hasil tool.
- `internal/workspace` — operasi file tersandbox, atomic write, edit, outline.
- `internal/runner`, `internal/procman` — command allowlisted dan proses latar.
- `internal/gitread`, `internal/gitwrite` — git baca dan tulis/remote/GitHub.
- `internal/webui`, `internal/tui` — antarmuka; `internal/fetch` — download.
- `docs/` — dokumentasi pengguna dan pengembangan (bahasa Indonesia).
- `TaskDev/` — catatan task, di-gitignore.

## Konvensi

- Utamakan Go stdlib; jangan menambah dependency tanpa alasan kuat.
- Semua path tool relatif ke root workspace dan tidak boleh keluar root.
- Jangan menjalankan shell; command runner hanya dari allowlist.
- Pesan error harus bisa ditindaklanjuti dan tidak membocorkan path host.
- Dokumentasi dan komentar pengguna berbahasa Indonesia; komentar kode singkat.
- Deskripsi/schema tool harus ringkas: `TestToolsListBudget` menjaga ukuran
  `tools/list`. Jangan menaikkan budget tanpa keputusan sadar.
- Perubahan kontrak tool: perbarui `testdata/tools.golden.json` dengan
  `go test ./internal/mcpserver -run TestToolContract -args -update`, lalu
  review diff-nya.
- Tool berisi konten besar memakai `addTextTool`; tool konfirmasi memakai
  `addUntypedTool`. `outputSchema` hanya untuk record kecil yang benar-benar
  dibutuhkan client.
- Perubahan perilaku pengguna harus disertai update `docs/` dan TaskDev.
- Gaya commit: `feat(scope): ringkasan (06 Fase N)` atau `fix(scope): ...`.

## Alur kerja AI

1. Baca `AGENTS.md` (resource `ngodingmcp://agents`) sebelum eksplorasi.
2. Cari dengan `search_text`/`list_files`; baca struktur dengan `outline`.
3. Baca hanya simbol yang perlu (`read_symbol`) atau beberapa rentang sekaligus
   (`read_files`). Gunakan `if_changed_since` untuk melewati file yang sama.
4. Edit dengan `replace_text`/`edit_file`; `write_file` hanya untuk file baru
   atau rewrite total.
5. Setelah perubahan: `go test ./...`; sebelum selesai: `make check`.

## Perintah

```sh
go test ./...        # tes cepat (atau make test)
make check           # gofmt, go vet, staticcheck, go test -race (sama dengan CI)
go build ./...       # build semua paket
make build           # binary ./ngodingmcp di root
```
