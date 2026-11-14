# Keamanan

## Jaringan dan auth

- Auth token untuk MCP dan REST (`Authorization: Bearer` atau `X-API-Key`), login
  cookie untuk browser.
- `/mcp` juga mendukung OAuth 2.1 sesuai MCP spec: resource server + authorization
  server embedded. Access token ES256 terikat audience ke server ini (RFC 8707),
  PKCE S256 wajib untuk code exchange, refresh token rotasi setiap dipakai.
  Refresh token hanya di memori; restart server = consent ulang. Detail:
  [oauth.md](oauth.md).
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
Matikan dengan `--allow-sensitive` (atau `NGODINGMCP_ALLOW_SENSITIVE=true`, toggle
di TUI, atau setelan per workspace di Web UI) hanya kalau perlu.

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
  textconv, jadi tidak ada program dari config repo yang ikut jalan. Tool remote
  dan merge menolak jalan kalau `.git/config` sendiri mengatur credential helper,
  ssh command, askpass, `insteadOf`, upload/receive-pack, filter, merge driver,
  atau include.
- Remote hanya https dan ssh (`GIT_ALLOW_PROTOCOL=https:ssh`); kredensial di URL
  disensor dari output dan URL remote tidak masuk log aktivitas. Tool remote tidak
  pernah meminta input (tanpa prompt, ssh BatchMode, timeout 2 menit).
- `git_add` melewati file sensitif dan file ignored; commit ditolak kalau ada file
  sensitif yang ter-stage.

Detail: [tools.md](tools.md#git).

## Eksekusi dan download (opt-in)

- `run_command` dan background process memberi akses eksekusi sebagai user yang
  menjalankan server. Aktifkan hanya dengan allowlist minimal dan di workspace
  tepercaya. Allowlist adalah batas kepercayaan: `go test` tetap bisa menjalankan
  kode dari workspace.
- `start_process`/`stop_process` dapat menyalakan dan mematikan proses; seluruh
  proses dimatikan saat server shutdown.
- `download_file` hanya ke host https publik; alamat privat/loopback/metadata cloud
  ditolak saat dial.

Detail: [run-and-processes.md](run-and-processes.md), [tools.md](tools.md#download_file).

## Pindah workspace

Pindah workspace hanya bisa dari Web UI (bukan tool MCP), jadi AI tidak bisa
memindahkan root sendiri. Lihat [web-ui.md](web-ui.md#workspace-selector).
