# Measured performance

From [BENCH.md](../BENCH.md), which has the per-image table, the caveats and the
commands to reproduce. Measured on 2026-11-13 on **one machine** (a 4-vCPU
virtual machine, Intel Xeon @ 2.10 GHz, Ubuntu 24.04.5, libvips 8.15.1, libwebp
1.3.2, libjpeg-turbo 2.1.5, govips v2.20.0, Go 1.27.4) with the **8 real
Telegram images** in `testdata/telegram/`. These are not synthetic images,
but they are a small sample on a shared machine: treat the numbers as an
indication, not a guarantee.

| | 480 px | 240 px |
|---|---:|---:|
| Time per thumbnail, one at a time (mean) | 47.9 ms | 23.4 ms |
| Time per thumbnail, one at a time (median) | 45.5 ms | 23.3 ms |
| First call in the process | 47.3 ms | 29.4 ms |
| Bytes per thumbnail (mean) | 31,371 | 11,206 |
| Bytes per thumbnail (smallest – largest) | 14,488 – 64,170 | 5,938 – 21,186 |
| Throughput with 4 workers | 71.2 per second | 126.9 per second |

A whole tile (both thumbnails of one image) takes about 71 ms on one core,
and 4 workers make 47.4 tiles per second. Every call decodes the JPEG again,
so this is what the first request for an uncached tile costs, without the
Telegram download.

Next to the figures the project was planned on (measured earlier with a
different Go binding, bimg, on 13 other Telegram images, also on 4 vCPU). The
image sets differ, so this is a rough comparison, least reliable for the sizes:

| | Plan (bimg, 13 images) | This build (govips, 8 images) |
|---|---:|---:|
| 480 px WebP thumbnail, cold | about 51–57 ms | 47.9 ms mean (47.3 ms for the first call; 47.1–51.5 ms over four runs) |
| 480 px WebP throughput, 4 cores | about 75 per second | 71.2 per second (67.2–73.1 over four runs) |
| Bytes per 480 px tile | about 44 KB | 31.4 KB mean |

Caveats, from BENCH.md:

- It is a shared virtual machine (load average 0.5 before the runs), and times
  vary. Everything was run twice: the second run of the report gave 47.1 ms and
  24.7 ms per thumbnail and 73.1 / 129.8 per second with 4 workers, so its
  means agree within 2% at 480 px and 6% at 240 px. The standard Go benchmarks,
  run straight afterwards, were slower at 480 px: 51.5 and 50.2 ms per
  thumbnail and 67.2 and 69.9 per second, up to 9% off the report. Per-image
  medians moved by up to 7.6 ms between the runs. Byte sizes do not vary.
- One image misses the plan's 60 KB target at 480 px: `telegram-07.jpg`, a
  finely detailed pattern of birds, is 64,170 bytes; the other seven are
  14,488–40,472 bytes. Quality 76 and effort 4 are fixed, so this is an
  accepted exception: the test names that one file, gives it a 65 KB
  ceiling, and fails if any other sample exceeds 60 KB.
- A cold tile also pays for one full check decode of the original (about
  14 ms on the same machine) before the thumbnails are made.
- Not measured there: the Telegram download, the disk cache, HTTP serving and
  memory (BENCH.md carries one memory figure from 2026-11-12 for a single
  49-megapixel file, measured on the earlier toolchain and not repeated).

Separately, the integration test measures a 240 px tile served from a warm
cache over loopback HTTP and fails above a 5 ms median; two runs of 300
requests on 2026-11-13 gave medians of 317 and 299 µs, p95 557 and 467 µs,
maximum 1.0 and 1.0 ms.

## In the browser

From the end-to-end tests (`e2e/perf.spec.js`): headless Chromium against the
dev stack on the same machine, empty browser cache, thumbnails already in the
gallery's cache. Measured on 2026-11-13 with the current eight images. The
page, script, stylesheet and first JSON page are 47.8 kB together on a first
visit. The page holds 59 tiles; on the loopback desktop visit (63 requests)
they come to about 31 kB each on average. Every image in these runs was
fetched at 480 px.

| Profile | Page + tiles in view | Whole first visit, throttled to 10 Mbit/s and 40 ms | Whole first visit, loopback |
|---|---:|---:|---:|
| 1440×900 | 688 kB (20 tiles) | 1,337 kB (44 requests; 40 images, 1,289 kB) | 1,850 kB (63 requests) |
| 1920×1080 | 830 kB (24 tiles) | 1,564 kB (52 requests; 48 images, 1,516 kB) | 1,850 kB |
| iPhone 15 profile | 280 kB (8 tiles) | 688 kB (24 requests; 20 images, 641 kB) | 1,276 kB (42 requests) |

- **With these images the whole throttled first visit is under the plan's
  2 MB target in every profile**, including 1920×1080 (1,564 kB), where
  Chromium's lazy-loading margin fetches 48 images although 24 tiles are in
  view. The tiles in view with the page are well under it everywhere.
- Page weight depends on the pictures. An earlier, heavier sample set
  measured 2.1 MB for the whole throttled first visit at 1920×1080, over the
  target. For that reason the test asserts the first column everywhere and
  the second column at 1440×900 and on the phone profile; the 1920×1080
  total is reported, not asserted.
- With no network latency at all (loopback) Chromium fetches every lazy image
  of the first batch at once, so those totals are larger; they are reported
  only. They were also under 2 MB in this run.
- A second visit transfers 33.6 kB and no image bytes, in every profile.
- A tile from a warm cache over loopback HTTP: median 0.39 ms and p95
  0.65 ms at 240 px (mean size 11,015 bytes), median 0.26 ms and p95 0.57 ms
  at 480 px (mean size 31,333 bytes); the test fails above 5 ms. A 304
  revalidation has a median of 0.18 ms. A cold tile was not timed end to end.
- Chromium only. The phone runs are Chromium with a device profile, not
  Safari, and the network is simulated.
