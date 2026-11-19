# Konfigurasi

## Subcommand

| Command | Fungsi |
|---|---|
| `ngodingmcp` | Server; di terminal interaktif membuka TUI |
| `ngodingmcp serve [flags]` | Start server HTTP di foreground tanpa TUI (yang dijalankan service) |
| `ngodingmcp tui [flags]` | Buka control panel (`--root`, `--addr`, `--token` sebagai preset) |
| `ngodingmcp -d` | Pasang/perbarui systemd user service lalu start (alias `--daemon`) |
| `ngodingmcp status` / `logs` / `restart` / `stop` | Kelola service, lihat [service.md](service.md) |
| `ngodingmcp version` | Cetak versi, commit, dan waktu build (juga `-v`, `--version`) |
| `ngodingmcp help` | Tampilkan bantuan (juga `-h`, `--help`) |

Flag lama tetap kompatibel: `ngodingmcp --addr ... --root ...` masih menjalankan
server. Subcommand yang tidak dikenal ditolak dengan pesan error.

Contoh:

```sh
./ngodingmcp serve \
  --addr 127.0.0.1:8080 \
  --root /path/ke/workspace \
  --token rahasia-panjang
```

- Kalau `--token` kosong, server membuat token acak dan mencetaknya ke stderr.
- `--root` dibuat otomatis kalau belum ada.

## Precedence

`serve` membaca config TUI lebih dulu, lalu environment menimpa config, dan flag
menimpa keduanya: **flag > environment > config.json**.

Environment yang bernilai invalid (durasi/integer/boolean tak bisa di-parse,
atau nilai tidak positif) **menggagalkan startup dengan pesan yang menyebut
nama variabelnya** — bukan diam-diam memakai default. Contoh: `NGODINGMCP_OAUTH=yes`
menolak jalan dengan error `NGODINGMCP_OAUTH: invalid boolean "yes" ...`, bukan
mematikan OAuth diam-diam.

## Flag dan environment

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
| `--public-url` | `NGODINGMCP_PUBLIC_URL` | kosong (issuer diturunkan dari header `Host`) |
| `--trusted-proxy` | `NGODINGMCP_TRUSTED_PROXIES` | `loopback` (peer yang `X-Forwarded-Proto`-nya dipercaya) |
| `--oauth` | `NGODINGMCP_OAUTH` | `true` (OAuth aktif; `--oauth=false` mematikan) |
| `--disable-tools` | `NGODINGMCP_DISABLE_TOOLS` | kosong (semua modul tool inti aktif) |

Penjelasan run/allowlist: [run-and-processes.md](run-and-processes.md).
Penjelasan OAuth: [oauth.md](oauth.md).
Penjelasan download: [tools.md](tools.md#download_file).
Modul yang bisa dimatikan: [tools.md](tools.md#profil-mematikan-modul-tool).

## `config.json`

Lokasi: `~/.config/ngodingmcp/config.json`.

- Menyimpan setting TUI (root, address, token, toggle) dan daftar workspace dari
  Web UI (field `workspaces`). Workspace terakhir yang dibuka di Web UI jadi root
  default di start berikutnya.
- Field `publicUrl`: URL publik absolut untuk issuer OAuth (misal
  `https://mcp.example.com`). Field `oauth`: `false` mematikan embedded
  authorization server (mode static token only). Field `trustedProxies`: daftar
  peer yang `X-Forwarded-Proto`-nya dipercaya saat derivasi issuer (array string;
  keyword `loopback`/`private`/`none`, atau CIDR/IP). Ketiganya opsional; `oauth`
  yang tidak diisi dianggap `true`, `trustedProxies` yang kosong dianggap `loopback`.
  Semua setting persistent TUI ini (addr, token, run/sensitive/download) juga bisa
  di-edit dari Web UI — tombol **⚙ Settings** di topbar — dengan isi yang sama: kolom
  `addr` (`host:port`), `token` (kosong = generate acak saat start), checkbox
  `runEnabled`, `allowSensitive` (berlaku langsung, tanpa restart), `allowDownload`,
  `publicUrl`, `trustedProxies`, `oauth`.
- Validasi server menolak `addr` tidak valid (`host:port`, port 1–65535) dan token
  yang memuat karakter berbahaya (`;`, `,`, `"`, `\`) atau karakter kontrol, atau
  lebih pendek dari 32 karakter (token kustom; kosong tetap berarti generate acak).
  Perubahan tersimpan ke `config.json` dan berlaku setelah restart — kecuali
  `allowSensitive` yang langsung berlaku; environment dan flag tetap menimpa file saat start. Pola non-interaktif tetap
  tersedia: `ngodingmcp serve --public-url ... --trusted-proxy ... --oauth=false`,
  atau file env service.
- Field `disableTools`: array nama modul tool inti yang tidak didaftarkan
  (`inspect`, `git_write`, `git_remote`), misalnya `["git_remote"]`. Nama yang
  tidak dikenal membuat start gagal dengan daftar nama yang valid. Belum ada di
  Web UI Settings; isi lewat file ini, env, atau flag.
- Signing key, client OAuth terdaftar, dan hash refresh token disimpan di
  `~/.config/ngodingmcp/oauth.json` (bukan di `config.json`). Lihat [oauth.md](oauth.md).
- Dipakai lagi pada run berikutnya, termasuk `ngodingmcp serve` tanpa flag.
- Untuk run policy, config hanya menyimpan flag enable (`{"run": {"enabled": true}}`);
  allowlist tidak bisa diubah dari sini.
- Berada **di luar workspace** agar agent tidak bisa mengubahnya.
- Karena menyimpan token dalam plaintext, file-nya ditulis dengan permission
  `0600` dan foldernya `0700`; config lama yang lebih longgar ikut diperketat saat
  disimpan.

## File env service

`~/.config/ngodingmcp/env` dibaca oleh systemd user service (format `KEY=value`,
memakai env var di tabel atas). Lihat [service.md](service.md).
