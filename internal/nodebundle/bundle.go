// Package nodebundle 内嵌同版本 Linux amd64 节点程序，make node-bundle 可重建。
package nodebundle

import (
	"bytes"
	"compress/gzip"
	_ "embed"
	"io"
)

//go:embed nbpass-node-linux-amd64.gz
var compressed []byte

func Open() (io.ReadCloser, error) { return gzip.NewReader(bytes.NewReader(compressed)) }
