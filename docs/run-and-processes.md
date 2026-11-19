# Eksekusi command dan background process (opt-in)

`run_command` dan 6 tool proses hanya terdaftar kalau diaktifkan, lewat toggle
**Enable run_command & processes** di TUI, `NGODINGMCP_ALLOW_RUN=true`, atau:

```sh
./ngodingmcp serve --allow-run
```

Tanpa `--allow-run` ada 32 tool. Dengan `--allow-run` jadi 39: 32 tool inti +
`run_command` + 6 tool proses (40 kalau `download_file` juga aktif).

## Allowlist

Saat aktif, policy memakai daftar **predefined di kode** (`internal/runner/policy.go`):

| Prefix (argumen tambahan bebas) | Exact (harus sama persis) |
|---|---|
| `go build` | `bun install --frozen-lockfile` |
| `go test` | `bun test` |
| `go vet` | `bun run build` |
| `gofmt -l` | `bun run dev` |
| `staticcheck` | `ngodingmcp restart` |
| `git status` | |
| `git diff` | |
| `git log` | |

`staticcheck` opsional: entri ini cuma berguna kalau programnya kebetulan ada
di `PATH` server. Tidak perlu di-install dan tidak perlu mengubah unit systemd,
karena CI dan `make check` sudah menjalankannya lewat `go run` (lihat
`development.md`). Flag `-debug.*`-nya ditolak karena bisa menulis file ke path
mana saja.

`ngodingmcp restart` tidak dijalankan sebagai program. Hasil tool dikirim dulu,
lalu sekitar satu detik kemudian server menjalankan
`systemctl --user --no-block restart ngodingmcp.service`. Ini hanya jalan kalau
server berjalan sebagai service (`ngodingmcp -d`); server yang dijalankan manual
di terminal menolak. `start_process` juga menolak command ini.

Override hanya kalau perlu (advanced). Kalau salah satu override diisi, daftar
predefined tidak dipakai:

```sh
./ngodingmcp serve --allow-run --run-allow "go build,go test,make"
./ngodingmcp serve --allow-run --run-allow-exact "bun install --frozen-lockfile"
```

Config hanya menyimpan flag enable; allowlist tidak bisa diubah dari config:

```json
{
  "run": { "enabled": true }
}
```

Precedence: **flag > environment > config**. Workspace tidak pernah dipakai
sebagai sumber policy.

> Jangan mendaftarkan command yang sama di prefix dan exact sekaligus: entri
> prefix akan membuka kembali argumen bebas. Untuk installer gunakan **hanya**
> `--run-allow-exact`.

> Allowlist adalah batas kepercayaan. Entri seperti `go test` tetap bisa
> menjalankan kode dari workspace; aktifkan hanya untuk workspace yang dipercaya.

## `run_command`

- **Prefix**: `go test ./...` cocok dengan entri `go test`.
- **Exact**: `bun install --frozen-lockfile` **hanya** cocok persis;
  `bun install paket-lain` ditolak.
- Tidak pernah lewat shell, jadi metacharacter tidak diinterpretasi.
- Flag yang menjembatani ke eksekusi lain atau baca file di luar workspace
  (`-exec`, `-toolexec`, `--no-index` untuk `git diff`, `-vettool` untuk
  `go vet`, `--output` untuk menulis di luar workspace) ditolak meski command-nya
  lolos allowlist.
- `cwd` selalu root workspace. Timeout default 60 detik, bisa diubah per request,
  dibatasi oleh `--run-timeout`.
- Output stdout+stderr digabung dan dipotong pada 256 KiB.
- Exit code non-zero tetap dikembalikan sebagai hasil (bukan error protokol),
  supaya AI bisa membaca kegagalan build/test.
- Saat timeout, seluruh process group dibunuh (termasuk child process).

## Background process

`run_command` bersifat request/response: proses harus selesai dalam timeout.
Untuk dev server atau watcher yang harus tetap hidup, pakai tool proses yang ikut
aktif bersama `--allow-run`:

| Tool | Fungsi |
|---|---|
| `start_process` | jalankan command allowlisted di background; bisa menunggu log/port siap |
| `list_processes` | status semua proses (starting/running/stopping/exited, exit code, runtime) |
| `process_logs` | baca stdout+stderr, tail atau incremental lewat cursor |
| `stop_process` | SIGTERM, lalu SIGKILL setelah grace period (bisa non-blocking) |
| `restart_process` | stop lalu start ulang dengan argv/cwd/env yang sama |
| `stop_all_processes` | hentikan semua proses sekaligus |

Contoh untuk `bun run dev` (sudah ada di predefined exact policy), dengan
readiness check supaya `start_process` baru kembali ketika server benar-benar siap:

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

> `bun run dev` hanya masuk akal lewat `start_process`. Kalau dipanggil lewat
> `run_command`, ia akan tetap berjalan sampai timeout lalu dibunuh.

### Batasan desain

- Command divalidasi oleh allowlist yang **sama** dengan `run_command`; tidak ada
  shell, dan `cwd` tidak bisa keluar dari workspace.
- Maksimal 8 proses berjalan bersamaan dan 64 entri terlacak; entri yang sudah
  exit dihapus otomatis saat penuh.
- Log per proses disimpan di ring buffer 128 KiB dengan cursor per baris.
- Nama proses: huruf, angka, `_`, `-` (maks 32 karakter); kosong = `proc-N`.
  Nama otomatis tidak akan bentrok dengan nama manual.
- `stop_process` mengirim sinyal ke seluruh process group/tree, jadi child process
  ikut berhenti (Job Object di Windows).
- Saat server di-shutdown, semua proses yang dikelola ikut dihentikan (grace 3
  detik, lalu force kill).
- Proses **tidak** bertahan setelah server mati; ini disengaja agar tidak ada
  orphan process.
- Saat pindah workspace di Web UI, proses milik workspace lama di-stop (setelah
  konfirmasi), dan `run_command` serta tool proses ikut pindah ke root baru. Lihat
  [web-ui.md](web-ui.md).

### Di TUI dan Web UI

- TUI menampilkan daftar proses; `Ctrl+X` menghentikan semuanya.
- Di Web UI, panel Processes punya tombol restart dan stop per proses. Klik
  prosesnya untuk membuka log (refresh tiap detik lewat cursor `nextSeq`, stderr
  berwarna merah, opsi follow) lengkap dengan tombol restart/stop.
- Endpoint REST Web UI (perlu auth): `GET /api/processes/{name}/logs?tail=200`
  atau `?after=<nextSeq>`, `POST /api/processes/{name}/stop`,
  `POST /api/processes/{name}/restart`.
