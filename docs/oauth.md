# OAuth untuk /mcp

NgodingMCP bisa bertindak sebagai *resource server* sekaligus *authorization server* OAuth 2.1 untuk
endpoint MCP, sesuai MCP Authorization spec (2025-06-18 dan 2026-07-28). Client MCP yang spec-compliant
(Claude, Cursor, dll.) bisa discover, registrasi, dan login otomatis — tanpa token statis.
Token statis (`Authorization: Bearer` / `X-API-Key`) tetap diterima.

## Cara kerja

1. Client memanggil `POST /mcp` tanpa kredensial → server membalas `401 Unauthorized` dengan
   `WWW-Authenticate: Bearer realm="ngodingmcp", resource_metadata="<base>/.well-known/oauth-protected-resource", scope="mcp"`.
2. Client membaca **Protected Resource Metadata** (RFC 9728) di
   `GET /.well-known/oauth-protected-resource`.
3. Client membaca **Authorization Server Metadata** (RFC 8414) di
   `GET /.well-known/oauth-authorization-server`.
4. Client **mendaftarkan diri** lewat Dynamic Client Registration (RFC 7591) di `POST /oauth/register`,
   atau memakai `client_id` yang sudah terdaftar.
5. Client membuka `GET /oauth/authorize` dengan PKCE (`S256`) → browser menampilkan **halaman consent**
   NgodingMCP. Untuk login, masukkan token server (atau gunakan cookie sesi Web UI yang sudah ada).
6. Setelah approve, redirect berisi `code` dan `iss` (RFC 9207). Client menukarnya di `POST /oauth/token`
   dengan `code_verifier` (S256) dan wajib membawa `resource` — server menerima origin issuer
   (`https://mcp.example.com`) maupun URL endpoint MCP (`https://mcp.example.com/mcp`); bentuk lain
   ditolak.
7. Server menerbitkan **access token** ES256 (`typ: at+jwt`; `aud` = resource yang diminta client;
   scope `mcp`; berlaku 10 menit) dan **refresh token** opaque (berlaku 30 hari, rotasi setiap dipakai).

## Flag dan env

- `-public-url` (`NGODINGMCP_PUBLIC_URL`, `config.json` `publicUrl`, Web UI **⚙ Settings**): URL
  publik absolut server, misal `https://mcp.example.com`. Dipakai sebagai issuer dan `resource`.
  Tanpa flag ini, issuer diturunkan per-request (lihat di bawah). Direkomendasikan untuk deployment
  internet.
- `-trusted-proxy` (`NGODINGMCP_TRUSTED_PROXIES`, `config.json` `trustedProxies`,
  Web UI **⚙ Settings**): daftar peer yang header `X-Forwarded-Proto`-nya dipercaya saat derivasi
  issuer. Default `loopback`. Keyword: `loopback` (127.0.0.0/8 + ::1), `private` (loopback + RFC 1918
  + ULA), `none` (tidak ada). Selebihnya CIDR atau IP (misal `103.21.244.0/22`, atau range IP
  Cloudflare dari <https://www.cloudflare.com/ips/>).
- `-oauth` (`NGODINGMCP_OAUTH`, `config.json` `oauth`, Web UI **⚙ Settings**): default **aktif**.
  `--oauth=false` mematikan authorization server; `/mcp` hanya menerima token statis.

Derivasi issuer tanpa `-public-url`: host diambil dari header `Host` (dinormalisasi secara sintaks,
nilai aneh ditolak), scheme `https` hanya jika koneksi TLS ke proses, atau `http(s)` dari
`X-Forwarded-Proto` **bila peer koneksi terdaftar sebagai trusted proxy** (default: loopback).
Selebihnya `http`. Jadi proxy TLS di host yang sama langsung jalan; proxy remote (misal
Cloudflare) perlu `-trusted-proxy` atau, paling direkomendasikan, `-public-url`.

## Reverse proxy dan TLS

Spesifikasi mewajibkan HTTPS untuk endpoint authorization server. NgodingMCP tidak terminasi TLS
di dalam proses. Jalankan di `localhost`, atau taruh di belakang reverse proxy TLS (nginx/Caddy) dan
set `-public-url https://...` supaya issuer dan audience token stabil — atau set `-trusted-proxy`
bila Anda ingin mempercayai header proxy saja (issuer tetap diturunkan per request). Di belakang
Cloudflare: isi `-trusted-proxy` dengan range IP Cloudflare, atau pakai `-public-url`.

## File `~/.config/ngodingmcp/oauth.json`

Berisi signing key ES256 dan daftar client terdaftar (DCR). Mode direktori `0700`, file `0600`.
Refresh token **tidak** disimpan di disk — hanya di memori proses, jadi restart service memaksa
client melakukan authorize ulang. Access token selalu bisa diverifikasi lewat `GET /oauth/jwks.json`.

## Batasan

- CIMD (Client ID Metadata Documents) belum didukung; hanya DCR.
- Jumlah client terdaftar dibatasi 200 (`maxRegisteredClients`). Saat penuh, server
  **mengevakuasi client yang belum pernah dipakai** (client `last_used_at = 0`
  terlama) untuk memberi slot client baru. Client yang sudah pernah dipakai tidak
  pernah di-evict; kalau semua 200 client sudah pernah dipakai, pendaftaran ditolak
  dengan pesan penjelas. `last_used_at` diperbarui saat client berhasil menukar
  token dan disimpan di `oauth.json`.
- Tidak ada endpoint revoke (RFC 7009). Refresh token dirotasi setiap dipakai; token lama yang
  dipakai ulang mencabut seluruh keluarga token (RFC 6819 §5.2.2.3). Refresh token bisa juga dicabut
  dengan restart server.
- Hanya client publik (`token_endpoint_auth_method: none`). Tidak mendukung client secret.
- Audience token selalu resource milik server ini (origin issuer atau `…/mcp`); `resource` lain
  ditolak dengan `invalid_target`. Protected Resource Metadata melaporkan `resource` = `https://<base>/mcp`.
