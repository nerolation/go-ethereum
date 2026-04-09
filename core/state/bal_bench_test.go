// bal_bench_test.go benchmarks batch-IO (prefetch) vs sequential IO for
// EIP-7928 Block Access Lists.
//
// The flagship benchmark models a worst-case 300M-gas block of pure SLOADs
// (300M / 2100 = 142,857 reads), where each SLOAD has a serial dependency
// on the previous one (keccak of the result determines the next action).
//
// Three execution modes are compared:
//
//  1. RawBatchIO:  All Pebble reads issued in parallel via goroutines,
//     results stored in a flat array. Then the serial chain
//     walks the array (instant lookups). This is the physical
//     upper bound — pure parallel IO, no framework overhead.
//
//  2. GethBatchIO: Uses geth's ReaderEIP7928 prefetch layer. Prefetch
//     goroutines read through the stateReaderWithCache. The
//     serial chain reads from the same cache.
//
//  3. NoBatchIO:   No prefetch. Each SLOAD is a cold Pebble read,
//     serialized with the keccak dependency computation.
//
// Usage:
//
//	BAL_BENCH_DB=/path/to/geth-db go test -bench BenchmarkBAL -run ^$ -timeout 60m ./core/state/
package state

import (
	"fmt"
	"math"
	mrand "math/rand"
	"os"
	"runtime"
	"sync"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/ethdb/pebble"
	"github.com/ethereum/go-ethereum/triedb"
	"github.com/ethereum/go-ethereum/triedb/pathdb"
)

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

const (
	numGeneratedEOAs = 268_435_456
	fullBlockSLOADs  = 142_857 // 300M gas / 2100 gas per SLOAD
)

// ---------------------------------------------------------------------------
// Database setup
// ---------------------------------------------------------------------------

type testDB struct {
	stateDB   *CachingDB
	rawDB     ethdb.KeyValueReader // raw Pebble handle for direct reads
	stateRoot common.Hash
}

var (
	sharedDB     *testDB
	sharedDBOnce sync.Once
	sharedDBErr  error
)

func getTestDB(tb testing.TB) *testDB {
	tb.Helper()
	dbPath := os.Getenv("BAL_BENCH_DB")
	if dbPath == "" {
		tb.Skip("BAL_BENCH_DB not set; skipping BAL benchmark")
	}
	sharedDBOnce.Do(func() {
		sharedDB, sharedDBErr = openTestDB(dbPath)
	})
	if sharedDBErr != nil {
		tb.Fatalf("failed to open test database: %v", sharedDBErr)
	}
	return sharedDB
}

func openTestDB(dbPath string) (*testDB, error) {
	pdb, err := pebble.New(dbPath, 2048, 256, "bal-bench/", true)
	if err != nil {
		return nil, fmt.Errorf("open pebble: %w", err)
	}
	db := rawdb.NewDatabase(pdb)

	scheme := rawdb.ReadStateScheme(db)
	var tdbConfig *triedb.Config
	switch scheme {
	case rawdb.PathScheme:
		tdbConfig = &triedb.Config{PathDB: pathdb.ReadOnly}
	default:
		tdbConfig = triedb.HashDefaults
	}

	tdb := triedb.NewDatabase(db, tdbConfig)
	codeDB := NewCodeDB(pdb)
	sdb := NewDatabase(tdb, codeDB)

	root := rawdb.ReadSnapshotRoot(pdb)
	if root == (common.Hash{}) {
		return nil, fmt.Errorf("no snapshot root found in database")
	}
	if _, err := sdb.Reader(root); err != nil {
		return nil, fmt.Errorf("cannot create reader for root %s: %w", root, err)
	}
	return &testDB{stateDB: sdb, rawDB: pdb, stateRoot: root}, nil
}

// ---------------------------------------------------------------------------
// Address / slot generation
// ---------------------------------------------------------------------------

type contractInfo struct {
	addr  common.Address
	slots []common.Hash
}

func generateOneContract(rng *mrand.Rand, codeSize, minSlots, maxSlots int) contractInfo {
	var c contractInfo
	rng.Read(c.addr[:])
	rng.Intn(1000)
	rng.Intn(100)
	csVar := codeSize + rng.Intn(codeSize)
	codeSeed := rng.Int63()
	alpha := 1.5
	u := rng.Float64()
	slotsF := float64(minSlots) / math.Pow(1-u, 1/alpha)
	if slotsF > float64(maxSlots) {
		slotsF = float64(maxSlots)
	}
	numSlots := int(slotsF)
	cRng := mrand.New(mrand.NewSource(codeSeed))
	waste := make([]byte, csVar)
	cRng.Read(waste)
	c.slots = make([]common.Hash, numSlots)
	for j := 0; j < numSlots; j++ {
		cRng.Read(c.slots[j][:])
		var dummy common.Hash
		cRng.Read(dummy[:])
	}
	return c
}

func advanceRNGPastEOAs(rng *mrand.Rand, n int) {
	var buf [20]byte
	for i := 0; i < n; i++ {
		rng.Read(buf[:])
		rng.Intn(1000)
		rng.Intn(1000)
	}
}

// ---------------------------------------------------------------------------
// Workload types
// ---------------------------------------------------------------------------

// storageRead is a single SLOAD: address + slot.
type storageRead struct {
	addr common.Address
	slot common.Hash
}

// hashedRead is a pre-hashed Pebble key for direct snapshot reads.
type hashedRead struct {
	addrHash common.Hash
	slotHash common.Hash
}

func flattenSlots(contracts []contractInfo, maxItems int) []storageRead {
	var reads []storageRead
	for _, c := range contracts {
		for _, slot := range c.slots {
			reads = append(reads, storageRead{addr: c.addr, slot: slot})
			if len(reads) >= maxItems {
				return reads
			}
		}
	}
	return reads
}

// preHashReads computes keccak256(addr) and keccak256(slot) for every read,
// exactly as the flat reader does. This moves the hashing cost out of the
// timed section so we measure pure IO.
func preHashReads(reads []storageRead) []hashedRead {
	hashed := make([]hashedRead, len(reads))
	for i, r := range reads {
		hashed[i] = hashedRead{
			addrHash: crypto.Keccak256Hash(r.addr[:]),
			slotHash: crypto.Keccak256Hash(r.slot[:]),
		}
	}
	return hashed
}

func buildStorageAccessList(reads []storageRead) map[common.Address][]common.Hash {
	al := make(map[common.Address][]common.Hash)
	for _, r := range reads {
		al[r.addr] = append(al[r.addr], r.slot)
	}
	return al
}

// ---------------------------------------------------------------------------
// Shared full-block workload
// ---------------------------------------------------------------------------

var (
	fullBlockReads  []storageRead
	fullBlockHashed []hashedRead
	fullBlockAL     map[common.Address][]common.Hash
	fullBlockOnce   sync.Once
	fullBlockNContr int
)

func getFullBlockWorkload(tb testing.TB) ([]storageRead, []hashedRead, map[common.Address][]common.Hash) {
	tb.Helper()
	fullBlockOnce.Do(func() {
		tb.Log("Advancing RNG past 268M EOAs (~5s)...")
		rng := mrand.New(mrand.NewSource(42))
		advanceRNGPastEOAs(rng, numGeneratedEOAs)

		tb.Log("Generating contracts for 142,857 SLOADs...")
		var contracts []contractInfo
		var total int
		for total < fullBlockSLOADs {
			c := generateOneContract(rng, 1024, 1, 10000)
			contracts = append(contracts, c)
			total += len(c.slots)
		}
		fullBlockNContr = len(contracts)
		fullBlockReads = flattenSlots(contracts, fullBlockSLOADs)
		fullBlockHashed = preHashReads(fullBlockReads)
		fullBlockAL = buildStorageAccessList(fullBlockReads)
		tb.Logf("Workload ready: %d contracts, %d SLOADs", fullBlockNContr, len(fullBlockReads))
	})
	return fullBlockReads, fullBlockHashed, fullBlockAL
}

// ---------------------------------------------------------------------------
// Mode 1: Raw batch IO — parallel Pebble reads via goroutines.
// This is the physical upper bound: no cache layer, no mutexes between
// readers. Just goroutines doing raw Pebble Gets.
// ---------------------------------------------------------------------------

func runRawBatchIO(b *testing.B, db ethdb.KeyValueReader, hashed []hashedRead, nWorkers int) {
	b.Helper()
	n := len(hashed)
	results := make([][]byte, n)

	for iter := 0; iter < b.N; iter++ {
		// Phase 1: parallel Pebble reads — all 142K at once.
		var wg sync.WaitGroup
		chunkSize := (n + nWorkers - 1) / nWorkers
		for w := 0; w < nWorkers; w++ {
			lo := w * chunkSize
			hi := lo + chunkSize
			if lo >= n {
				break
			}
			if hi > n {
				hi = n
			}
			wg.Add(1)
			go func(lo, hi int) {
				defer wg.Done()
				for i := lo; i < hi; i++ {
					results[i] = rawdb.ReadStorageSnapshot(db, hashed[i].addrHash, hashed[i].slotHash)
				}
			}(lo, hi)
		}
		wg.Wait()

		// Phase 2: serial dependency chain over cached results.
		var sink common.Hash
		for i := range results {
			sink = crypto.Keccak256Hash(sink[:], results[i])
		}
		runtime.KeepAlive(sink)
	}
}

// ---------------------------------------------------------------------------
// Mode 2: Geth's ReaderEIP7928 (prefetch through cache layer).
// ---------------------------------------------------------------------------

func runGethBatchIO(b *testing.B, tdb *testDB, reads []storageRead, al map[common.Address][]common.Hash, threads int) {
	b.Helper()
	var sink common.Hash
	for i := 0; i < b.N; i++ {
		reader, err := tdb.stateDB.ReaderEIP7928(tdb.stateRoot, al, threads)
		if err != nil {
			b.Fatal(err)
		}
		for _, r := range reads {
			val, err := reader.Storage(r.addr, r.slot)
			if err != nil {
				b.Fatal(err)
			}
			sink = crypto.Keccak256Hash(sink[:], val[:])
		}
		closePrefetcher(reader)
	}
	runtime.KeepAlive(sink)
}

// ---------------------------------------------------------------------------
// Mode 3: No batch IO — sequential cold reads.
// ---------------------------------------------------------------------------

func runNoBatchIO(b *testing.B, tdb *testDB, reads []storageRead) {
	b.Helper()
	empty := make(map[common.Address][]common.Hash)
	var sink common.Hash
	for i := 0; i < b.N; i++ {
		reader, err := tdb.stateDB.ReaderEIP7928(tdb.stateRoot, empty, runtime.NumCPU())
		if err != nil {
			b.Fatal(err)
		}
		for _, r := range reads {
			val, err := reader.Storage(r.addr, r.slot)
			if err != nil {
				b.Fatal(err)
			}
			sink = crypto.Keccak256Hash(sink[:], val[:])
		}
		closePrefetcher(reader)
	}
	runtime.KeepAlive(sink)
}

// Mode 3b: Raw sequential — direct Pebble reads, no geth layers.
func runRawSequential(b *testing.B, db ethdb.KeyValueReader, hashed []hashedRead) {
	b.Helper()
	var sink common.Hash
	for iter := 0; iter < b.N; iter++ {
		for i := range hashed {
			data := rawdb.ReadStorageSnapshot(db, hashed[i].addrHash, hashed[i].slotHash)
			sink = crypto.Keccak256Hash(sink[:], data)
		}
	}
	runtime.KeepAlive(sink)
}

// ---------------------------------------------------------------------------
// Flagship benchmark: full worst-case block (142,857 SLOADs)
// ---------------------------------------------------------------------------

func BenchmarkBAL_FullBlock(b *testing.B) {
	tdb := getTestDB(b)
	reads, hashed, al := getFullBlockWorkload(b)

	b.Run("RawBatchIO_32t", func(b *testing.B) {
		runRawBatchIO(b, tdb.rawDB, hashed, 32)
	})
	b.Run("RawSequential", func(b *testing.B) {
		runRawSequential(b, tdb.rawDB, hashed)
	})
	b.Run("GethBatchIO", func(b *testing.B) {
		runGethBatchIO(b, tdb, reads, al, runtime.NumCPU())
	})
	b.Run("GethNoBatchIO", func(b *testing.B) {
		runNoBatchIO(b, tdb, reads)
	})
}

// ---------------------------------------------------------------------------
// Scaling benchmark
// ---------------------------------------------------------------------------

func BenchmarkBAL_SLOADScaling(b *testing.B) {
	tdb := getTestDB(b)
	allReads, allHashed, _ := getFullBlockWorkload(b)

	for _, n := range []int{1000, 5000, 10000, 50000, fullBlockSLOADs} {
		if n > len(allReads) {
			n = len(allReads)
		}
		reads := allReads[:n]
		hashed := allHashed[:n]
		al := buildStorageAccessList(reads)

		b.Run(fmt.Sprintf("sloads=%d/RawBatchIO", n), func(b *testing.B) {
			runRawBatchIO(b, tdb.rawDB, hashed, 32)
		})
		b.Run(fmt.Sprintf("sloads=%d/RawSequential", n), func(b *testing.B) {
			runRawSequential(b, tdb.rawDB, hashed)
		})
		b.Run(fmt.Sprintf("sloads=%d/GethBatchIO", n), func(b *testing.B) {
			runGethBatchIO(b, tdb, reads, al, runtime.NumCPU())
		})
		b.Run(fmt.Sprintf("sloads=%d/GethNoBatchIO", n), func(b *testing.B) {
			runNoBatchIO(b, tdb, reads)
		})
	}
}

// ---------------------------------------------------------------------------
// Thread scaling for raw batch IO
// ---------------------------------------------------------------------------

func BenchmarkBAL_RawThreadScaling(b *testing.B) {
	tdb := getTestDB(b)
	_, hashed, _ := getFullBlockWorkload(b)

	for _, threads := range []int{1, 2, 4, 8, 16, 32} {
		b.Run(fmt.Sprintf("threads=%d", threads), func(b *testing.B) {
			runRawBatchIO(b, tdb.rawDB, hashed, threads)
		})
	}
	b.Run("sequential", func(b *testing.B) {
		runRawSequential(b, tdb.rawDB, hashed)
	})
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func findPrefetcher(r Reader) (*prefetchStateReader, bool) {
	if rd, ok := r.(*reader); ok {
		if pr, ok := rd.StateReader.(*prefetchStateReader); ok {
			return pr, true
		}
	}
	return nil, false
}

func closePrefetcher(r Reader) {
	if pr, ok := findPrefetcher(r); ok {
		pr.Close()
	}
}
