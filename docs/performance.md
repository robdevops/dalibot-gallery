# Measured performance

From [BENCH.md](../BENCH.md), which has the per-image table, the caveats and the
commands to reproduce. Measured on 2026-11-12 on **one machine** (a 4-vCPU
virtual machine, Intel Xeon @ 2.30 GHz, Ubuntu 24.04, libvips 8.15.1, libwebp
1.3.2, libjpeg-turbo 2.1.5, govips v2.17.0, Go 1.24.7; the project has since
moved to Go 1.27 and govips v2.20.0, and the figures have not been measured
again) with the **12 real
Telegram images** in `testdata/telegram/`. These are not synthetic images,
but they are a small sample on a shared machine: treat the numbers as an
indication, not a guarantee.

| | 480 px | 240 px |
|---|---:|---:|
| Time per thumbnail, one at a time (mean) | 42.4 ms | 21.0 ms |
| Time per thumbnail, one at a time (median) | 42.4 ms | 20.3 ms |
| First call in the process | 52.4 ms | 19.9 ms |
| Bytes per thumbnail (mean) | 42,795 | 13,036 |
| Bytes per thumbnail (smallest – largest) | 24,810 – 64,170 | 7,226 – 21,186 |
| Throughput with 4 workers | 82.2 per second | 154.4 per second |

A whole tile (both thumbnails of one image) takes about 63 ms on one core,
and 4 workers make 54.0 tiles per second. Every call decodes the JPEG again,
so this is what the first request for an uncached tile costs, without the
Telegram download.

Next to the figures the project was planned on (measured earlier with a
different Go binding, bimg, on 13 Telegram images, also on 4 vCPU):

| | Plan (bimg, 13 images) | This build (govips, 12 images) |
|---|---:|---:|
| 480 px WebP thumbnail, cold | about 51–57 ms | 42.4 ms mean (52.4 ms for the first call) |
| 480 px WebP throughput, 4 cores | about 75 per second | 82.2 per second |
| Bytes per 480 px tile | about 44 KB | 42.8 KB mean |

Caveats, from BENCH.md:

- The machine was shared with other jobs during the run (load average about
  3), and times vary: an earlier run gave 44.3 ms and 22.1 ms per thumbnail
  and 70.2 / 145.7 per second with 4 workers. Byte sizes do not vary.
- One image misses the plan's 60 KB target at 480 px: `telegram-10.jpg` is
  64,170 bytes; the other eleven are 24,810–59,550 bytes. Quality 76 and
  effort 4 are fixed, so this is an accepted exception: the test names that
  one file, gives it a 65 KB ceiling, and fails if any other sample exceeds 60 KB.
- The table was recorded before the loader got `fail_on=error`. The bytes
  are identical. A rerun of the Go benchmarks afterwards gave 46.1 ms and
  23.3 ms per thumbnail and 74.6 / 145.8 per second, inside the spread above.
- A cold tile also pays for one full check decode of the original (about
  12–13 ms on the same machine) before the thumbnails are made.
- Not measured there: the Telegram download, the disk cache, HTTP serving and memory.

Separately, the integration test measures a 240 px tile served from a warm
cache over loopback HTTP and fails above a 5 ms median; one recorded run of
300 requests gave a median of 220 µs, p95 401 µs, maximum 1.2 ms.

## In the browser

From the end-to-end tests (`e2e/perf.spec.js`): headless Chromium against the
dev stack on the same machine, empty browser cache, thumbnails already in the
gallery's cache. The page, script, stylesheet and first JSON page are 43 kB
together; every tile was fetched at 480 px, about 43 kB each.

| Profile | Page + tiles in view | Whole first visit, throttled to 10 Mbit/s and 40 ms | Whole first visit, loopback |
|---|---:|---:|---:|
| 1440×900, 1x and 2x | 884 kB (20 tiles) | 1,767 kB (40 images) | 2,579 kB (59 images) |
| 1920×1080 | 1,099 kB (24 tiles) | **2,131 kB (48 images)** | 2,579 kB (59 images) |
| iPhone 15 profile | 422 kB (8 tiles) | 884 kB (20 images) | 1,687 kB (38 images) |

- **The plan's "first screen transfers at most 2 MB" holds for the tiles in
  view on every profile, but not for everything a first visit downloads at
  1920×1080.** There Chromium's lazy-loading margin fetches 48 tiles, 2.13 MB
  in all, 6% over. The test asserts the first column everywhere and the
  second column at 1440×900 and on the phone profile; the 1920×1080 figure
  is reported, not asserted.
- With no network latency at all (loopback) Chromium fetches every lazy image
  of the first batch at once, so those totals are larger; they are reported only.
- A second visit transfers 33 kB and no image bytes.
- A tile from a warm cache, 400 requests over 20 tiles on loopback: median
  0.18–0.19 ms, p95 0.34–0.40 ms, maximum 0.9 ms for both sizes (the test
  fails above 5 ms). A cold tile was not timed end to end.
- Chromium only. The phone runs are Chromium with a device profile, not
  Safari, and the network is simulated.
