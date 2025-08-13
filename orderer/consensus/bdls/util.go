package bdls

import (
	"bytes"
	"encoding/pem"
	"errors"
)

func trimLeadingZeros(b []byte) []byte {
    return bytes.TrimLeft(b, "\x00")
}

func pemDecodeIfPEM(b []byte) ([]byte, []byte) {
    blk, rest := pem.Decode(b)
    if blk == nil {
        return nil, nil
    }
    return blk.Bytes, rest
}

func pemToDER(b []byte) ([]byte, error) {
    blk, _ := pem.Decode(b)
    if blk == nil {
        return nil, errors.New("invalid PEM block")
    }
    return blk.Bytes, nil
}
