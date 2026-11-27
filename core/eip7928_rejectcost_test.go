// Copyright 2026 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package core

import (
	"context"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/beacon"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/holiman/uint256"
)

var oneEth = new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)

// keccakBurner loops hashing 1 KiB of memory until it runs out of gas, so a tx
// against it spends almost its whole gas limit on real work.
//
//	JUMPDEST; PUSH2 0x0400; PUSH1 0; KECCAK256; POP; PUSH1 0; JUMP
var keccakBurner = []byte{0x5b, 0x61, 0x04, 0x00, 0x60, 0x00, 0x20, 0x50, 0x60, 0x00, 0x56}

// tamperBALReads grafts n surplus storage reads (slot keys 1..n) onto addr in
// vb's access list and recomputes the header commitment, so the block is
// self-consistent but its BAL no longer matches execution. n selects whether the
// list stays under or over the item cap.
func tamperBALReads(t *testing.T, vb *types.Block, addr common.Address, n int) *types.Block {
	t.Helper()
	al := vb.AccessList().Copy()
	acc := findAccount(al, addr)
	if acc == nil {
		t.Fatalf("account %x not present in the valid BAL", addr)
	}
	acc.StorageReads = make([]*uint256.Int, n)
	for i := range acc.StorageReads {
		acc.StorageReads[i] = uint256.NewInt(uint64(i + 1))
	}
	hash := al.Hash()
	hdr := types.CopyHeader(vb.Header())
	hdr.BlockAccessListHash = &hash
	return types.NewBlockWithHeader(hdr).WithBody(*vb.Body()).WithAccessListUnsafe(al)
}

// TestBALPhantomReadRejectCost shows that a block access list which stays under
// the item cap but declares storage reads the transactions never make is only
// rejected after the whole block has executed, whereas an over-cap list is
// rejected pre-execution for free. geth has no gas-feasibility check that would
// abort the former early.
func TestBALPhantomReadRejectCost(t *testing.T) {
	const (
		nTx      = 6
		gasPerTx = 16_000_000 // under the EIP-7825 per-tx cap
		gasLimit = 200_000_000
		capItems = gasLimit / 2000 // BALItemCost
	)
	contract := common.HexToAddress("0xbba1")
	env := newBALTestEnv(types.GenesisAlloc{contract: {Code: keccakBurner, Balance: common.Big0}})
	env.gspec.GasLimit = gasLimit
	env.gspec.Alloc[env.from] = types.Account{Balance: new(big.Int).Mul(oneEth, big.NewInt(100_000))}

	engine := beacon.New(ethash.NewFaker())
	_, blocks, _ := GenerateChainWithGenesis(env.gspec, engine, 1, func(_ int, g *BlockGen) {
		for n := range nTx {
			g.AddTx(env.tx(uint64(n), &contract, big.NewInt(0), gasPerTx, 0, nil))
		}
	})
	vb := blocks[0]
	if vb.AccessList() == nil {
		t.Fatal("expected a non-nil block access list")
	}

	ctx := context.Background()
	newRun := func() (*BlockChain, *BlockValidator) {
		bc, err := NewBlockChain(rawdb.NewMemoryDatabase(), env.gspec, engine, nil)
		if err != nil {
			t.Fatalf("new blockchain: %v", err)
		}
		return bc, NewBlockValidator(env.gspec.Config, bc)
	}
	process := func(bc *BlockChain, b *types.Block) (*ProcessResult, *state.StateDB, time.Duration, error) {
		st, err := bc.State()
		if err != nil {
			t.Fatalf("state: %v", err)
		}
		start := time.Now()
		res, perr := NewStateProcessor(bc).Process(ctx, b, st, nil, nil, vm.Config{}, nil, nil)
		return res, st, time.Since(start), perr
	}

	// Baseline: the valid block executes in full.
	bc0, _ := newRun()
	defer bc0.Stop()
	_, _, tExec, err := process(bc0, vb)
	if err != nil {
		t.Fatalf("valid block failed to process: %v", err)
	}

	// Under the cap: passes ValidateBody, so the mismatch is only caught after a
	// full execution in ValidateState.
	within := tamperBALReads(t, vb, contract, capItems-1_000)
	bc1, v1 := newRun()
	defer bc1.Stop()
	if err := v1.ValidateBody(within); err != nil {
		t.Fatalf("within-cap BAL must pass ValidateBody, got: %v", err)
	}
	res1, st1, tReject, perr := process(bc1, within)
	var rejectErr error
	if perr != nil {
		rejectErr = perr
	} else {
		if res1.GasUsed != vb.GasUsed() {
			t.Fatalf("expected full execution before reject: gas used %d != %d", res1.GasUsed, vb.GasUsed())
		}
		rejectErr = v1.ValidateState(within, st1, res1, false, nil)
	}
	if rejectErr == nil || !strings.Contains(rejectErr.Error(), "access list") {
		t.Fatalf("expected an access-list rejection, got: %v", rejectErr)
	}

	// Over the cap: rejected by ValidateBody before any execution.
	over := tamperBALReads(t, vb, contract, capItems+50_000)
	bc2, v2 := newRun()
	defer bc2.Stop()
	start := time.Now()
	bodyErr := v2.ValidateBody(over)
	tOverCap := time.Since(start)
	if bodyErr == nil || !strings.Contains(bodyErr.Error(), "size constraint") {
		t.Fatalf("expected an item-cap rejection, got: %v", bodyErr)
	}

	t.Logf("valid block execution:   %v (%d gas)", tExec, vb.GasUsed())
	t.Logf("within-cap reject:        %v (%v)", tReject, rejectErr)
	t.Logf("over-cap reject:          %v (%v)", tOverCap, bodyErr)
	if tReject < 20*tOverCap {
		t.Fatalf("within-cap reject (%v) should cost far more than the over-cap reject (%v)", tReject, tOverCap)
	}
}

// BenchmarkBALPhantomReadReject measures the cost of rejecting a within-cap
// phantom-read BAL on a near-full block: geth executes the whole block before
// the post-execution hash check rejects it.
func BenchmarkBALPhantomReadReject(b *testing.B) {
	const (
		nTx      = 11
		gasPerTx = 16_000_000
		gasLimit = 200_000_000
	)
	contract := common.HexToAddress("0xbba1")
	env := newBALTestEnv(types.GenesisAlloc{contract: {Code: keccakBurner, Balance: common.Big0}})
	env.gspec.GasLimit = gasLimit
	env.gspec.Alloc[env.from] = types.Account{Balance: new(big.Int).Mul(oneEth, big.NewInt(100_000))}

	engine := beacon.New(ethash.NewFaker())
	_, blocks, _ := GenerateChainWithGenesis(env.gspec, engine, 1, func(_ int, g *BlockGen) {
		for n := range nTx {
			g.AddTx(env.tx(uint64(n), &contract, big.NewInt(0), gasPerTx, 0, nil))
		}
	})
	within := tamperBALReads(&testing.T{}, blocks[0], contract, gasLimit/2000-1_000)

	bc, err := NewBlockChain(rawdb.NewMemoryDatabase(), env.gspec, engine, nil)
	if err != nil {
		b.Fatalf("new blockchain: %v", err)
	}
	defer bc.Stop()
	v := NewBlockValidator(env.gspec.Config, bc)
	ctx := context.Background()

	b.ReportAllocs()
	for b.Loop() {
		st, err := bc.State()
		if err != nil {
			b.Fatalf("state: %v", err)
		}
		res, perr := NewStateProcessor(bc).Process(ctx, within, st, nil, nil, vm.Config{}, nil, nil)
		rejectErr := perr
		if perr == nil {
			rejectErr = v.ValidateState(within, st, res, false, nil)
		}
		if rejectErr == nil {
			b.Fatal("phantom-read BAL must be rejected")
		}
	}
}
