# Thumbnail benchmark

Measured on 8 real Telegram images (`testdata/telegram/telegram-01.jpg` … `telegram-08.jpg`:
1024×1024 progressive JPEG, 102–443 KB each, exactly as Telegram served them). No synthetic
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
| Time per thumbnail, one at a time (mean) | 45.3 ms | 23.4 ms |
| Time per thumbnail, one at a time (median) | 43.6 ms | 23.0 ms |
| First call in the process | 48.5 ms | 19.6 ms |
| Bytes per thumbnail (mean) | 36,594 | 12,038 |
| Bytes per thumbnail (smallest – largest) | 14,488 – 99,882 | 5,938 – 28,980 |
| Throughput with 4 workers | 75.5 per second | 138.7 per second |

A whole tile (the 480 px and the 240 px thumbnail of one image) takes about 69 ms on one
core, and 4 workers make 48.7 tiles per second.

Every call is "cold": the libvips operation cache is off and each thumbnail is decoded from
the JPEG bytes again, so the per-thumbnail time is what a first request for an uncached tile
costs (without the Telegram download). The times include the pure-Go header validation.

The server also decodes every downloaded original once in full (`imaging.Verify`) before
storing it. That check takes about 13 ms per image (13.4 and 13.0 ms in two runs of
`BenchmarkVerify`), once, on the first request for a cold tile: about 82 ms of CPU for a
whole tile instead of about 69 ms.

Against the plan's original figures (51–57 ms per 480 px thumbnail cold, about 75 WebP per
second on 4 cores and about 44 KB per tile, measured with bimg on 13 other images): this
run is somewhat faster per thumbnail (45.3 ms) and the same in throughput (75.5 per second).
The mean 480 px size is smaller, 36.6 KB. The image sets differ, so the sizes in particular
are not a like-for-like comparison.

**One image misses the 60 KB target.** `telegram-04.jpg`, a pop-art picture covered in
halftone dots, is 99,882 bytes at 480 px with this libvips; the other seven are between
14,488 and 41,294 bytes (mean 27,554). Quality 76 and effort 4 are fixed, so this is an
accepted exception. The imaging test names that one file and gives it a 100 KB ceiling,
fails if any other sample is over 60 KB, and checks the means (≤ 50 KB at 480 px, ≤ 16 KB
at 240 px).

## Per image

Median of 15 runs per image and size, one pipeline at a time.

| image | JPEG bytes | 480 ms | 480 bytes | 240 ms | 240 bytes |
|---|---:|---:|---:|---:|---:|
| telegram-01.jpg | 160732 | 43.6 | 25252 | 21.0 | 7346 |
| telegram-02.jpg | 148604 | 40.6 | 23252 | 22.0 | 9470 |
| telegram-03.jpg | 102160 | 35.4 | 14488 | 16.5 | 7850 |
| telegram-04.jpg | 443038 | 62.9 | 99882 | 35.8 | 28980 |
| telegram-05.jpg | 252279 | 45.8 | 40472 | 24.8 | 11888 |
| telegram-06.jpg | 246523 | 47.0 | 41294 | 24.4 | 11844 |
| telegram-07.jpg | 131027 | 37.7 | 16892 | 19.3 | 5938 |
| telegram-08.jpg | 179206 | 41.1 | 31226 | 23.1 | 12990 |

## All runs

120 timed runs per size (8 images × 15).

| size | ms mean | ms median | ms min | ms max | first call ms | bytes mean | bytes min | bytes max |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| 480 | 45.3 | 43.6 | 31.6 | 83.1 | 48.5 | 36594 | 14488 | 99882 |
| 240 | 23.4 | 23.0 | 14.5 | 41.7 | 19.6 | 12038 | 5938 | 28980 |

## Throughput

Four goroutines (`Workers: 4`), each running pipelines back to back for 10 seconds over the
8 images.

| workload, 4 goroutines for 10 s | per second |
|---|---:|
| 480 px thumbnails | 75.5 |
| 240 px thumbnails | 138.7 |
| tiles (480 px + 240 px from the same JPEG) | 48.7 |

The standard Go benchmarks agree:

```
$ go test ./internal/imaging -run '^$' -bench 'Thumbnail|Verify' -benchtime 5s -cpu 1
BenchmarkThumbnail480 	     133	  45867207 ns/op	     36748 webp-bytes/op
BenchmarkThumbnail240 	     264	  23035817 ns/op	     12038 webp-bytes/op
BenchmarkVerify       	     452	  13360031 ns/op
$ go test ./internal/imaging -run '^$' -bench Parallel -benchtime 5s -cpu 4
BenchmarkParallel480-4   	     429	  13196916 ns/op	        75.78 thumbs/s
BenchmarkParallel240-4   	     880	   7010923 ns/op	       142.6 thumbs/s
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

- Every measurement was run twice, with other light work on the machine (load average 1–3.6
  around the runs). The tables above are the first run. The second run of the report gave
  46.1 ms and 22.8 ms mean per thumbnail (medians 43.1 and 22.0 ms), first calls of 43.4 and
  25.3 ms, and 76.2 / 139.3 / 48.6 per second with 4 workers. The second run of the Go
  benchmarks gave 46.4 ms and 23.3 ms, and 74.0 and 141.2 per second. So the means agree
  within about 3% and the throughput within about 2%. Single images vary more: the
  per-image medians moved by up to 5 ms at 480 px (`telegram-03.jpg` 35.4 → 40.4 ms,
  `telegram-05.jpg` 45.8 → 53.6 ms) and up to 5.4 ms at 240 px between the two runs.
  Byte sizes do not vary: the output is identical from run to run.
- Eight images are a small sample. Sizes depend heavily on the picture (6.9× between the
  smallest and largest here at 480 px), and one image out of eight moves the mean a lot:
  without `telegram-04.jpg` the 480 px mean is 27.6 KB instead of 36.6 KB.
- Not measured: the Telegram download, the disk cache and HTTP serving. Memory was not
  measured for the Telegram images at all; see "Memory".

## Reproduce

```
GALLERY_BENCH=1 go test -count=1 ./internal/imaging -run TestBenchReport -v
go test ./internal/imaging -run '^$' -bench 'Thumbnail|Verify' -benchtime 5s -cpu 1
go test ./internal/imaging -run '^$' -bench Parallel -benchtime 5s -cpu 4
```
