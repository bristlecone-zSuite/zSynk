package engine

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/bristlecone-zSuite/zSynk/internal/atomicfile"
)

// Scale and efficiency: the demonstrations for Phase 2's exit criterion.
// Each test runs at a size CI can afford, and each benchmark reports the
// numbers a bigger run would show. SYNK_SCALE_MB scales the big-file
// tests; SYNK_SCALE_IDLE_FILES scales the idle-library ones.

// scaleMB is how many megabytes the big-file tests and benchmarks move.
func scaleMB(t testing.TB) int {
	t.Helper()
	n := 64
	if v := os.Getenv("SYNK_SCALE_MB"); v != "" {
		if _, err := fmt.Sscanf(v, "%d", &n); err != nil || n <= 0 {
			t.Fatalf("SYNK_SCALE_MB=%q isn't a positive number", v)
		}
	}
	return n
}

// idleFiles is how many files the idle-library tests and benchmarks build.
func idleFiles(t testing.TB) int {
	t.Helper()
	n := 2_000
	if v := os.Getenv("SYNK_SCALE_IDLE_FILES"); v != "" {
		if _, err := fmt.Sscanf(v, "%d", &n); err != nil || n <= 0 {
			t.Fatalf("SYNK_SCALE_IDLE_FILES=%q isn't a positive number", v)
		}
	}
	return n
}

// idleKB is how many kilobytes each of those files holds.
func idleKB(t testing.TB) int {
	t.Helper()
	n := 1
	if v := os.Getenv("SYNK_SCALE_IDLE_KB"); v != "" {
		if _, err := fmt.Sscanf(v, "%d", &n); err != nil || n <= 0 {
			t.Fatalf("SYNK_SCALE_IDLE_KB=%q isn't a positive number", v)
		}
	}
	return n
}

func bigContent(t testing.TB, size int64) []byte {
	t.Helper()
	content := make([]byte, size)
	for i := range content {
		content[i] = byte(i * 31)
	}
	return content
}

// A file of any size syncs, and a transfer that stops part way continues
// where it stopped. The default size is 64 MiB; SYNK_SCALE_MB=4096 runs
// the 4 GiB the roadmap names.
func TestLargeFileSyncResumesAfterAnInterruption(t *testing.T) {
	if testing.Short() {
		t.Skip("long")
	}
	size := int64(scaleMB(t)) << 20
	content := bigContent(t, size)

	a := openNode(t, "A")
	b := openNode(t, "B")
	must(t, Pair(a, b))
	dirA := library(t, a, "notes")
	dirB := library(t, b, "notes")
	must(t, os.WriteFile(filepath.Join(dirA, "big.bin"), content, 0o600))
	h := atomicfile.HashBytes(content)

	start := time.Now()
	addr := listen(t, a)
	// The first sync stops when its reads pass a quarter of the file.
	if err := syncWithCut(t, b, addr, "notes", size/4); err == nil {
		t.Fatal("the interrupted sync reported success")
	}
	staged := b.Store.PartialSize(h)
	if staged <= 0 || staged >= size {
		t.Fatalf("staged %d of %d bytes after the interruption", staged, size)
	}
	if _, err := os.Stat(filepath.Join(dirB, "big.bin")); !os.IsNotExist(err) {
		t.Fatal("a partial transfer reached the library")
	}

	// The next sync finishes the file from where it stopped.
	must(t, b.SyncWith(addr, "notes"))
	if got := b.Store.PartialSize(h); got != 0 {
		t.Fatalf("staged content left behind: %d bytes", got)
	}
	if _, err := os.Stat(filepath.Join(dirB, "big.bin")); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dirB, "big.bin"))
	must(t, err)
	if !bytes.Equal(got, content) {
		t.Fatalf("the file that arrived is %d bytes, want %d", len(got), len(content))
	}
	t.Logf("%d MiB in %.1fs (%.0f MiB/s), resumed from %d bytes",
		size>>20, time.Since(start).Seconds(), float64(size>>20)/time.Since(start).Seconds(), staged)
}

// An idle daemon doesn't read its files again: a scan that finds nothing
// changed reports what the cache holds without reading any content. On a
// 20 GB library that's close to zero bytes a minute; this test proves
// the zero by planting a wrong hash where the content would be read.
func TestWarmScanReadsNoContent(t *testing.T) {
	a := openNode(t, "A")
	a.RacyWindow = 0 // the test file was written just now
	dir := library(t, a, "notes")
	must(t, os.WriteFile(filepath.Join(dir, "a.md"), []byte("hello"), 0o600))
	if _, err := a.ScanLibrary("notes"); err != nil {
		t.Fatal(err)
	}

	// What the cache holds is what the index reports, without reading.
	planted, err := a.Store.Put([]byte("planted"))
	must(t, err)
	n := a.caches["notes"]
	if n == nil || n.Entries["a.md"].Hash == planted {
		t.Fatalf("the cache doesn't hold the scan's result: %+v", n)
	}
	e := n.Entries["a.md"]
	e.Hash = planted
	n.Entries["a.md"] = e
	if _, err := a.ScanLibrary("notes"); err != nil {
		t.Fatal(err)
	}
	if got := a.Index("notes").Entries["a.md"].Hash; got != planted {
		t.Fatalf("the warm scan read the file and reported %s", got)
	}
}

// Memory doesn't depend on file size: a big file syncs through buffers,
// never whole in RAM. The test syncs one 256 MiB file over the network
// and bounds how far memory may rise while it moves.
func TestMemoryDoesNotScaleWithFileSize(t *testing.T) {
	if testing.Short() {
		t.Skip("long")
	}
	const size = 256 << 20
	content := bigContent(t, size)

	a := openNode(t, "A")
	b := openNode(t, "B")
	must(t, Pair(a, b))
	dirA := library(t, a, "notes")
	dirB := library(t, b, "notes")
	must(t, os.WriteFile(filepath.Join(dirA, "big.bin"), content, 0o600))
	runtime.GC()
	var base runtime.MemStats
	runtime.ReadMemStats(&base)

	stop := make(chan struct{})
	var peak uint64
	done := make(chan struct{})
	go func() {
		defer close(done)
		tick := time.NewTicker(5 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
				var m runtime.MemStats
				runtime.ReadMemStats(&m)
				if m.HeapInuse > peak {
					peak = m.HeapInuse
				}
			}
		}
	}()
	must(t, b.SyncWith(listen(t, a), "notes"))
	close(stop)
	<-done

	if delta := peak - base.HeapInuse; delta >= size/2 {
		t.Fatalf("syncing a %d MiB file raised memory by %d MiB; it must not hold the file whole",
			size>>20, delta>>20)
	}
	wantFile(t, dirB, "big.bin", string(content))
}

// SYNK_DIR holds the content the library's index references, the versions
// the vault keeps, and a small fixed share per file — nothing more. The
// test measures both a fresh library and one whose file was overwritten
// by a peer a few times.
func TestSynkDirOverheadIsBounded(t *testing.T) {
	const fileCount = 200
	const fileSize = 64 << 10

	a := openNode(t, "A")
	b := openNode(t, "B")
	must(t, Pair(a, b))
	dirA := library(t, a, "notes")
	library(t, b, "notes")

	// Half the files share content, as real libraries do.
	unique := make([][]byte, fileCount/2)
	for i := range unique {
		unique[i] = bytes.Repeat([]byte(fmt.Sprintf("content-%03d-", i)), fileSize/16)
	}
	for i := 0; i < fileCount; i++ {
		must(t, os.WriteFile(filepath.Join(dirA, fmt.Sprintf("f%03d.md", i)), unique[i%(fileCount/2)], 0o600))
	}
	must(t, SyncOnce(a, b, "notes"))

	uniqueBytes := int64(fileCount/2) * fileSize
	overhead := func() int64 {
		var total int64
		_ = filepath.WalkDir(b.DataDir, func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			if i, err := d.Info(); err == nil {
				total += i.Size()
			}
			return nil
		})
		return total
	}
	if got := overhead(); got > uniqueBytes+int64(fileCount)*2<<10+1<<20 {
		t.Fatalf("SYNK_DIR holds %d bytes for %d bytes of unique content across %d files",
			got, uniqueBytes, fileCount)
	}

	// Every version the vault keeps costs one copy of that file, and
	// nothing else grows with the number of versions.
	const versions = 3
	for i := 0; i < versions; i++ {
		must(t, os.WriteFile(filepath.Join(dirA, "f000.md"), fmt.Appendf(nil, "version %d of the file", i), 0o600))
		must(t, SyncOnce(a, b, "notes"))
	}
	if got := overhead(); got > uniqueBytes+int64(versions)*fileSize*2+int64(fileCount)*2<<10+1<<20 {
		t.Fatalf("SYNK_DIR holds %d bytes with %d kept versions of a %d-byte file",
			got, versions, fileSize)
	}
}

// BenchmarkLargeFileSync moves one big file between two devices over the
// network and reports the rate. SYNK_SCALE_MB=4096 shows the 4 GiB case.
func BenchmarkLargeFileSync(b *testing.B) {
	if testing.Short() {
		b.Skip("long")
	}
	size := int64(scaleMB(b)) << 20
	content := bigContent(b, size)

	for i := 0; i < b.N; i++ {
		b.StopTimer()
		a := openNode(b, "A")
		dst := openNode(b, "B")
		must(b, Pair(a, dst))
		dirA := library(b, a, "notes")
		dirB := library(b, dst, "notes")
		must(b, os.WriteFile(filepath.Join(dirA, "big.bin"), content, 0o600))
		addr := listen(b, a)
		b.StartTimer()

		must(b, dst.SyncWith(addr, "notes"))
		wantFile(b, dirB, "big.bin", string(content))

		b.StopTimer()
		must(b, a.Close())
		must(b, dst.Close())
	}
	b.ReportMetric(float64(size>>20)/float64(b.N), "MiB/op")
}

// BenchmarkIdleScanWarm scans an unchanged library again and again: what
// an idle daemon does between changes. SYNK_SCALE_IDLE_FILES=200000 with
// SYNK_SCALE_IDLE_KB=100 is the 20 GB library the roadmap names.
func BenchmarkIdleScanWarm(b *testing.B) {
	if testing.Short() {
		b.Skip("long")
	}
	n := idleFiles(b)
	size := idleKB(b) << 10
	a := openNode(b, "A")
	a.RacyWindow = 0
	dir := library(b, a, "notes")
	one := bytes.Repeat([]byte("idle library file "), size/18+1)
	for i := 0; i < n; i++ {
		must(b, os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%05d.md", i)), one, 0o600))
	}
	if _, err := a.ScanLibrary("notes"); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		res, err := a.ScanLibrary("notes")
		if err != nil {
			b.Fatal(err)
		}
		if len(res.Placeholders) != 0 || len(res.Errors) != 0 {
			b.Fatalf("scan reported %+v", res)
		}
	}
	b.StopTimer()
	b.ReportMetric(float64(n), "files")
	b.ReportMetric(float64(int64(n)*int64(size)>>20), "MB")
}

// BenchmarkMemoryTransfer reports how far memory rises while a big file
// moves between two devices.
func BenchmarkMemoryTransfer(b *testing.B) {
	if testing.Short() {
		b.Skip("long")
	}
	size := int64(scaleMB(b)) << 20
	content := bigContent(b, size)

	for i := 0; i < b.N; i++ {
		b.StopTimer()
		a := openNode(b, "A")
		dst := openNode(b, "B")
		must(b, Pair(a, dst))
		dirA := library(b, a, "notes")
		library(b, dst, "notes")
		must(b, os.WriteFile(filepath.Join(dirA, "big.bin"), content, 0o600))
		runtime.GC()
		var base runtime.MemStats
		runtime.ReadMemStats(&base)
		var peak uint64
		stop := make(chan struct{})
		done := make(chan struct{})
		go func() {
			defer close(done)
			tick := time.NewTicker(5 * time.Millisecond)
			defer tick.Stop()
			for {
				select {
				case <-stop:
					return
				case <-tick.C:
					var m runtime.MemStats
					runtime.ReadMemStats(&m)
					if m.HeapInuse > peak {
						peak = m.HeapInuse
					}
				}
			}
		}()
		b.StartTimer()

		must(b, dst.SyncWith(listen(b, a), "notes"))

		b.StopTimer()
		close(stop)
		<-done
		b.ReportMetric(float64((peak-base.HeapInuse)>>20), "MiB-peak")
		must(b, a.Close())
		must(b, dst.Close())
	}
}

// BenchmarkResumedSync syncs a big file after an interruption cut it in
// half, and reports how much the resuming session moves: the bytes the
// first transfer already delivered are not sent again.
func BenchmarkResumedSync(b *testing.B) {
	if testing.Short() {
		b.Skip("long")
	}
	size := int64(scaleMB(b)) << 20
	content := bigContent(b, size)

	for i := 0; i < b.N; i++ {
		b.StopTimer()
		a := openNode(b, "A")
		dst := openNode(b, "B")
		must(b, Pair(a, dst))
		dirA := library(b, a, "notes")
		library(b, dst, "notes")
		must(b, os.WriteFile(filepath.Join(dirA, "big.bin"), content, 0o600))
		h := atomicfile.HashBytes(content)
		addr := listen(b, a)
		b.StartTimer()

		if err := syncWithCut(b, dst, addr, "notes", size/2); err == nil {
			b.Fatal("the interrupted sync reported success")
		}
		staged := dst.Store.PartialSize(h)
		if staged <= 0 || staged >= size {
			b.Fatalf("staged %d of %d bytes", staged, size)
		}
		must(b, dst.SyncWith(addr, "notes"))

		b.StopTimer()
		b.ReportMetric(float64((size-staged)>>20), "MiB-resumed")
		b.ReportMetric(float64(staged>>20), "MiB-skipped")
		if got := dst.Store.PartialSize(h); got != 0 {
			b.Fatalf("staged content left behind: %d bytes", got)
		}
		must(b, a.Close())
		must(b, dst.Close())
	}
}

// BenchmarkSynkDirOverhead reports how much SYNK_DIR holds beside the
// library it mirrors: its blobs for unique content, its versions, and a
// small fixed share per file.
func BenchmarkSynkDirOverhead(b *testing.B) {
	if testing.Short() {
		b.Skip("long")
	}
	const fileCount = 1_000
	const fileSize = 64 << 10

	a := openNode(b, "A")
	dst := openNode(b, "B")
	must(b, Pair(a, dst))
	dirA := library(b, a, "notes")
	library(b, dst, "notes")
	unique := make([][]byte, fileCount/2)
	for i := range unique {
		unique[i] = bytes.Repeat([]byte(fmt.Sprintf("content-%03d-", i)), fileSize/16)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for j := 0; j < fileCount; j++ {
			must(b, os.WriteFile(filepath.Join(dirA, fmt.Sprintf("f%03d.md", j)), unique[j%(fileCount/2)], 0o600))
		}
		must(b, SyncOnce(a, dst, "notes"))
	}
	b.StopTimer()
	uniqueBytes := int64(fileCount/2) * fileSize
	b.ReportMetric(float64(fileCount), "files")
	b.ReportMetric(float64(uniqueBytes>>20), "MiB-unique")
}
