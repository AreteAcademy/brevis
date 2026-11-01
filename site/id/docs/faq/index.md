# FAQ

> Pertanyaan yang paling awal muncul, dengan jawaban singkat dan ke mana harus melangkah berikutnya.

*https://brevis.sh/id/docs/faq/ · brevis.sh docs (id)*

---

## Apakah brevis.sh benar-benar 100% sumber terbuka?

Ya. Runtime lengkapnya terbuka untuk dibaca, dipakai, diadaptasi, dan dikontribusikan.

## Apa arti rujukan kepada Seneca bagi proyek ini?

De Brevitate Vitae mengilhami rekayasa yang lebih disengaja: mengurangi kompleksitas yang menghabiskan waktu, dan menjaga ruang untuk pekerjaan yang bertujuan.

Selengkapnya di [Filosofi](/id/docs/philosophy/index.md).

## Bagaimana isolasi pod bekerja?

Setiap step workflow berjalan sebagai pod-nya sendiri dengan image-nya sendiri, menjaga dependensi dan konteks eksekusi tetap terpisah.

Selengkapnya di [Pod per step](/id/docs/pod-per-step/index.md).

## Apa hubungannya dengan Aretê Academy?

brevis.sh adalah proyek Aretê Academy, yang mendekatkan gagasan-gagasan besar dari filsafat kepada tantangan nyata dalam membangun perangkat lunak.

## Bisakah saya memakai hanya satu bagian?

Ya. Setiap bagian punya modul, versi, dan image sendiri: SDK adalah pustaka Go yang berjalan di program mana pun, Gateway dapat dijalankan sendiri dengan file YAML tanpa database, dan brevis-sql berjalan sebagai container. Core mengorkestrasi yang lain bila Anda menghendakinya — ia bukan prasyarat bagi satu pun dari mereka.

Selengkapnya di [Ekosistem](/id/docs/ecosystem/index.md).
