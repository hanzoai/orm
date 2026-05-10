package db

// Stubs kept compilable so orm/adapter.go's type references resolve
// while the real ZAP-Node-backed implementation in _zap_archive.go
// stays disabled (it imports zap.Node from luxfi/mdns-dependent
// paths). When the zap-proto/go Node port lands, restore
// _zap_archive.go and delete this file.

type ZapBackend int

const (
	ZapSQL ZapBackend = iota
	ZapDocumentDB
	ZapKV
	ZapDatastore
)

type ZapConfig struct {
	Addr    string
	Backend ZapBackend
}
