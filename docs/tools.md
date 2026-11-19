# Daftar tool

Semua path relatif ke root workspace. Garis miring di depan diabaikan, dan `..`
tidak bisa keluar dari root.

## Jumlah tool

| Mode | Jumlah | Isi |
|---|---|---|
| Default | 38 | tool inti (file, edit, search, inspect, git) |
| `--allow-run` | 45 | 38 inti + `run_command` + 6 tool proses |
| `--allow-download` | 39 | 38 inti + `download_file` |
| `--allow-run --allow-download` | 46 | semuanya |

Daftar tool aktif bisa dicek di TUI (baris `tools: N active`). Kontrak semua 46
tool (nama, deskripsi, anotasi, schema) dikunci di
`internal/mcpserver/testdata/tools.golden.json`, lihat [development.md](development.md).

### Profil: mematikan modul tool

Daftar tool dikirim ke model di awal setiap sesi, jadi tool yang tidak dipakai
tetap makan token. Tiga modul tool inti bisa dimatikan dengan `--disable-tools`,
`NGODINGMCP_DISABLE_TOOLS`, atau `disableTools` di `config.json` (dipisah koma):

| Modul | Tool |
|---|---|
| `inspect` | `workspace_stats`, `validate_syntax`, `count_tokens` |
| `git_write` | `git_init`, `git_add`, `git_unstage`, `git_commit`, `git_create_branch`, `git_switch_branch` |
| `git_remote` | `git_remote`, `git_fetch`, `git_pull`, `git_push`, `git_merge`, `git_github_*` |

Tool file, edit, search, dan git read-only (`git_status`, `git_diff`, `git_log`)
selalu aktif. Contoh profil paling kecil:
`--disable-tools inspect,git_write,git_remote` (20 tool).

### Resource `AGENTS.md`

Server menyediakan resource MCP `ngodingmcp://agents` (MIME `text/markdown`)
yang membaca `AGENTS.md` di root workspace. Resource tidak masuk `tools/list`,
jadi tidak menambah biaya tetap; client membacanya saat butuh. Isinya sebaiknya
struktur folder, konvensi, dan perintah test proyek. Instructions handshake
menyarankan client membacanya lebih dulu.

### Isi hasil tool

Teks (`content`) adalah hasil utama setiap tool. Client tidak seragam soal apa
yang diteruskan ke model, jadi isi yang sama tidak dikirim dua kali:

- Tool yang hasilnya isi file, listing, diff, atau log (`read_file`, `outline`,
  `read_symbol`, `read_files`, `list_files`, `edit_file`, `search_text`,
  `workspace_stats`, `process_logs`, `git_status`, `git_diff`, `git_log`,
  `git_github_runs`, `git_github_run_view`, `run_command`) hanya mengirim teks,
  tanpa `structuredContent` dan tanpa `outputSchema`.
- Tool konfirmasi (tulis, pindah, hapus, edit baris, git write/remote, kontrol
  proses, `download_file`) serta `validate_syntax` dan `count_tokens` tetap
  mengirim `structuredContent`, tapi tanpa `outputSchema`.
- `file_info` dan `list_processes` tetap punya `outputSchema`.
- Exit code `run_command` ada di teks dan di `_meta`
  (`ngodingmcp/exitCode`, `ngodingmcp/timedOut`).

## Tool inti (selalu aktif)

| Domain | Tool |
|---|---|
| File | `read_file`, `outline`, `read_symbol`, `read_files`, `write_file`, `list_files`, `file_info`, `mkdir_path`, `move_path`, `delete_path` |
| Edit | `replace_text`, `edit_file`, `insert_text`, `delete_lines`, `replace_lines` |
| Search | `search_text`, `search_and_replace` |
| Inspect | `workspace_stats`, `validate_syntax`, `count_tokens` |
| Git read | `git_status`, `git_diff`, `git_log` |
| Git write | `git_init`, `git_add`, `git_unstage`, `git_commit`, `git_create_branch`, `git_switch_branch` |
| Git remote | `git_remote`, `git_fetch`, `git_pull`, `git_push`, `git_merge`, `git_github_create_repo`, `git_github_runs`, `git_github_run_view`, `git_github_run_rerun` |

Perilaku umum:

- **Pencarian melewati direktori berat**: `search_text`, `search_and_replace`,
  `workspace_stats`, dan `count_tokens` mengabaikan direktori dependensi dan
  cache build (`node_modules`, `bower_components`, `.next`, `.nuxt`,
  `.svelte-kit`, `.turbo`, `.cache`, `.venv`, `__pycache__`). Menunjuk langsung
  ke direktori itu (`path: "node_modules"`) tetap bisa. Pengabaian ini diam-diam
  dan bukan batas keamanan. Hasil `search_text` dikelompokkan per file supaya
  path tidak diulang, default 30 kecocokan (`max_results`, maks 1000), dipotong
  per baris (~300 byte di sekitar kecocokan), dan teks output dibatasi 64 KiB.
- **Walker bulk berjalan paralel**: `search_text`, `search_and_replace`,
  `workspace_stats`, dan `count_tokens` memproses file dengan worker pool;
  urutan output tetap deterministik.
- **`list_files` rekursif default dibatasi 300 entri** dan melewati direktori
  berat. `depth` membatasi kedalaman (implisit rekursif), `max_entries` menaikkan
  batas sampai 2000. Output yang terpotong ditandai `truncated: true` dan
  menyertakan notice "listing truncated at N entries".
- **Write atomik**: setiap tulis memakai temp file lalu `rename`, jadi tidak ada
  file setengah jadi.
- **Batas ukuran baca** (`--max-file-bytes`, default 2 MiB) dan deteksi file biner.
- `write_file` bersifat destruktif (menimpa seluruh file) dan hanya menerima teks.
  Untuk edit kecil pakai `replace_text`; untuk file biner pakai `download_file`
  atau upload lewat Web UI. Jika file besar yang sudah ada ditimpa dengan
  perubahan kecil, hasilnya menambahkan saran memakai `replace_text`/`edit_file`
  (deteksi berbasis awalan/akhiran yang sama, bukan diff penuh).

### Membaca sebagian file

Membaca seluruh file untuk mengambil satu fungsi membayar semua baris yang tidak
dipakai. Empat tool ini memotong biaya tersebut:

- `read_file` mengembalikan `hash` 16 hex di header. Kirim kembali hash itu di
  `if_changed_since`; kalau isi file tidak berubah, hasilnya cuma
  `unchanged <path> (hash ...)` tanpa isi. `outline`, `read_symbol`, dan
  `read_files` menerima hash yang sama dengan cara yang sama.
- `outline` mendaftar simbol top-level (`func`, `method`, `type`, `const`,
  `var`) beserta rentang baris. Go memakai `go/ast`; JS/TS memakai fallback
  regex untuk `function`, `class`, `interface`/`type`, dan arrow function.
  Tipe file lain membalas notice dan tetap bisa dibaca lewat `read_file`.
- `read_symbol` membaca satu simbol dari hasil `outline` (misal `Server.Run`)
  tanpa baris lain. Nama method memakai bentuk `Tipe.Nama`.
- `read_files` membaca beberapa file atau rentang dalam satu panggilan. Setiap
  item punya `if_changed_since` sendiri; item yang tidak berubah diringkas jadi
  `== <path> (unchanged, hash ...)`. Total isi dibatasi `max_bytes` (default
  64 KiB, maks 1 MiB); file yang tidak tertampung dilaporkan di akhir, dan file
  terakhir bisa dipotong di batas baris.

### `edit_file`

Beberapa edit exact-text dalam satu langkah atomik: semua berhasil atau tidak ada
yang ditulis. Hasilnya memuat unified diff. Pakai `dry_run: true` untuk preview
saja tanpa menulis.

### `validate_syntax`

- Go dan JSON: parse penuh.
- JS/TS/CSS: cek delimiter (kurung seimbang; string, template literal, regex, dan
  komentar tertutup).
- Hasil memuat field `check`: `parser`, `delimiters`, atau `none`.

## Git

### Read

- `git_status`
- `git_diff`: default ringkasan `--stat`; `full: true` atau `path` mengembalikan
  patch (dibatasi 256 KiB, notice `truncated`). Opsi lain: `staged`, `stat`, `ref`.
- `git_log`: default 10 commit satu baris (`limit`, maks 200); `body: true`
  menambahkan isi body commit.

Kalau workspace bukan repo git, tool membalas pesan penjelasan (arahkan ke
`git_init`), bukan error. External diff, textconv, pager, dan fsmonitor dimatikan,
jadi membaca tidak pernah menjalankan program dari config repo. File sensitif
dikecualikan dari diff.

### Write

- `git_init`: branch awal `main`.
- `git_add`: `paths` atau `all: true`. File yang di-ignore tidak pernah di-stage
  (tidak ada opsi force). File sensitif seperti `.env` atau private key dilewati
  dan dilaporkan.
- `git_unstage`: file di workspace tidak berubah.
- `git_commit`: `message`, opsional `author_name` + `author_email`.
- `git_create_branch`: opsi `start` dan `switch`.
- `git_switch_branch`.
Batas aman:

- **Tidak ada** rebase, `reset --hard`, clean, checkout file, hapus branch,
  amend, atau force.
- Hook repo (`core.hooksPath`, husky) dan GPG signing tidak pernah dijalankan.
- Commit ditolak kalau ada file sensitif yang ter-stage.
- Hanya bekerja kalau root workspace = root repo. Workspace di dalam repo lain
  ditolak, kecuali `git_init`.

### Remote

- `git_remote`: `action` = `list` (default), `add`, `set_url`, `remove`, plus
  `name` dan `url`. `remove` cuma menghapus remote dan remote-tracking branch-nya,
  branch lokal dan commit tidak disentuh.
- `git_fetch`: opsi `remote`, `prune`. Hanya mengisi `origin/*`; branch lokal dan
  file tidak berubah.
- `git_pull`: opsi `remote`, `branch`. **Fast-forward saja.** Kalau lokal dan
  remote sudah bercabang, tidak ada yang diubah dan pesannya menyarankan
  `git_merge ref=origin/main` lalu `git_push`. Tidak ada rebase. Ditolak kalau
  perubahan yang belum di-commit akan tertimpa.
- `git_push`: opsi `remote`, `branch`, `remote_branch`, `set_upstream`.
  **Tidak pernah force** (tanpa `--force`, `--force-with-lease`, atau refspec `+`),
  tidak menghapus branch remote, dan tidak push tag. Push yang ditolak karena
  non-fast-forward dilaporkan, tidak dicoba ulang.
- `git_merge`: `ref` (branch, `origin/main`, atau commit), opsi `ff_only`,
  `message`. Butuh working tree bersih (file ter-track). Kalau konflik, merge
  langsung di-abort otomatis, branch tidak berubah, dan file yang konflik
  dilaporkan.
- `git_github_create_repo`: `name`, opsi `owner` (organisasi), `visibility`
  (`private` default, `public`, `internal`), `description`, `remote` (default
  `origin`), `skip_remote`. Membuat repo kosong lewat GitHub CLI (`gh repo create`)
  lalu menambahkannya sebagai remote `https://github.com/<owner>/<name>.git`.
  Tidak ada yang di-push; lanjutkan dengan `git_push set_upstream=true`. Tidak bisa
  clone, hapus, atau ubah repo yang sudah ada.
- `git_github_runs`: daftar workflow run GitHub Actions terbaru lewat `gh run list`
  (opsi `workflow`, `branch`, `status`, `limit` maksimal 50).
- `git_github_run_view`: detail job, step, status/conclusion, dan opsi
  `failed_logs: true` untuk log step yang gagal lewat `gh run view` (`run_id`
  opsional, default run terbaru).
- `git_github_run_rerun`: jalankan ulang workflow run lewat `gh run rerun` (`run_id`
  wajib, opsi `failed_only: true` untuk rerun job yang gagal saja).

Pengaman remote:

- URL remote baru hanya `https://host/...` (tanpa user/token di URL) atau ssh
  (`git@host:owner/repo.git`, `ssh://git@host/...`). `file://`, path lokal,
  `git://`, `http://`, dan `ext::` ditolak. Saat fetch/pull/push, `GIT_ALLOW_PROTOCOL`
  juga dikunci ke `https:ssh`, jadi remote lama yang menunjuk ke path lokal ikut
  ditolak.
- Kredensial di URL (`user:token@`) disensor jadi `***@` di semua output dan
  error. URL remote tidak pernah masuk log aktivitas.
- Setelan program dari `.git/config` repo tidak dipercaya: kalau repo sendiri
  mengatur `credential.helper`, `core.sshCommand`, `core.askPass`,
  `url.*.insteadOf`, `remote.*.uploadpack/receivepack`, `filter.*`,
  `merge.*.driver`, atau `include`, tool remote dan merge menolak jalan. Taruh
  setelan seperti itu di `~/.gitconfig`.
- Tidak pernah menunggu input: `GIT_TERMINAL_PROMPT=0`, askpass dimatikan, ssh
  `BatchMode=yes`, proses jalan tanpa terminal, dan ada timeout 2 menit yang
  mematikan git beserta ssh/credential helper-nya. Error auth disertai `hint:`
  cara memperbaikinya.

### Auth untuk remote

ngodingmcp memakai binary `git` dan `gh` sistem, jadi kredensialmu sendiri
langsung terpakai:

- **SSH** (`git@github.com:...`): key di `~/.ssh` tanpa passphrase, atau key yang
  sudah di-`ssh-add` ke ssh-agent. Host key harus sudah dikenal: jalankan sekali
  `ssh -T git@github.com` di terminal.
- **HTTPS**: credential helper di config global. Untuk GitHub paling mudah
  `gh auth login` lalu `gh auth setup-git`. Jangan taruh token di URL remote.
- **`git_github_create_repo` & GitHub Actions tools**: butuh `gh` terpasang dan
  sudah `gh auth login`.

Mode service (`ngodingmcp -d`) tidak punya terminal, dan mungkin tidak punya
ssh-agent atau keyring desktop. Kalau push/fetch/create gagal di sana:

1. SSH lewat agent: tambahkan `SSH_AUTH_SOCK=<hasil echo $SSH_AUTH_SOCK>` ke
   `~/.config/ngodingmcp/env`. Socket agent bisa berubah setelah login ulang; key
   tanpa passphrase atau HTTPS + gh lebih stabil.
2. HTTPS/gh: kalau token gh disimpan di keyring dan service tidak bisa membacanya,
   login ulang dengan `gh auth login --insecure-storage`, atau tambahkan
   `GH_TOKEN=<token>` ke file env (izin file 0600). Helper `gh auth git-credential`
   juga membaca `GH_TOKEN`.
3. Jalankan `ngodingmcp restart`.

## Tool opt-in

- `run_command` dan 6 tool proses (`start_process`, `list_processes`,
  `process_logs`, `stop_process`, `restart_process`, `stop_all_processes`):
  aktif dengan `--allow-run`. Lihat [run-and-processes.md](run-and-processes.md).
- `download_file`: aktif dengan `--allow-download` atau toggle
  **Enable download_file** di TUI. Detail di bawah.

Tanpa flag-nya, tool opt-in tidak terdaftar sama sekali.

### `download_file`

`write_file` hanya menerima teks. Untuk file biner (misalnya gambar WebP), aktifkan
`download_file`:

```sh
./ngodingmcp serve --allow-download
# opsional: batasi host dan ukuran
./ngodingmcp serve --allow-download --download-allow-host "*.example.com" --download-max-bytes 52428800
```

Argumen: `source_url` (wajib, https), `path` (wajib, relatif ke root),
`overwrite` (default `false`), `sha256` (opsional, 64 hex). Hasil: `path`,
`bytes`, `sha256`, `contentType` (hanya informasi dari server).

Pengaman:

- Hanya `https://`, tanpa `user:pass@`. Redirect maksimal 5, dan tiap hop dicek ulang.
- Alamat loopback, privat, link-local (termasuk metadata cloud `169.254.169.254`),
  CGNAT/Tailscale (`100.64.0.0/10`), dan range reserved ditolak **saat dial**, jadi
  DNS rebinding dan redirect tidak bisa melewatinya. Proxy dari environment diabaikan.
- Batas ukuran dihitung dari byte yang benar-benar dibaca, bukan `Content-Length`.
  Timeout total 2 menit.
- Path tujuan lewat sandbox dan guard file sensitif, ditulis atomik (temp file +
  rename). Tidak ada file setengah jadi kalau gagal.
- Log aktivitas hanya mencatat host dan path tujuan, tidak pernah query string atau token.

Allowlist host kosong secara default karena host URL bertanda tangan (signed URL)
dari MCP client tidak diketahui sebelumnya.
