---
title: Konteks antar step
description: Apa yang dikatakan satu step kepada step berikutnya — dua variabel environment dan sebuah file.
group: Konsep
order: 7
slug: context
---

Sebuah step mempublikasikan nilai; step yang bergantung padanya membacanya.
Hanya itu yang dilakukan fitur ini.

```yaml
steps:
  - id: extract
    run: python fetch.py

  - id: transform
    run: python transform.py
    depends_on: [extract]
```

```python
# extract
from brevis import context
context.set(bucket="s3://landing/2026-09-07", rows=48213)
```

```python
# transform
from brevis import context
bucket = context.get("extract.bucket")
```

Key selalu berbentuk `<step id>.<name>`. Sebuah step membaca apa yang
dipublikasikan oleh **dependensinya sendiri**, dan tidak yang lain.

:::note Apa yang bukan fitur ini
Bukan message queue, bukan state antar run, dan bukan tempat menyimpan data. Ini
adalah catatan yang ditinggalkan satu step untuk step berikutnya — sebuah path,
watermark, atau jumlah.
:::

## Bahasa apa pun, tanpa pustaka wajib

Kontraknya adalah **dua variabel environment dan sebuah file**, sehingga step
tanpa SDK sama sekali pun dapat ikut serta:

```bash
window=$(jq -r '.extract.watermark' <<< "$BREVIS_INPUT")
./publish --until "$window"
jq -n --arg n "$(date -Is)" '{published_at: $n}' > "$BREVIS_OUTPUT"
```

| | |
|---|---|
| `BREVIS_INPUT` | `{"<step id>": {…}}` — apa yang dipublikasikan oleh dependensi step ini |
| `BREVIS_OUTPUT` | path untuk menulis **satu** objek JSON |

Di Kubernetes path itu adalah `/dev/termination-log`, yang sudah dibaca engine
untuk mendapatkan exit code. Jadi **tidak ada panggilan API, token, atau port** —
dan pustaka untuknya hanyalah parse JSON dan penulisan file.

| bahasa | |
|---|---|
| Python | [`pip install brevis`](/docs/python/) — juga membawa `brevis.run`, dengan [parameter otomatis](/docs/parameters/) |
| Go | `sdk/context`, bagian dari modul SDK, tidak bergantung pada apa pun |
| lainnya | dua variabel di atas |

## Isolasi

Tidak ada step yang dapat menimpa apa yang dipublikasikan step lain. Key diberi
prefiks id penulisnya, dan engine — bukan step — yang menentukan prefiks itu. Dua
step yang mempublikasikan `bucket` menghasilkan `extract.bucket` dan
`load.bucket`, dan tidak ada yang hilang.

## Batas 4096 byte

Apa yang dipublikasikan sebuah step harus muat dalam **4096 byte**. Batas ini
tidak sembarangan: di Kubernetes transportnya adalah `/dev/termination-log`, dan
itulah yang diterima kubelet.

Melebihinya adalah **error step**, dengan pesan yang menyebutkan berapa byte yang
ditulis. Alternatifnya adalah pemotongan — dan JSON yang terpotong adalah JSON
yang tidak valid, yang ditemukan oleh step berikutnya, yang tidak punya cara
untuk mengetahui bahwa kesalahannya bukan miliknya.

Jika sebuah nilai tidak muat, ia bukan context: tulis ke bucket dan publikasikan
path-nya.

## Retry dan run yang dilanjutkan

Pada percobaan baru, step mempublikasikan lagi dan nilai sebelumnya diganti.
Step yang sudah berhasil tidak dijalankan ulang dan tidak mempublikasikan ulang
— apa yang dikatakannya tetap berlaku bagi yang bergantung padanya.

## Menjalankan step secara manual

Kedua variabel itu adalah seluruh kontraknya, sehingga mereproduksi sebuah run
cukup dengan mengekspornya:

```bash
export BREVIS_INPUT='{"extract":{"bucket":"s3://landing/2026-09-07"}}'
export BREVIS_OUTPUT=/tmp/out.json
python transform.py
cat /tmp/out.json
```

## Langkah berikutnya

- [Parameter](/docs/parameters/) — apa yang berubah di antara run, dan yang otomatis
- [Runtime step](/docs/runtime/) — apa yang dideklarasikan sebuah step tentang tempat ia berjalan
- [Python](/docs/python/) — pustaka klien, dan bahasa lainnya
