// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package upstream

import (
	"encoding/json"
	"errors"
	"io"
)

const maxDiscoveryResponseBytes = 16 << 20

// decodeDiscoveryResponse rejects a partial or oversized dependency response
// before allowing it to replace the serving target set.
func decodeDiscoveryResponse(reader io.Reader, dst any) error {
	data, err := io.ReadAll(io.LimitReader(reader, maxDiscoveryResponseBytes+1))
	if err != nil {
		return err
	}
	if len(data) > maxDiscoveryResponseBytes {
		return errors.New("discovery response exceeds 16 MiB")
	}
	return json.Unmarshal(data, dst)
}
