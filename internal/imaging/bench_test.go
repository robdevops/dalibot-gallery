package imaging

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func benchInputs(b testing.TB) [][]byte {
	samples := telegramSamples(b)
	var out [][]byte
	for _, n := range sortedNames(samples) {
		out = append(out, samples[n])
	}
	return out
}

// Sequential: one pipeline at a time, cycling through the samples. Every call
// is cold: the libvips operation cache is off, so nothing is reused.
func benchSequential(b *testing.B, size int) {
	inputs := benchInputs(b)
	ctx := context.Background()
	bytesOut := 0
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		out, err := Thumbnail(ctx, inputs[i%len(inputs)], size)
		if err != nil {
			b.Fatal(err)
		}
		bytesOut += len(out)
	}
	b.ReportMetric(float64(bytesOut)/float64(b.N), "webp-bytes/op")
}

func BenchmarkThumbnail480(b *testing.B) { benchSequential(b, 480) }
func BenchmarkThumbnail240(b *testing.B) { benchSequential(b, 240) }

// Verify is the full decode that checks an original before it is stored.
func BenchmarkVerify(b *testing.B) {
	inputs := benchInputs(b)
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := Verify(ctx, inputs[i%len(inputs)]); err != nil {
			b.Fatal(err)
		}
	}
}

// Parallel: as many goroutines as worker slots (run with -cpu 4).
func benchParallel(b *testing.B, size int) {
	inputs := benchInputs(b)
	ctx := context.Background()
	var n atomic.Int64
	b.SetParallelism(1)
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			i := int(n.Add(1))
			if _, err := Thumbnail(ctx, inputs[i%len(inputs)], size); err != nil {
				b.Error(err)
				return
			}
		}
	})
	b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "thumbs/s")
}

func BenchmarkParallel480(b *testing.B) { benchParallel(b, 480) }
func BenchmarkParallel240(b *testing.B) { benchParallel(b, 240) }

// TestBenchReport prints the table recorded in BENCH.md:
//
//	GALLERY_BENCH=1 go test ./internal/imaging -run TestBenchReport -v
func TestBenchReport(t *testing.T) {
	if os.Getenv("GALLERY_BENCH") == "" {
		t.Skip("set GALLERY_BENCH=1 to run the benchmark report")
	}
	samples := telegramSamples(t)
	names := sortedNames(samples)
	ctx := context.Background()
	const rounds = 15

	var sb strings.Builder
	fmt.Fprintf(&sb, "\nlibvips %s, %d images, %d timed runs per image and size, quality %d, effort %d\n\n",
		Version(), len(names), rounds, DefaultQuality, reductionEffort)
	fmt.Fprintf(&sb, "| image | JPEG bytes | 480 ms | 480 bytes | 240 ms | 240 bytes |\n|---|---:|---:|---:|---:|---:|\n")

	type agg struct {
		ms    []float64
		bytes []int
	}
	all := map[int]*agg{240: {}, 480: {}}
	first := map[int]float64{}
	for _, name := range names {
		data := samples[name]
		row := map[int][2]float64{}
		for _, size := range []int{480, 240} {
			var ms []float64
			var n int
			for r := 0; r < rounds; r++ {
				start := time.Now()
				out, err := Thumbnail(ctx, data, size)
				d := float64(time.Since(start).Microseconds()) / 1000
				if err != nil {
					t.Fatal(err)
				}
				if _, seen := first[size]; !seen {
					first[size] = d
				}
				ms = append(ms, d)
				n = len(out)
			}
			sort.Float64s(ms)
			med := ms[len(ms)/2]
			row[size] = [2]float64{med, float64(n)}
			all[size].ms = append(all[size].ms, ms...)
			all[size].bytes = append(all[size].bytes, n)
		}
		fmt.Fprintf(&sb, "| %s | %d | %.1f | %.0f | %.1f | %.0f |\n", name, len(data), row[480][0], row[480][1], row[240][0], row[240][1])
	}
	sb.WriteString("\n| size | ms mean | ms median | ms min | ms max | first call ms | bytes mean | bytes min | bytes max |\n|---|---:|---:|---:|---:|---:|---:|---:|---:|\n")
	for _, size := range []int{480, 240} {
		a := all[size]
		sort.Float64s(a.ms)
		sort.Ints(a.bytes)
		var sum float64
		for _, v := range a.ms {
			sum += v
		}
		var bsum int
		for _, v := range a.bytes {
			bsum += v
		}
		fmt.Fprintf(&sb, "| %d | %.1f | %.1f | %.1f | %.1f | %.1f | %d | %d | %d |\n", size,
			sum/float64(len(a.ms)), a.ms[len(a.ms)/2], a.ms[0], a.ms[len(a.ms)-1], first[size],
			bsum/len(a.bytes), a.bytes[0], a.bytes[len(a.bytes)-1])
	}

	sb.WriteString("\n| workload, 4 goroutines for 10 s | per second |\n|---|---:|\n")
	run := func(label string, sizes ...int) {
		var done atomic.Int64
		var wg sync.WaitGroup
		deadline := time.Now().Add(10 * time.Second)
		start := time.Now()
		for g := 0; g < testWorkers; g++ {
			wg.Add(1)
			go func(g int) {
				defer wg.Done()
				for i := g; time.Now().Before(deadline); i++ {
					for _, size := range sizes {
						if _, err := Thumbnail(ctx, samples[names[i%len(names)]], size); err != nil {
							t.Error(err)
							return
						}
					}
					done.Add(1)
				}
			}(g)
		}
		wg.Wait()
		fmt.Fprintf(&sb, "| %s | %.1f |\n", label, float64(done.Load())/time.Since(start).Seconds())
	}
	run("480 px thumbnails", 480)
	run("240 px thumbnails", 240)
	run("tiles (480 px + 240 px from the same JPEG)", 480, 240)
	t.Log(sb.String())
}
