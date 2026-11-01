---
title: White label
description: Mengubah judul, motto, logo, dan palet antarmuka lewat satu file YAML.
group: Operasi
order: 17
slug: white-label
---

Antarmuka dapat dikustomisasi lewat sebuah file. Warna menimpa variabel CSS
**saat runtime**, sehingga mengubah tema pelanggan tidak mengompilasi ulang apa
pun.

## File-nya

Salin `brand.example.yaml` ke `brand.yaml` di root, atau arahkan
`BREVIS_BRAND_FILE` ke sana:

```yaml
titulo: Acme Data
subtitulo: Data platform

frase: |
  Measure before deciding.
  Decide before scaling.

cores:
  fundo:    "#f4efe4"
  tinta:    "#21180f"
  destaque: "#aa8450"
```

**Setiap field bersifat opsional.** Yang Anda tinggalkan tetap memakai default,
sehingga file yang hanya berisi `titulo:` sudah valid.

Pemisah baris pada `frase` dipertahankan: ia menentukan ritme kalimat di sidebar.

## Logo

```yaml
logo: https://example.com/mark.svg
```

Menerima URL (`https://` atau `http://`) atau path internal yang diawali `/`.
Bila tidak ada, ia memakai simbol bawaan, yang mengikuti warna tema.

:::warning Logo yang di-host di tempat lain bergantung pada host itu
Jika host-nya mati, atau jika klaster tidak punya egress internet, gambar tidak
termuat. Bagian antarmuka lainnya tetap berfungsi, tetapi pertimbangkan itu saat
memilih — sisa UI, termasuk font dan bundle, dilayani dari binary itu sendiri
justru agar tidak bergantung pada jaringan.
:::

## Memvalidasi

Nilai hex yang salah baru akan muncul ketika container naik, dan pesannya akan
datang lewat log pod — jauh dari orang yang mengedit file. Karena itu ada
subcommand:

```bash
brevis marca brand.yaml
```

```
  ok    Acme Data · Data platform
        logo      /assets/logo.svg  (built-in symbol)
        accent    (the product's own)
        Powered by Brevis
```

Panggil di CI instalasi dan error kembali di pull request.

:::note Ketiadaan berarti hal yang berbeda
Di `serve`, file yang hilang berarti "pakai identitas default" — dan API tetap
naik. Di `marca`, file yang hilang adalah **error**: orang yang meminta
memvalidasi sebuah path berharap diberi tahu bahwa path itu tidak ada.
:::

## Perilaku saat boot

Error isi — warna tidak valid, YAML rusak — **tidak menghentikan antarmuka naik**.
Proses mencatat peringatan dan memakai tema default. Menjatuhkan API karena
sebuah warna akan lebih buruk daripada melayaninya dengan tema default.

```
level=WARN msg="visual identity ignored" file=brand.yaml error="brand.yaml: colour accent: \"#gggggg\" is not a hex value (#rgb, #rrggbb or #rrggbbaa)"
```

## Yang tidak dapat dikustomisasi

Baris **"Powered by Brevis"** di footer tidak berasal dari konfigurasi — ia
berasal dari kode.

## Di Kubernetes

Mount file dari ConfigMap:

```yaml
volumes:
  - name: brand
    configMap: {name: brevis-brand}
containers:
  - name: api
    volumeMounts:
      - {name: brand, mountPath: /etc/brevis}
    env:
      - name: BREVIS_BRAND_FILE
        value: /etc/brevis/brand.yaml
```
