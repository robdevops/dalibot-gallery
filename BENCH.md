# Thumbnail benchmark

Measured on 12 real Telegram images (`testdata/telegram/telegram-01.jpg` … `telegram-12.jpg`:
1024×1024 progressive JPEG, 142–348 KB each, exactly as Telegram served them). No synthetic
images are used in these numbers.

| | |
|---|---|
| Date | 2026-11-12 |
| Machine | 4 vCPU, Intel Xeon @ 2.30 GHz (virtual machine), Linux, Ubuntu 24.04 |
| libvips | 8.15.1 (Ubuntu `libvips42t64` 8.15.1-1.1build4), libwebp 1.3.2, libjpeg-turbo 2.1.5 |
| govips | `github.com/davidbyttow/govips/v2` v2.17.0 |
| Go | 1.24.7 |
| Pipeline | `imaging.Thumbnail`: header check in Go, `vips_thumbnail_buffer` (centre crop), WebP quality 76, effort 4, metadata stripped; libvips concurrency 1, operation cache off |
| Note | The tables below were recorded before the JPEG loader was given `fail_on=error`. The output bytes are identical with it; see "After `fail_on=error`" for a rerun of the timings. |

## Summary

| | 480 px | 240 px |
|---|---:|---:|
| Time per thumbnail, one at a time (mean) | 42.4 ms | 21.0 ms |
| Time per thumbnail, one at a time (median) | 42.4 ms | 20.3 ms |
| First call in the process | 52.4 ms | 19.9 ms |
| Bytes per thumbnail (mean) | 42,795 | 13,036 |
| Bytes per thumbnail (smallest – largest) | 24,810 – 64,170 | 7,226 – 21,186 |
| Throughput with 4 workers | 82.2 per second | 154.4 per second |

A whole tile (the 480 px and the 240 px thumbnail of one image) takes about 63 ms on one
core, and 4 workers make 54.0 tiles per second.

Every call is "cold": the libvips operation cache is off and each thumbnail is decoded from
the JPEG bytes again, so the per-thumbnail time is what a first request for an uncached tile
costs (without the Telegram download). The times include the pure-Go header validation.

Against the brief's earlier figures (51–57 ms per 480 px thumbnail and about 75 per second
on 4 cores, measured with bimg on 13 images): this run is somewhat faster per thumbnail and
similar in throughput. The mean 480 px size (42.8 KB) matches the brief's ≈44 KB per tile.

**One image misses the 60 KB target.** `telegram-10.jpg` is 64,170 bytes at 480 px; the
other eleven are between 24,810 and 59,550 bytes. Quality 76 and effort 4 are fixed, so
this is an accepted exception. The imaging test names that one file and gives it a 65 KB
ceiling, fails if any other sample is over 60 KB, and checks the mean (≤ 50 KB).

## Per image

Median of 15 runs per image and size, one pipeline at a time.

| image | JPEG bytes | 480 ms | 480 bytes | 240 ms | 240 bytes |
|---|---:|---:|---:|---:|---:|
| telegram-01.jpg | 182117 | 39.2 | 29532 | 17.2 | 10154 |
| telegram-02.jpg | 193838 | 37.6 | 36380 | 18.0 | 14090 |
| telegram-03.jpg | 290794 | 46.1 | 39368 | 22.9 | 11010 |
| telegram-04.jpg | 271559 | 45.0 | 49702 | 20.2 | 14408 |
| telegram-05.jpg | 343538 | 46.7 | 49732 | 22.7 | 11334 |
| telegram-06.jpg | 291281 | 45.4 | 59550 | 21.8 | 18764 |
| telegram-07.jpg | 243882 | 39.2 | 29948 | 18.7 | 7226 |
| telegram-08.jpg | 287843 | 40.8 | 43726 | 20.1 | 11030 |
| telegram-09.jpg | 141736 | 33.4 | 24810 | 16.9 | 11342 |
| telegram-10.jpg | 348340 | 46.8 | 64170 | 25.4 | 21186 |
| telegram-11.jpg | 255445 | 36.6 | 33658 | 18.1 | 11108 |
| telegram-12.jpg | 303722 | 41.7 | 52964 | 25.2 | 14786 |

## All runs

180 timed runs per size (12 images × 15).

| size | ms mean | ms median | ms min | ms max | first call ms | bytes mean | bytes min | bytes max |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| 480 | 42.4 | 42.4 | 31.6 | 61.5 | 52.4 | 42795 | 24810 | 64170 |
| 240 | 21.0 | 20.3 | 14.9 | 47.2 | 19.9 | 13036 | 7226 | 21186 |

## Throughput

Four goroutines (`Workers: 4`), each running pipelines back to back for 10 seconds over the
12 images.

| workload, 4 goroutines for 10 s | per second |
|---|---:|
| 480 px thumbnails | 82.2 |
| 240 px thumbnails | 154.4 |
| tiles (480 px + 240 px from the same JPEG) | 54.0 |

The standard Go benchmarks agree:

```
$ go test ./internal/imaging -run '^$' -bench Thumbnail -benchtime 5s -cpu 1
BenchmarkThumbnail480 	     146	  42261879 ns/op	     42660 webp-bytes/op
BenchmarkThumbnail240 	     280	  22005516 ns/op	     13028 webp-bytes/op
$ go test ./internal/imaging -run '^$' -bench Parallel -benchtime 5s -cpu 4
BenchmarkParallel480-4   	     470	  12304436 ns/op	        81.27 thumbs/s
BenchmarkParallel240-4   	     901	   6473354 ns/op	       154.5 thumbs/s
```

## After `fail_on=error`

The loader now runs with `fail_on=error` (a truncated or corrupt JPEG is an error instead of
a partly grey thumbnail), each pipeline takes its share of a pixel budget, and the server
decodes every downloaded original once in full (`imaging.Verify`) before storing it. A rerun
of the Go benchmarks on the same machine later the same day, same 12 images:

```
$ go test ./internal/imaging -run '^$' -bench 'Thumbnail|Verify' -benchtime 5s -cpu 1
BenchmarkThumbnail480 	     144	  46143540 ns/op	     42795 webp-bytes/op
BenchmarkThumbnail240 	     289	  23287998 ns/op	     13027 webp-bytes/op
BenchmarkVerify       	     450	  12786465 ns/op
$ go test ./internal/imaging -run '^$' -bench Parallel -benchtime 5s -cpu 4
BenchmarkParallel480-4   	     422	  13397649 ns/op	        74.64 thumbs/s
BenchmarkParallel240-4   	     850	   6858837 ns/op	       145.8 thumbs/s
```

- Thumbnail bytes are unchanged, image by image (`telegram-10.jpg` is still 64,170 bytes).
- Times are 6–9% above the table (46.1 ms and 23.3 ms; 74.6 and 145.8 per second). That is
  inside the run-to-run spread noted under Caveats (44.3 ms, 22.1 ms, 70.2 and 145.7 per
  second in an earlier run of the old code), so this run does not show whether the change
  costs anything.
- The check decode adds about 12–13 ms, once per image, to the first request for a cold
  tile: about 82 ms of CPU for a whole tile instead of about 70 ms in this run.

Memory, measured once with a 7000×7000 (49 megapixel) progressive JPEG of 11.5 MB, which is
not a Telegram photo: four concurrent 480 px thumbnails plus four check decodes peaked at
202 MB resident (`VmHWM`), because the pixel budget decodes such images one at a time. The
same four thumbnails without the budget peaked at 651 MB in a reviewer's measurement.

## Caveats

- The machine was shared with other build jobs while this ran (load average about 3 just
  before the run), so times vary by run: an earlier run of the same report gave 44.3 ms and
  22.1 ms per thumbnail and 70.2 / 145.7 per second with 4 workers. Byte sizes do not vary:
  the output is identical from run to run.
- Twelve images are a small sample. Sizes depend heavily on the picture (2.6× between the
  smallest and largest here).
- Not measured: the Telegram download, the disk cache and HTTP serving. Memory was measured
  only for the one large file described above, not for the 12 Telegram images.

## Reproduce

```
GALLERY_BENCH=1 go test -count=1 ./internal/imaging -run TestBenchReport -v
go test ./internal/imaging -run '^$' -bench 'Thumbnail|Verify' -benchtime 5s -cpu 1
go test ./internal/imaging -run '^$' -bench Parallel -benchtime 5s -cpu 4
```
