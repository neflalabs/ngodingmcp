# Web UI

Buka alamat server (default `http://127.0.0.1:8080/`), masukkan token, lalu edit
seperti editor biasa. Browser memakai login cookie; MCP client dan REST API
memakai `Authorization: Bearer` atau `X-API-Key`.

UI-nya bawaan (embedded di binary, tanpa dependency frontend):

- file tree, editor, dan search;
- buat, rename, dan hapus file; save dengan `Ctrl+S`;
- light/dark mode;
- viewer log proses dengan tombol stop/restart (lihat
  [run-and-processes.md](run-and-processes.md));
- upload file;
- workspace selector;
- **⚙ Settings** — same settings as the TUI's persisted panel, saved to
  `config.json`: listen address, auth token, `run_command` & processes,
  sensitive files (applied live), `download_file`, plus the OAuth Public URL,
  trusted proxies and OAuth toggle. Applies after the server restarts, except
  the sensitive-files toggle. See [oauth.md](oauth.md) for the OAuth fields;
- sidebar yang bisa di-collapse (tombol ☰ atau `Ctrl+B`; statusnya diingat browser);
- versi build di status bar (tooltip berisi waktu build, lihat [versioning.md](versioning.md)).

## Upload

Tombol **↑ upload** di sidebar membuka dialog: drop file atau klik *choose files*,
isi folder tujuan (relatif ke root), centang *overwrite* kalau perlu.

Endpoint: `POST /api/upload?dir=<folder>&overwrite=true|false` (multipart, field
`file`, boleh banyak; maksimal 20 file per request), wajib login/token.

Aturannya sama dengan `download_file`:

- sandbox path dan guard file sensitif;
- batas ukuran per file (`--upload-max-bytes`, default 20 MiB);
- write atomik;
- nama file harus nama polos (tanpa `/`, `\`, `..`).

Status: file yang sudah ada dibalas `409` kecuali `overwrite=true`, terlalu besar
`413`, path sensitif atau di luar root `403`. Respons memuat hasil per file.

## Workspace selector

Tombol **workspace ▾** di topbar membuka daftar workspace tersimpan. Tambah
workspace dengan nama + folder absolut (harus sudah ada; `~/` boleh), lalu klik
**open** untuk pindah. Tombol *use current folder* mengisi form dengan root yang
sedang aktif.

Daftar ini disimpan di config yang sama dengan TUI
(`~/.config/ngodingmcp/config.json`, field `workspaces`), dan workspace terakhir
yang dibuka jadi root default di start berikutnya, termasuk setelan
per-workspace-nya. Kalau server dijalankan dari TUI, kolom root di TUI ikut
ter-update.

Setiap workspace boleh punya setelan sendiri; yang dikosongkan ikut default
global saat server dinyalakan:

- **Sensitive files**: `inherit` / `block` / `allow` (override `--allow-sensitive`).
  Memilih *allow* meminta konfirmasi.
- **Max file size**: batas baca/tulis per file (override `--max-file-bytes`).

Yang terjadi saat pindah workspace:

- Server menunggu tool call MCP dan request Web UI yang sedang jalan selesai
  (maksimal 15 detik; kalau lewat dibalas `409` "busy"), jadi satu operasi tidak
  pernah tercampur dua root.
- Background process milik workspace lama tidak dibiarkan jalan diam-diam: kalau
  ada, Web UI minta konfirmasi dulu, lalu semuanya di-stop dan log-nya dibuang.
- Perubahan editor yang belum di-save memunculkan konfirmasi.
- `run_command` dan process tools ikut pindah ke root baru. Sandbox path dan guard
  file sensitif berlaku untuk workspace yang aktif.
- MCP client tidak perlu reconnect: daftar tool tidak berubah, hanya root-nya.
- Pindah workspace sengaja hanya bisa dari Web UI. Tidak ada tool MCP untuk itu,
  jadi AI tidak bisa pindah folder sendiri.

Folder workspace yang aktif tidak bisa diganti lewat *edit* (pindah dulu ke
workspace lain); setelan lain langsung berlaku. Hapus dari daftar tidak menyentuh
foldernya.

## Endpoint REST

Semua endpoint `/api/...` wajib login/token (tanpa itu dibalas `401`). Halaman
`/login`, `/logout`, dan aset `/static/` terbuka; `/mcp` memakai auth yang sama
dengan `/api` (token statis atau OAuth — lihat [oauth.md](oauth.md)). Endpoint
discovery dan flow OAuth (`/.well-known/...`, `/oauth/...`) terbuka.

| Endpoint | Fungsi |
|---|---|
| `GET /api/info` | info server, termasuk `version` dan `built` |
| `GET /api/tree` | file tree |
| `GET /api/file`, `PUT /api/file`, `DELETE /api/file` | baca, simpan, hapus file |
| `POST /api/mkdir`, `POST /api/rename` | buat folder, rename/pindah |
| `GET /api/search`, `POST /api/search-replace` | search, search & replace |
| `GET /api/stats`, `GET /api/validate` | statistik workspace, cek sintaks |
| `POST /api/upload?dir=…&overwrite=…` | upload file (lihat di atas) |
| `GET /api/workspaces` | daftar workspace tersimpan |
| `POST /api/workspaces` | tambah workspace |
| `PUT /api/workspaces/{name}` | ubah nama/setelan |
| `DELETE /api/workspaces/{name}` | hapus dari daftar (folder tidak disentuh) |
| `POST /api/workspaces/{name}/activate` | pindah workspace |
| `GET /api/processes` | daftar proses |
| `GET /api/processes/{name}/logs?tail=200` atau `?after=<nextSeq>` | log proses |
| `POST /api/processes/{name}/stop` | stop proses |
| `POST /api/processes/{name}/restart` | restart proses |
| `POST /api/processes/stop-all` | stop semua proses |

Endpoint `/api/processes...` hanya ada kalau `--allow-run` aktif.

`activate` menerima body opsional `{"stopProcesses": true}`. Tanpa itu, proses
yang masih jalan membuat switch dibalas `409` dengan `runningProcesses`.
