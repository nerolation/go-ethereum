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

package ethapi

import (
	"encoding/json"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types/bal"
	"github.com/holiman/uint256"
)

func TestMarshalBlockAccessList(t *testing.T) {
	addr := common.HexToAddress("0xaaaa")
	construction := bal.NewConstructionBlockAccessList()
	construction.StorageWrite(1, addr, common.HexToHash("0x01"), common.HexToHash("0x02"))
	construction.StorageRead(addr, common.HexToHash("0x03"))
	construction.BalanceChange(1, addr, uint256.NewInt(1000))
	construction.NonceChange(addr, 1, 1)
	construction.CodeChange(addr, 1, []byte{0x60, 0x00})
	construction.AccountRead(common.HexToAddress("0xbbbb"))

	encoded, err := json.Marshal(marshalBlockAccessList(construction.ToEncodingObj()))
	if err != nil {
		t.Fatal(err)
	}
	want := `[{"address":"0x000000000000000000000000000000000000aaaa",` +
		`"storageChanges":[{"key":"0x0000000000000000000000000000000000000000000000000000000000000001",` +
		`"changes":[{"index":"0x1","value":"0x0000000000000000000000000000000000000000000000000000000000000002"}]}],` +
		`"storageReads":["0x0000000000000000000000000000000000000000000000000000000000000003"],` +
		`"balanceChanges":[{"index":"0x1","value":"0x3e8"}],` +
		`"nonceChanges":[{"index":"0x1","value":"0x1"}],` +
		`"codeChanges":[{"index":"0x1","code":"0x6000"}]},` +
		`{"address":"0x000000000000000000000000000000000000bbbb",` +
		`"storageChanges":[],"storageReads":[],"balanceChanges":[],"nonceChanges":[],"codeChanges":[]}]`
	if string(encoded) != want {
		t.Fatalf("unexpected block access list JSON:\ngot  %s\nwant %s", encoded, want)
	}
}
