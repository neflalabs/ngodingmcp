# Jalan di background (systemd user service)

Supaya NgodingMCP tetap jalan tanpa terminal terbuka, jadikan dia systemd user
service. Tidak perlu `sudo`/root. Kontrolnya lewat Web UI.

```sh
go build -o ngodingmcp .     # atau: make build
./ngodingmcp -d              # alias: --daemon
```

## Yang dilakukan `-d`

`-d` melakukan ini lalu keluar:

1. Menulis `~/.config/systemd/user/ngodingmcp.service` dengan
   `ExecStart="<path absolut binary ini>" serve`,
   `EnvironmentFile=-%h/.config/ngodingmcp/env`, `Restart=on-failure`,
   `WantedBy=default.target`.
2. Kalau belum ada, membuat `~/.config/ngodingmcp/env` (mode 0600) berisi
   `NGODINGMCP_TOKEN`. Token-nya diambil dari token yang disimpan TUI, jadi tetap
   sama. Kalau belum ada token, dibuatkan baru.
   Env var di file ini divalidasi ketat saat startup: nilai yang gagal di-parse
   atau tidak positif menggagalkan boot dengan error yang menyebut nama
   variabelnya (bukan diam-diam memakai default).
3. Menjalankan `systemctl --user daemon-reload` dan
   `systemctl --user enable --now ngodingmcp`, menunggu port-nya menjawab, lalu
   mencetak alamat Web UI dan MCP.

Aman dijalankan berulang. Kalau binary dipindah atau di-build di folder lain,
jalankan `-d` lagi dari lokasi baru: path di unit file diperbarui dan service
di-restart.

`-d` menolak jalan kalau port-nya sedang dipakai TUI atau `serve` manual, dan
kalau binary-nya hasil `go run` (path-nya sementara).

## Mengelola service

| Perintah | Fungsi |
|---|---|
| `ngodingmcp status` | Nyala/tidak, alamat Web UI + MCP, workspace, binary, status linger, plus peringatan (misalnya unit menunjuk ke binary lain) |
| `ngodingmcp logs` | Log service, ikut baris baru (= `journalctl --user -u ngodingmcp -f`). `-n 200` untuk lebih banyak baris, `--no-follow` untuk cetak lalu keluar |
| `ngodingmcp restart` | Restart, misalnya setelah rebuild binary atau edit file env |
| `ngodingmcp stop` | Stop. Service tetap enabled, jadi nyala lagi saat login berikutnya |

Mematikan permanen: `systemctl --user disable --now ngodingmcp`.

## Jalan tanpa login (linger)

User service biasanya hanya hidup selama kamu login. Supaya tetap jalan setelah
logout dan ikut nyala saat boot, jalankan sekali:

```sh
loginctl enable-linger $USER
```

`-d` tidak menjalankan ini otomatis; `ngodingmcp status` menunjukkan linger on/off.

## Setelan service (file env)

Setelan service ada di `~/.config/ngodingmcp/env` (format `KEY=value`, sama
dengan env var CLI di [configuration.md](configuration.md)), misalnya
`NGODINGMCP_ADDR=0.0.0.0:8080`, `NGODINGMCP_ALLOW_RUN=true`, atau
`NGODINGMCP_PUBLIC_URL=https://mcp.example.com` untuk OAuth. Setelah diedit,
jalankan `ngodingmcp restart`.

Jangan isi `NGODINGMCP_ROOT` di sana: workspace yang dipilih di Web UI disimpan di
`config.json` dan dipakai lagi setelah restart (termasuk setelan
per-workspace-nya); `NGODINGMCP_ROOT` akan menimpanya di setiap restart.

## Bentrok port

Kalau service sedang jalan lalu kamu buka TUI biasa (`./ngodingmcp`) dan menekan
start di alamat yang sama, TUI menampilkan pesan "the background service is
already running on …" beserta alamat Web UI-nya. Pakai service itu, atau
`ngodingmcp stop` dulu. Kalau port-nya dipakai program lain, pesannya berbeda.
Sebaliknya, `-d` menolak jalan kalau port-nya sedang dipakai TUI atau `serve` manual.

## `serve`

`ngodingmcp serve` adalah mode headless yang dijalankan systemd: tanpa TUI, jalan
di foreground, log ke stdout/stderr (masuk journald). Biasanya tidak perlu diketik
sendiri. Kalau port-nya sudah dipakai, `serve` mencetak pesan yang jelas.
