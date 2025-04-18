// Copyright 2023 The go-ethereum Authors
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
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/metrics"
)

var (
	// Transaction timing metrics
	txTotalTimeHistogram        = metrics.NewHistogram(metrics.NewExpDecaySample(1028, 0.015))
	txIOTimeHistogram           = metrics.NewHistogram(metrics.NewExpDecaySample(1028, 0.015))
	txEVMExecutionTimeHistogram = metrics.NewHistogram(metrics.NewExpDecaySample(1028, 0.015))

	// Register the metrics
	_ = metrics.GetOrRegisterHistogram("tx/timing/total", nil, txTotalTimeHistogram)
	_ = metrics.GetOrRegisterHistogram("tx/timing/io", nil, txIOTimeHistogram)
	_ = metrics.GetOrRegisterHistogram("tx/timing/evm", nil, txEVMExecutionTimeHistogram)
)

// TransactionTiming tracks the execution time metrics for a transaction
type TransactionTiming struct {
	TxHash          common.Hash
	StartTime       time.Time
	IOStartTime     time.Time
	IOEndTime       time.Time
	EVMStartTime    time.Time
	EVMEndTime      time.Time
	totalDuration   time.Duration
	ioDuration      time.Duration
	evmDuration     time.Duration
	ioMeasureActive bool
	evmMeasureActive bool
}

// NewTransactionTiming creates a new transaction timing tracker
func NewTransactionTiming(txHash common.Hash) *TransactionTiming {
	return &TransactionTiming{
		TxHash:          txHash,
		StartTime:       time.Now(),
		ioMeasureActive: false,
		evmMeasureActive: false,
	}
}

// StartIOTimer begins tracking IO operations time
func (tt *TransactionTiming) StartIOTimer() {
	if tt.ioMeasureActive {
		return // Avoid nested calls
	}
	tt.IOStartTime = time.Now()
	tt.ioMeasureActive = true
}

// StopIOTimer stops tracking IO operations time
func (tt *TransactionTiming) StopIOTimer() {
	if !tt.ioMeasureActive {
		return
	}
	tt.IOEndTime = time.Now()
	tt.ioDuration += tt.IOEndTime.Sub(tt.IOStartTime)
	tt.ioMeasureActive = false
}

// StartEVMExecutionTimer begins tracking EVM execution time
func (tt *TransactionTiming) StartEVMExecutionTimer() {
	if tt.evmMeasureActive {
		return // Avoid nested calls
	}
	tt.EVMStartTime = time.Now()
	tt.evmMeasureActive = true
}

// StopEVMExecutionTimer stops tracking EVM execution time
func (tt *TransactionTiming) StopEVMExecutionTimer() {
	if !tt.evmMeasureActive {
		return
	}
	tt.EVMEndTime = time.Now()
	tt.evmDuration += tt.EVMEndTime.Sub(tt.EVMStartTime)
	tt.evmMeasureActive = false
}

// Finalize completes the timing measurements and updates metrics
func (tt *TransactionTiming) Finalize() {
	// Make sure any active timers are stopped
	if tt.ioMeasureActive {
		tt.StopIOTimer()
	}
	if tt.evmMeasureActive {
		tt.StopEVMExecutionTimer()
	}

	// Calculate total duration
	endTime := time.Now()
	tt.totalDuration = endTime.Sub(tt.StartTime)

	// Record metrics if enabled
	if metrics.Enabled() {
		txTotalTimeHistogram.Update(tt.totalDuration.Milliseconds())
		txIOTimeHistogram.Update(tt.ioDuration.Milliseconds())
		txEVMExecutionTimeHistogram.Update(tt.evmDuration.Milliseconds())
	}

	// Log transaction timing information
	if metrics.Enabled() {
		log.Debug("Transaction timing metrics", 
			"txHash", tt.TxHash.Hex(),
			"totalTime", tt.totalDuration.Milliseconds(),
			"ioTime", tt.ioDuration.Milliseconds(),
			"evmTime", tt.evmDuration.Milliseconds())
	}
}

// GetTotalDuration returns the total transaction execution time in milliseconds
func (tt *TransactionTiming) GetTotalDuration() int64 {
	return tt.totalDuration.Milliseconds()
}

// GetIODuration returns the IO operations time in milliseconds
func (tt *TransactionTiming) GetIODuration() int64 {
	return tt.ioDuration.Milliseconds()
}

// GetEVMDuration returns the EVM execution time in milliseconds
func (tt *TransactionTiming) GetEVMDuration() int64 {
	return tt.evmDuration.Milliseconds()
} 