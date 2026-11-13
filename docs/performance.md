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
| Time per thumbnail, one at a time (mean) | 45.3 ms | 23.4 ms |
| Time per thumbnail, one at a time (median) | 43.6 ms | 23.0 ms |
| First call in the process | 48.5 ms | 19.6 ms |
| Bytes per thumbnail (mean) | 36,594 | 12,038 |
| Bytes per thumbnail (smallest – largest) | 14,488 – 99,882 | 5,938 – 28,980 |
| Throughput with 4 workers | 75.5 per second | 138.7 per second |

A whole tile (both thumbnails of one image) takes about 69 ms on one core,
and 4 workers make 48.7 tiles per second. Every call decodes the JPEG again,
so this is what the first request for an uncached tile costs, without the
Telegram download.

Next to the figures the project was planned on (measured earlier with a
different Go binding, bimg, on 13 other Telegram images, also on 4 vCPU). The
image sets differ, so this is a rough comparison, least reliable for the sizes:

| | Plan (bimg, 13 images) | This build (govips, 8 images) |
|---|---:|---:|
| 480 px WebP thumbnail, cold | about 51–57 ms | 45.3 ms mean (48.5 ms for the first call) |
| 480 px WebP throughput, 4 cores | about 75 per second | 75.5 per second |
| Bytes per 480 px tile | about 44 KB | 36.6 KB mean |

Caveats, from BENCH.md:

- The machine was doing other light work during the runs (load average 1–3.6),
  and times vary. Everything was run twice: the second run gave 46.1 ms and
  22.8 ms per thumbnail and 76.2 / 139.3 per second with 4 workers, so the
  means agree within about 3%. Per-image medians moved by up to 5 ms between
  the runs. Byte sizes do not vary.
- One image misses the plan's 60 KB target at 480 px: `telegram-04.jpg`, a
  halftone pop-art picture, is 99,882 bytes; the other seven are
  14,488–41,294 bytes. Quality 76 and effort 4 are fixed, so this is an
  accepted exception: the test names that one file, gives it a 100 KB
  ceiling, and fails if any other sample exceeds 60 KB.
- A cold tile also pays for one full check decode of the original (about
  13 ms on the same machine) before the thumbnails are made.
- Not measured there: the Telegram download, the disk cache, HTTP serving and
  memory (BENCH.md carries one memory figure from 2026-11-12 for a single
  49-megapixel file, measured on the earlier toolchain and not repeated).

Separately, the integration test measures a 240 px tile served from a warm
cache over loopback HTTP and fails above a 5 ms median; two runs of 300
requests on 2026-11-13 gave medians of 283 and 325 µs, p95 557 and 493 µs,
maximum 1.2 and 1.0 ms.

## In the browser

From the end-to-end tests (`e2e/perf.spec.js`): headless Chromium against the
dev stack on the same machine, empty browser cache, thumbnails already in the
gallery's cache. Measured on 2026-11-13 with the eight images. The page,
script, stylesheet and first JSON page are 45.5 kB together on a first visit.
The page holds 59 tiles; on the loopback desktop visit they come to about
42 kB each on average.

| Profile | Page + tiles in view | Whole first visit, throttled to 10 Mbit/s and 40 ms | Whole first visit, loopback |
|---|---:|---:|---:|
| 1440×900 | 915 kB (20 tiles) | 1,556 kB (44 requests) | 2,506 kB |
| 1920×1080 | 1,042 kB (24 tiles) | **2,075 kB (52 requests)** | 2,506 kB |
| iPhone 15 profile | 445 kB (8 tiles) | 915 kB (24 requests) | 1,498 kB |

- **The plan's "first screen transfers at most 2 MB" holds for the tiles in
  view on every profile, but not for everything a first visit downloads at
  1920×1080.** There Chromium's lazy-loading margin fetches tiles beyond the
  24 in view, 2,075 kB in all over 52 requests, about 4% over. The test
  asserts the first column everywhere and the second column at 1440×900 and
  on the phone profile; the 1920×1080 figure is reported, not asserted.
- With no network latency at all (loopback) Chromium fetches every lazy image
  of the first batch at once, so those totals are larger; they are reported only.
- A second visit transfers 33.4 kB and no image bytes, in every profile.
- A tile from a warm cache, 400 requests over 20 tiles on loopback: median
  0.29 ms and p95 0.57 ms at 240 px, median 0.36 ms and p95 0.67 ms at
  480 px (the test fails above 5 ms). A 304 revalidation has a median of
  0.15 ms. A cold tile was not timed end to end.
- Chromium only. The phone runs are Chromium with a device profile, not
  Safari, and the network is simulated.
