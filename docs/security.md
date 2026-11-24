# Keamanan

## Jaringan dan auth

- Auth token untuk MCP dan REST (`Authorization: Bearer` atau `X-API-Key`).
  `/mcp` **hanya** menerima header kredensial itu; cookie browser tidak diterima,
  jadi cookie yang bocor tidak bisa dipakai replay ke `/mcp`.
- Login browser memakai cookie sesi **opaque server-side** (ID acak 32 byte),
  bukan token: HttpOnly, `SameSite=Lax`, `Secure` saat TLS/https, sliding
  TTL 7 hari. `Lax` (bukan `Strict`) agar cookie ikut pada navigasi top-level
  lintas-situs menuju halaman consent OAuth; POST lintas-situs tetap tidak
  membawa cookie, dan consent POST dilindungi CSRF token. Logout mencabut sesi
  di server. Restart server = semua sesi Web UI hilang. Login dibatasi 5
  percobaan gagal per menit per IP.
- Semua respons HTTP membawa header hardening: `Content-Security-Policy`
  (`default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self';
  frame-ancestors 'none'; base-uri 'self'; form-action 'self'`),
  `X-Frame-Options: DENY`, `X-Content-Type-Options: nosniff`,
  `Referrer-Policy: no-referrer`, `Cross-Origin-Opener-Policy: same-origin`.
  Halaman consent menambahkan origin `redirect_uri` klien yang sudah tervalidasi
  ke `form-action`, karena Chrome juga memeriksa directive itu pada redirect
  setelah submit form.
  Browser menolak request state-changing dari origin lain
  (`http.CrossOriginProtection`). Endpoint REST yang menerima JSON wajib
  `Content-Type: application/json` (yang lain 415).
- Token kustom baru yang di-set lewat Web UI minimal 32 karakter. Token lama
  yang sudah tersimpan boleh tetap dipakai selama tidak diubah (grandfather);
  ganti ke token ≥32 karakter disarankan.
- `/mcp` juga mendukung OAuth 2.1 sesuai MCP spec: resource server + authorization
  server embedded. Access token ES256 terikat audience ke server ini (RFC 8707),
  PKCE S256 wajib untuk code exchange, refresh token rotasi setiap dipakai.
  Rotasi dan deteksi replay berada dalam satu critical section; penanda token
  yang sudah dipakai bertahan selama penerusnya masih valid, jadi replay
  paralel maupun replay terlambat mencabut seluruh keluarga token. Refresh token
  disimpan di `oauth.json` hanya sebagai hash SHA-256 (file `0600`), jadi restart
  server tidak memaksa consent ulang dan isi file tidak bisa dipakai untuk
  refresh. Detail: [oauth.md](oauth.md).
- OAuth tetap menggunakan token statis sebagai jalur autentikasi tambahan.
- Token dikirim sebagai header biasa. Jalankan di `localhost`, atau taruh di
  belakang reverse proxy TLS kalau diakses dari jaringan lain (lihat `trusted-proxy`
  di [oauth.md](oauth.md)).
- Token tersimpan plaintext di `config.json` dan file env service, keduanya dengan
  mode `0600`.
- Proses yang di-spawn tool (`run_command`, background process) menerima environment
  yang sudah disaring: variabel dengan nama mengandung `TOKEN`, `SECRET`, `PASSWORD`,
  `PASSWD`, `PASSPHRASE`, `API_KEY`, `ACCESS_KEY`, `PRIVATE_KEY`, `CREDENTIAL`,
  `AUTH_TOKEN`, dan sejenisnya dihapus (mis. `NGODINGMCP_TOKEN`, `GITHUB_TOKEN`,
  `AWS_*`). `PATH`, `HOME`, `SSH_AUTH_SOCK` tetap ada untuk toolchain.

## Sandbox workspace

- Root workspace adalah batas keamanan. Semua path relatif ke root; `..` dan
  symlink yang keluar root ditolak saat diakses. Jangan menaruh symlink ke luar
  root kalau tidak yakin.
- Write atomik (temp file lalu `rename`), batas ukuran baca, dan deteksi file biner.
  FIFO, device, dan socket ditolak saat resolusi path: `open` pada FIFO bisa
  menunggu penulis tanpa batas dan `rename` bisa menimpa pipe tanpa sadar.
  Symlink ke file khusus ikut ditolak.
- `move_path` menolak menimpa tujuan yang sudah ada (file, direktori, atau symlink);
  hapus tujuan dulu atau pilih nama lain.
- **Error tanpa path host**: pesan error tool tidak pernah memuat path absolut
  workspace di host; path diganti menjadi path relatif terhadap root.
- `write_file` bersifat destruktif (menimpa file). Gunakan `replace_text` untuk
  edit kecil.

## File sensitif

Diblokir secara default:

- `.env` dan `.env.*` (kecuali template seperti `.env.example`, `.env.sample`,
  `.env.template`, `.env.dist`);
- `.envrc` dan varian `.env.*` lainnya;
- folder `.git`, `.ssh`, `.aws`, `.gnupg`, `.kube`, `.docker`;
- file `.npmrc`, `.pypirc`, `.netrc`, `.git-credentials`, `.htpasswd`,
  `id_rsa`/`id_dsa`/`id_ecdsa`/`id_ed25519`, `.pgpass`, `.my.cnf`, `.dockercfg`,
  `.terraformrc`, `credentials`, `credentials.json`, `kubeconfig`;
- ekstensi `.pem`, `.key`, `.p12`, `.pfx`, `.jks`, `.keystore`, `.tfvars`,
  `.tfstate`, `.p8`, `.gpg`, `.kdbx`;
- symlink di dalam root yang menunjuk ke file/folder sensitif ikut diblokir
  (nama netral seperti `link.txt -> .env` tidak bisa dipakai membaca isinya).

File ini tidak bisa dibaca/ditulis/diedit, disembunyikan dari list, search, stats,
dan count, dan delete rekursif ditolak kalau foldernya berisi file tersebut.
Matikan dengan `--allow-sensitive` (atau `NGODINGMCP_ALLOW_SENSITIVE=true`, field
`allowSensitive` di `config.json` lewat toggle TUI/Web UI, atau setelan per workspace
di Web UI) hanya kalau perlu. Field `config.json` ikut aturan urutan flag > env >
config, jadi tetap aktif setelah restart service.

Catatan: pencarian & statistik melewati direktori dependensi/cache build
(`node_modules`, `.next`, `.venv`, `__pycache__`, dll.) terlepas dari pengaturan
ini — itu bukan batas keamanan. `delete_path` tetap memindai seluruh subtree
incl. direktori tersebut supaya penghapusan tidak sengaja terdeteksi.

## Git

- Tool git **bisa menulis**: `git_init`, `git_add`, `git_unstage`, `git_commit`,
  `git_create_branch`, `git_switch_branch`, dan tool remote `git_remote`,
  `git_fetch`, `git_pull`, `git_push`, `git_merge`, `git_github_create_repo`.
- Push tidak pernah force, tidak menghapus branch remote, dan tidak push tag.
  Pull hanya fast-forward. Merge butuh tree bersih dan konflik di-abort otomatis.
- Tidak ada rebase, `reset --hard`, clean, checkout file, hapus branch, amend,
  atau force.
- Hook repo (`core.hooksPath`, husky) dan GPG signing tidak pernah dijalankan;
  fsmonitor dan pager dimatikan. Tool baca juga mematikan external diff dan
  textconv, jadi tidak ada program dari config repo yang ikut jalan. **Semua
  tool git** (baca dan tulis) menolak jalan kalau `.git/config` sendiri
  mengatur credential helper, ssh command, askpass, `insteadOf`,
  upload/receive-pack, filter, merge driver, include, `http.*`
  (proxy/`sslVerify`/`sslCAInfo`/`extraHeader`, termasuk varian per-URL),
  `remote.*.proxy`, atau `extensions.worktreeConfig` — bukan hanya tool
  remote dan merge. `config.worktree` ikut ditolak karena guard `--local` tidak
  membacanya.
- Subprocess git menerima environment **tanpa variabel `GIT_*`** yang mengarahkan
  git (fsmonitor, diff.external, config path, ssh variant, template dir, dst.).
  Variabel rahasia umum (mis. `GITHUB_TOKEN`) tetap diwariskan ke subprocess
  git — yang menghentikan eksekusi kode dari repo adalah penonaktifan hook/
  filter/fsmonitor/external diff di atas dan penolakan `CheckLocalConfig`,
  bukan penyaringan environment.
- `GIT_SSH_COMMAND` dan `GIT_SSH` dari environment sengaja dibuang: keduanya
  berisi string shell yang bisa menjalankan program. Default ssh aman
  (`BatchMode=yes`, `ConnectTimeout=20`) selalu dipakai kecuali
  `core.sshCommand` di config global menggantinya. Butuh opsi ssh kustom?
  Taruh di `~/.gitconfig` atau `~/.ssh/config`, bukan di environment.
- Remote hanya https dan ssh (`GIT_ALLOW_PROTOCOL=https:ssh`) dengan host publik:
  loopback, private, link-local (termasuk `169.254.169.254`), `localhost`, dan
  hostname yang resolve ke alamat non-publik ditolak saat add/set-url serta
  dicek ulang sebelum fetch/pull/push. Redirect HTTP dimatikan. Kredensial di URL
  disensor dari output dan URL remote tidak masuk log aktivitas. Tool remote tidak
  pernah meminta input (tanpa prompt, ssh BatchMode, timeout 2 menit).
- `git_add` melewati file sensitif dan file ignored; commit ditolak kalau ada file
  sensitif yang ter-stage.

Detail: [tools.md](tools.md#git).

## Eksekusi dan download (opt-in)

- `run_command` dan background process memberi akses eksekusi sebagai user yang
  menjalankan server. Aktifkan hanya dengan allowlist minimal dan di workspace
  tepercaya. Allowlist adalah batas kepercayaan: `go test` tetap bisa menjalankan
  kode dari workspace. Flag yang mem-bypass review dimatikan meski lolos
  allowlist (mis. `-exec`/`-toolexec`, `--no-index` untuk `git diff`,
  `-vettool` untuk `go vet`, `--output`). Argumen path absolut, `..`, dan
  symlink yang keluar root juga ditolak. Ini tetap bukan sandbox OS: `go test`
  bisa menjalankan kode workspace, jadi aktifkan hanya untuk workspace tepercaya.
- `start_process`/`stop_process` dapat menyalakan dan mematikan proses; seluruh
  proses dimatikan saat server shutdown.
- `download_file` hanya ke host https publik; alamat privat/loopback/metadata cloud
  ditolak saat dial.

Detail: [run-and-processes.md](run-and-processes.md), [tools.md](tools.md#download_file).

## Pindah workspace

Pindah workspace hanya bisa dari Web UI (bukan tool MCP), jadi AI tidak bisa
memindahkan root sendiri. Lihat [web-ui.md](web-ui.md#workspace-selector).
