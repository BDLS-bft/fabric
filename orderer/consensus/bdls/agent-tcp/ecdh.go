/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/
package agent

import (
	"crypto/ecdsa"
	"math/big"
)

func ECDH(publicKey *ecdsa.PublicKey, key *ecdsa.PrivateKey) *big.Int {
	secret, _ := key.Curve.ScalarMult(publicKey.X, publicKey.Y, key.D.Bytes())
	return secret
}
