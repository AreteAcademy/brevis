---
title: Filosofi
description: Dari mana brevis.sh berasal: Seneca, Aretê Academy, dan apa yang ditentukannya dalam kode.
group: Proyek
order: 30
slug: philosophy
serif: true
---

brevis.sh lahir di dalam Aretê Academy: disiplin, tujuan, dan keunggulan yang diterapkan pada pembuatan perangkat lunak.

## De brevitate vitae

> Bukan karena kita punya sedikit waktu, melainkan karena kita menyia-nyiakan banyak.

Maksim Seneca memandu pertanyaan di balik brevis.sh: kompleksitas mana yang benar-benar layak mendapat waktu sebuah tim? Setiap workflow berjalan di pod yang terisolasi agar kegagalan, versi, dan dependensi memiliki batas yang jelas.

Empat pilihan dalam runtime lahir dari pertanyaan itu:

- **image per step** — Setiap step mendeklarasikan runtime yang dibutuhkannya. Menaikkan satu versi tidak memaksa Anda menyentuh yang lain.
- **log per run** — Output milik run yang menghasilkannya. Menyelidiki kegagalan bukan membangun ulang konteks yang hilang.
- **retry persisten** — Percobaan bertahan lebih lama dari prosesnya. Restart tidak menghapus apa yang sudah diketahui tentang pekerjaan itu.
- **isolasi dependensi** — Apa yang dibawa satu step tidak merembes ke tetangganya. Batas yang jelas adalah yang membuat kegagalan dapat dibaca.

## Apa yang diusulkan proyek ini

Di antara *filsafat tanpa praktik* dan *teknologi tanpa arah*, **kejernihan untuk mengerjakan apa yang penting.**

## Alat membentuk cara kita berpikir.

> “Kode yang berkelanjutan bukan sekadar kode yang terus berjalan. Ia adalah kode yang menyisakan ruang bagi orang untuk bernalar.” — Filsafat yang diterapkan pada perangkat lunak

> “Kejernihan bukanlah lawan dari kecanggihan. Ia adalah kriteria yang menjaga kecanggihan agar tidak menjadi noise.” — Sumber terbuka berdasarkan prinsip
