# Thumbnail benchmark

Measured on 8 real Telegram images (`testdata/telegram/telegram-01.jpg` … `telegram-08.jpg`:
1024×1024 progressive JPEG, 102–348 KB each, exactly as Telegram served them). No synthetic
images are used in these numbers.

| | |
|---|---|
| Date | 2026-11-13 |
| Machine | 4 vCPU, Intel Xeon @ 2.10 GHz (virtual machine), Linux 6.18, Ubuntu 24.04.5 |
| libvips | 8.15.1 (Ubuntu `libvips42t64` 8.15.1-1.1build4), libwebp 1.3.2 (`libwebp7` 1.3.2-0.4build3), libjpeg-turbo 2.1.5 (`libjpeg-turbo8` 2.1.5-2ubuntu2) |
| govips | `github.com/davidbyttow/govips/v2` v2.20.0 |
| Go | 1.27.4 |
| Pipeline | `imaging.Thumbnail`: header check in Go, `vips_thumbnail_buffer` (centre crop, `fail_on=error`), WebP quality 76, effort 4, metadata stripped; libvips concurrency 1, operation cache off |
| Not covered | The container image is Alpine (musl, a newer libvips). Nothing here was measured on that stack, and thumbnail bytes can differ with the libvips version. |

## Summary

| | 480 px | 240 px |
|---|---:|---:|
| Time per thumbnail, one at a time (mean) | 47.9 ms | 23.4 ms |
| Time per thumbnail, one at a time (median) | 45.5 ms | 23.3 ms |
| First call in the process | 47.3 ms | 29.4 ms |
| Bytes per thumbnail (mean) | 31,371 | 11,206 |
| Bytes per thumbnail (smallest – largest) | 14,488 – 64,170 | 5,938 – 21,186 |
| Throughput with 4 workers | 71.2 per second | 126.9 per second |

A whole tile (the 480 px and the 240 px thumbnail of one image) takes about 71 ms on one
core, and 4 workers make 47.4 tiles per second.

Every call is "cold": the libvips operation cache is off and each thumbnail is decoded from
the JPEG bytes again, so the per-thumbnail time is what a first request for an uncached tile
costs (without the Telegram download). The times include the pure-Go header validation.

The server also decodes every downloaded original once in full (`imaging.Verify`) before
storing it. That check takes about 14 ms per image (14.0 and 13.4 ms in two runs of
`BenchmarkVerify`), once, on the first request for a cold tile: about 85 ms of CPU for a
whole tile instead of about 71 ms.

Against the plan's original figures (51–57 ms per 480 px thumbnail cold, about 75 WebP per
second on 4 cores and about 44 KB per tile, measured with bimg on 13 other images): this
run is a little faster per thumbnail (47.9 ms; 47.1 to 51.5 ms over all four runs, see
"Caveats") and a little lower in throughput (71.2 per second; 67.2 to 73.1 over all four
runs). The mean 480 px size is smaller, 31.4 KB. The image sets differ, so the sizes in
particular are not a like-for-like comparison.

**One image misses the 60 KB target.** `telegram-07.jpg`, a finely detailed pattern of
birds, is 64,170 bytes at 480 px with this libvips; the other seven are between 14,488 and
40,472 bytes (mean 26,685). Quality 76 and effort 4 are fixed, so this is an accepted
exception. The imaging test (`internal/imaging/imaging_test.go`) names that one file and
gives it a 65 KB ceiling, fails if any other sample is over 60 KB, and checks the means
(≤ 50 KB at 480 px, ≤ 16 KB at 240 px).

## Per image

Median of 15 runs per image and size, one pipeline at a time.

| image | JPEG bytes | 480 ms | 480 bytes | 240 ms | 240 bytes |
|---|---:|---:|---:|---:|---:|
| telegram-01.jpg | 160732 | 40.5 | 25252 | 19.4 | 7346 |
| telegram-02.jpg | 102160 | 38.6 | 14488 | 20.4 | 7850 |
| telegram-03.jpg | 252279 | 55.4 | 40472 | 25.2 | 11888 |
| telegram-04.jpg | 131027 | 39.1 | 16892 | 19.4 | 5938 |
| telegram-05.jpg | 179206 | 42.5 | 31226 | 23.8 | 12990 |
| telegram-06.jpg | 141736 | 45.1 | 24810 | 20.0 | 11342 |
| telegram-07.jpg | 348340 | 62.6 | 64170 | 30.9 | 21186 |
| telegram-08.jpg | 255445 | 45.5 | 33658 | 25.8 | 11108 |

## All runs

120 timed runs per size (8 images × 15).

| size | ms mean | ms median | ms min | ms max | first call ms | bytes mean | bytes min | bytes max |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| 480 | 47.9 | 45.5 | 30.3 | 78.5 | 47.3 | 31371 | 14488 | 64170 |
| 240 | 23.4 | 23.3 | 16.1 | 35.6 | 29.4 | 11206 | 5938 | 21186 |

## Throughput

Four goroutines (`Workers: 4`), each running pipelines back to back for 10 seconds over the
8 images.

| workload, 4 goroutines for 10 s | per second |
|---|---:|
| 480 px thumbnails | 71.2 |
| 240 px thumbnails | 126.9 |
| tiles (480 px + 240 px from the same JPEG) | 47.4 |

The standard Go benchmarks, run straight afterwards, came out somewhat slower at 480 px:

```
$ go test ./internal/imaging -run '^$' -bench 'Thumbnail|Verify' -benchtime 5s -cpu 1
BenchmarkThumbnail480 	     100	  51462408 ns/op	     31087 webp-bytes/op
BenchmarkThumbnail240 	     248	  24276378 ns/op	     11206 webp-bytes/op
BenchmarkVerify       	     399	  14014582 ns/op
$ go test ./internal/imaging -run '^$' -bench Parallel -benchtime 5s -cpu 4
BenchmarkParallel480-4   	     415	  14878761 ns/op	        67.21 thumbs/s
BenchmarkParallel240-4   	     694	   8079148 ns/op	       123.8 thumbs/s
```

(`webp-bytes/op` in the 480 px line is not exactly the table's mean because the number of
iterations is not a multiple of 8.)

## Memory

Not measured again for this report. It was measured once, on 2026-11-12, with the earlier
toolchain (Go 1.24.7, govips v2.17.0, the same libvips 8.15.1), using a 7000×7000
(49 megapixel) progressive JPEG of 11.5 MB, which is not a Telegram photo and not one of the
samples: four concurrent 480 px thumbnails plus four check decodes peaked at 202 MB resident
(`VmHWM`), because the pixel budget decodes such images one at a time. The same four
thumbnails without the budget peaked at 651 MB in a reviewer's measurement.

## Caveats

- Every measurement was run twice, one after the other: the report, the report again, then
  the Go benchmarks twice. The load average was 0.5 before the first run; nothing else was
  started on the machine, but it is a shared virtual machine. The tables above are the
  first run of each. The second run of the report gave 47.1 ms and 24.7 ms mean per
  thumbnail (medians 46.1 and 25.0 ms), first calls of 45.1 and 21.1 ms, and
  73.1 / 129.8 / 45.3 per second with 4 workers. The second run of the Go benchmarks gave
  50.2 ms and 24.5 ms, 13.4 ms for `Verify`, and 69.9 and 130.0 per second. So the two
  report runs agree within 2% at 480 px and 6% at 240 px, and their throughput within 3%
  (5% for tiles). The Go benchmarks were 5–9% slower than the report at 480 px (50.2 and
  51.5 ms against 47.1 and 47.9 ms) and 2–8% lower in 480 px throughput (67.2 and 69.9
  against 71.2 and 73.1 per second). Over all four runs: 47.1–51.5 ms at 480 px,
  23.4–24.7 ms at 240 px, 67.2–73.1 and 123.8–130.0 per second. Single images vary more:
  the per-image medians moved by up to 7.6 ms at 480 px (`telegram-07.jpg` 62.6 → 55.0 ms,
  `telegram-04.jpg` 39.1 → 44.1 ms) and up to 5.5 ms at 240 px (`telegram-01.jpg`
  19.4 → 24.9 ms) between the two report runs. Byte sizes do not vary: the output is
  identical from run to run.
- Eight images are a small sample. Sizes depend heavily on the picture (4.4× between the
  smallest and largest here at 480 px), and one image out of eight moves the mean a lot:
  without `telegram-07.jpg` the 480 px mean is 26.7 KB instead of 31.4 KB.
- Not measured: the Telegram download, the disk cache and HTTP serving. Memory was not
  measured for the Telegram images at all; see "Memory".

## Reproduce

```
GALLERY_BENCH=1 go test -count=1 ./internal/imaging -run TestBenchReport -v
go test ./internal/imaging -run '^$' -bench 'Thumbnail|Verify' -benchtime 5s -cpu 1
go test ./internal/imaging -run '^$' -bench Parallel -benchtime 5s -cpu 4
```
