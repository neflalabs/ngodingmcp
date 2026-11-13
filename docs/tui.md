# TUI control panel

Control panel interaktif, jadi kamu tidak perlu hafal flag:

```sh
go build -o ngodingmcp .
./ngodingmcp
```

Tanpa argumen di terminal, `ngodingmcp` langsung membuka TUI. Bisa juga
eksplisit dengan `./ngodingmcp tui` (dengan `--root`, `--addr`, `--token` sebagai preset).

## Tombol

| Tombol | Fungsi |
|---|---|
| `Tab` / `Shift+Tab` | pindah field |
| `Space` | ubah toggle yang sedang disorot (`run_command` + processes, file sensitif, download) |
| `Enter` | start / stop server |
| `Ctrl+R` | restart |
| `Ctrl+O` | buka UI di browser |
| `Ctrl+X` | hentikan semua background process |
| `Ctrl+T` | tampilkan/sembunyikan token |
| `Ctrl+L` | bersihkan log |
| `Ctrl+Q` | keluar (server ikut dimatikan) |

## Field dan toggle

- Isi *Workspace root*, *Listen address*, dan *Auth token* (kosongkan token agar
  dibuat otomatis).
- **Enable run_command & processes**: daftar command yang diizinkan sudah
  di-predefine di kode, lihat [run-and-processes.md](run-and-processes.md).
- **Enable download_file**: sama dengan `--allow-download`.
- **Allow sensitive files** (default mati): sama dengan `--allow-sensitive`. Saat
  dinyalakan, TUI menampilkan peringatan merah karena AI bisa membaca/menulis
  `.env`, private key, dan isi `.git`.

Semua setting disimpan di `~/.config/ngodingmcp/config.json` dan dipakai lagi
pada run berikutnya, termasuk saat menjalankan `ngodingmcp serve` tanpa flag.
Lihat [configuration.md](configuration.md).

## Ringkasan dan log aktivitas

Saat server jalan, TUI menampilkan satu baris ringkasan, misalnya:

```
tools: 39 active  ·  run: on  ·  sensitive guard: on
```

(32 tool default, 39 dengan run aktif, lihat [tools.md](tools.md).) Header TUI
juga menampilkan versi dan waktu build, lihat [versioning.md](versioning.md).

Setiap tool call dari AI muncul di log sebagai satu baris ringkas:

```
09:41:02 ▸ edit_file internal/app/app.go ✓ 4ms
09:41:05 ▸ run_command go test ./... ✗ exit 1 3.2s
```

Yang dicatat hanya nama tool, target utama (path, command, atau nama proses),
berhasil/gagal, dan durasi. Isi file, teks edit, env, token, dan pesan error
tidak pernah ditulis ke log. Baris yang sama juga muncul di log `serve`.

## Bentrok dengan service background

Kalau service systemd sedang jalan dan kamu menekan start di alamat yang sama,
TUI menampilkan "the background service is already running on …" beserta alamat
Web UI-nya. Lihat [service.md](service.md).
