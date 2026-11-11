package config

import "bytes"

// bytesReader exists so config.go reads as one piece; it keeps the yaml
// decoder construction free of an extra import there.
func bytesReader(b []byte) *bytes.Reader { return bytes.NewReader(b) }
