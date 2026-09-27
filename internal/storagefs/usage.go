// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package storagefs

import "math/bits"

// usageFromBlocks multiplies block counts by the block size. A non-positive
// block size or a product that overflows uint64 leaves the field unavailable
// rather than reporting a wrapped or zero value.
func usageFromBlocks(availBlocks, totalBlocks uint64, blockSize int64) Usage {
	if blockSize <= 0 {
		return Usage{}
	}
	var u Usage
	u.Available, u.HaveAvailable = mulBytes(availBlocks, uint64(blockSize))
	u.Total, u.HaveTotal = mulBytes(totalBlocks, uint64(blockSize))
	return u
}

func mulBytes(blocks, size uint64) (uint64, bool) {
	hi, lo := bits.Mul64(blocks, size)
	if hi != 0 {
		return 0, false
	}
	return lo, true
}
