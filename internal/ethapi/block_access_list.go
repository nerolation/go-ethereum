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
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types/bal"
)

// RPCStorageChange is one transaction's write to a storage slot.
type RPCStorageChange struct {
	Index hexutil.Uint64 `json:"index"`
	Value common.Hash    `json:"value"`
}

// RPCSlotChanges aggregates all per-transaction writes to a single storage slot.
type RPCSlotChanges struct {
	Key     common.Hash         `json:"key"`
	Changes []*RPCStorageChange `json:"changes"`
}

// RPCBalanceChange is one transaction's post-state balance for an account.
type RPCBalanceChange struct {
	Index hexutil.Uint64 `json:"index"`
	Value *hexutil.Big   `json:"value"`
}

// RPCNonceChange is one transaction's post-state nonce for an account.
type RPCNonceChange struct {
	Index hexutil.Uint64 `json:"index"`
	Value hexutil.Uint64 `json:"value"`
}

// RPCCodeChange is one transaction's deployed runtime bytecode for an account.
type RPCCodeChange struct {
	Index hexutil.Uint64 `json:"index"`
	Code  hexutil.Bytes  `json:"code"`
}

// RPCAccountAccess is the eth_getBlockAccessList response element for a single
// account of an EIP-7928 block access list.
type RPCAccountAccess struct {
	Address        common.Address      `json:"address"`
	StorageChanges []*RPCSlotChanges   `json:"storageChanges"`
	StorageReads   []common.Hash       `json:"storageReads"`
	BalanceChanges []*RPCBalanceChange `json:"balanceChanges"`
	NonceChanges   []*RPCNonceChange   `json:"nonceChanges"`
	CodeChanges    []*RPCCodeChange    `json:"codeChanges"`
}

// marshalBlockAccessList converts a block access list into the JSON-RPC
// response format, preserving the ordering of the stored list.
func marshalBlockAccessList(list *bal.BlockAccessList) []*RPCAccountAccess {
	result := make([]*RPCAccountAccess, 0, len(*list))
	for _, account := range *list {
		entry := &RPCAccountAccess{
			Address:        account.Address,
			StorageChanges: make([]*RPCSlotChanges, 0, len(account.StorageChanges)),
			StorageReads:   make([]common.Hash, 0, len(account.StorageReads)),
			BalanceChanges: make([]*RPCBalanceChange, 0, len(account.BalanceChanges)),
			NonceChanges:   make([]*RPCNonceChange, 0, len(account.NonceChanges)),
			CodeChanges:    make([]*RPCCodeChange, 0, len(account.CodeChanges)),
		}
		for _, slot := range account.StorageChanges {
			changes := make([]*RPCStorageChange, 0, len(slot.SlotChanges))
			for _, write := range slot.SlotChanges {
				changes = append(changes, &RPCStorageChange{
					Index: hexutil.Uint64(write.BlockAccessIndex),
					Value: common.Hash(write.PostValue.Bytes32()),
				})
			}
			entry.StorageChanges = append(entry.StorageChanges, &RPCSlotChanges{
				Key:     common.Hash(slot.Slot.Bytes32()),
				Changes: changes,
			})
		}
		for _, slot := range account.StorageReads {
			entry.StorageReads = append(entry.StorageReads, common.Hash(slot.Bytes32()))
		}
		for _, change := range account.BalanceChanges {
			entry.BalanceChanges = append(entry.BalanceChanges, &RPCBalanceChange{
				Index: hexutil.Uint64(change.BlockAccessIndex),
				Value: (*hexutil.Big)(change.PostBalance.ToBig()),
			})
		}
		for _, change := range account.NonceChanges {
			entry.NonceChanges = append(entry.NonceChanges, &RPCNonceChange{
				Index: hexutil.Uint64(change.BlockAccessIndex),
				Value: hexutil.Uint64(change.PostNonce),
			})
		}
		for _, change := range account.CodeChanges {
			entry.CodeChanges = append(entry.CodeChanges, &RPCCodeChange{
				Index: hexutil.Uint64(change.BlockAccessIndex),
				Code:  change.NewCode,
			})
		}
		result = append(result, entry)
	}
	return result
}
