# bebii-seo-dashboard

MVP portal verifikasi plugin berbasis Golang + HTMX.

## Fitur MVP

- Login/logout dengan session cookie
- Role `admin` dan `user`
- Admin CRUD user (create, toggle active, reset password)
- Domain whitelist per user (admin dan user)
- License key per user + reset license key dari admin
- Endpoint verifikasi lisensi: `GET /api/verify?domain=example.com&license_key=...`
- Audit verify logs (IP, user-agent, source, status) dan halaman admin `/admin/logs`
- **Artifact registry**: upload manual `.tar.gz`/`.zip` di `/admin/registry`, atau push dari GitHub Actions; pull via API untuk server manapun

## Tech Stack

- Go (`net/http`, `html/template`)
- MySQL (`github.com/go-sql-driver/mysql`)
- GORM (`gorm.io/gorm`)
- HTMX (CDN pada halaman)

## Jalankan Lokal

1. Masuk ke folder project:

```bash
cd bebii-seo-dashboard
```

2. Install dependency:

```bash
go mod tidy
```

3. Jalankan server:

```bash
go run ./cmd/server
```

Server berjalan di `http://localhost:8080`.
Database akan otomatis dibuat saat startup jika belum ada (`CREATE DATABASE IF NOT EXISTS`).

## Environment Variable (opsional)

- `DB_HOST` (default: `127.0.0.1`)
- `DB_PORT` (default: `3306`)
- `DB_USER` (default: `root`)
- `DB_PASSWORD` (default: kosong)
- `DB_NAME` (default: `bebii_seo_dashboard`)
- `PLUGIN_SHARED_TOKEN` (opsional, jika diisi maka endpoint verify wajib header `x-plugin-token`)
- `BEBII_GLOBAL_KEY` (opsional, untuk decrypt header `x-digital` ala auth-lp)
- `REGISTRY_ROOT` (default: `data/registry`) — penyimpanan paket di disk
- `REGISTRY_UPLOAD_TOKEN` (opsional; wajib untuk upload API dari CI; upload admin UI tidak perlu token ini)
- `REGISTRY_READ_TOKEN` (opsional; jika diisi, download API memerlukan token yang sama)

Contoh `.env`:

```bash
DB_HOST=127.0.0.1
DB_PORT=3306
DB_USER=root
DB_PASSWORD=
DB_NAME=bebii_seo_dashboard
PLUGIN_SHARED_TOKEN=
BEBII_GLOBAL_KEY=
```

## Akun Seeder Awal

Jika email belum ada di database, sistem otomatis seed:

- Admin:
  - Email: `admin@bebii.local`
  - Password: `admin12345`
- User:
  - Email: `user@bebii.local`
  - Password: `user12345`

Seeder disimpan di `internal/db/seeder.go` dan saat startup dipanggil lewat `db.SeedDefaultData(...)`.

Disarankan ganti password segera via fitur reset password.

## Smoke Test Manual

1. Login sebagai admin.
2. Buat user baru role `user`.
3. Buka `Manage Domains` untuk user tersebut, tambah domain `example.com`.
4. Login sebagai user baru, cek domain muncul, tambah/hapus domain.
5. Ambil `license_key` dari dashboard user atau tabel admin users, lalu uji endpoint:

```bash
curl -H "x-plugin-token: YOUR_TOKEN" "http://localhost:8080/api/verify?domain=example.com&license_key=bebii-xxxx"
```

Contoh respons:

```json
{"success":true,"message":"Website valid.","status":200,"domain":"example.com","license_key":"bebii-xxxx"}
```

Jika plugin mengirim `x-digital` terenkripsi (dengan `BEBII_GLOBAL_KEY`), endpoint akan decrypt otomatis seperti flow `auth-lp`. Mode ini tidak membutuhkan Redis.

6. Deactivate user dari admin, lalu cek ulang endpoint verify untuk domain user tersebut (harus `allowed: false` jika tidak ada user aktif lain dengan domain sama).

## Artifact Registry

1. Login admin → tab **Registry** (`/admin/registry`).
2. Upload manual: isi nama artefak (huruf kecil, angka, strip), versi, pilih file.
3. Untuk CI: set `REGISTRY_UPLOAD_TOKEN` di `.env`, lalu salin workflow dari `.github/workflows/publish-to-bebii-registry.example.yml` ke repo proyek yang di-build (ringkas: `deploy/registry-upload.example.yml`).

```bash
# Manifest
curl -fsS "http://localhost:8080/api/registry/my-artifact/manifest"

# Download latest (JSON)
curl -fsS "http://localhost:8080/api/registry/my-artifact/latest"

# Download file
curl -fsSL -o pkg.tar.gz "http://localhost:8080/api/registry/my-artifact/1.0.0/my-artifact-1.0.0.tar.gz"
```
