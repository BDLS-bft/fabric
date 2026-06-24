/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package bdls

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFormatTraceBucketsIsDeterministic(t *testing.T) {
	formatted := formatTraceBuckets(map[string]bdlsTraceBucket{
		"RoundChange": {count: 2, signedBytes: 20, messageBytes: 10, stateBytes: 3},
		"Commit":      {count: 1, signedBytes: 7, messageBytes: 5, proofCount: 4, proofBytes: 11},
	})

	require.Equal(t, "Commit count=1 signed_bytes=7 message_bytes=5 state_bytes=0 proof_count=4 proof_bytes=11; RoundChange count=2 signed_bytes=20 message_bytes=10 state_bytes=3 proof_count=0 proof_bytes=0", formatted)
}
