# Daftar tool

Semua path relatif ke root workspace. Garis miring di depan diabaikan, dan `..`
tidak bisa keluar dari root.

## Jumlah tool

| Mode | Jumlah | Isi |
|---|---|---|
| Default | 32 | tool inti (file, edit, search, inspect, git) |
| `--allow-run` | 39 | 32 inti + `run_command` + 6 tool proses |
| `--allow-download` | 33 | 32 inti + `download_file` |
| `--allow-run --allow-download` | 40 | semuanya |

Daftar tool aktif bisa dicek di TUI (baris `tools: N active`). Kontrak semua 40
tool (nama, deskripsi, anotasi, schema) dikunci di
`internal/mcpserver/testdata/tools.golden.json`, lihat [development.md](development.md).

## Tool inti (selalu aktif)

| Domain | Tool |
|---|---|
| File | `read_file`, `write_file`, `list_files`, `file_info`, `mkdir_path`, `move_path`, `delete_path` |
| Edit | `replace_text`, `edit_file`, `insert_text`, `delete_lines`, `replace_lines` |
| Search | `search_text`, `search_and_replace` |
| Inspect | `workspace_stats`, `validate_syntax`, `count_tokens` |
| Git read | `git_status`, `git_diff`, `git_log` |
| Git write | `git_init`, `git_add`, `git_unstage`, `git_commit`, `git_create_branch`, `git_switch_branch` |
| Git remote | `git_remote`, `git_fetch`, `git_pull`, `git_push`, `git_merge`, `git_github_create_repo` |

Perilaku umum:

- **Pencarian melewati direktori berat**: `search_text`, `search_and_replace`,
  `workspace_stats`, dan `count_tokens` mengabaikan direktori dependensi dan
  cache build (`node_modules`, `bower_components`, `.next`, `.nuxt`,
  `.svelte-kit`, `.turbo`, `.cache`, `.venv`, `__pycache__`). Menunjuk langsung
  ke direktori itu (`path: "node_modules"`) tetap bisa. Pengabaian ini diam-diam
  dan bukan batas keamanan. Hasil `search_text` dipotong per baris (~300 byte di
  sekitar kecocokan) dan teks output dibatasi 64 KiB.
- **Write atomik**: setiap tulis memakai temp file lalu `rename`, jadi tidak ada
  file setengah jadi.
- **Batas ukuran baca** (`--max-file-bytes`, default 2 MiB) dan deteksi file biner.
- `write_file` bersifat destruktif (menimpa seluruh file) dan hanya menerima teks.
  Untuk edit kecil pakai `replace_text`; untuk file biner pakai `download_file`
  atau upload lewat Web UI.

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
- `git_diff`: opsi `path`, `staged`, `stat`, `ref`
- `git_log`: opsi `path`, `limit`

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
- **`git_github_create_repo`**: butuh `gh` terpasang dan sudah `gh auth login`.

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
