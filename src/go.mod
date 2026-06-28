module kgai

go 1.22

require github.com/kuzudb/go-kuzu v0.11.2

require github.com/google/uuid v1.6.0

require github.com/shopspring/decimal v1.4.0 // indirect

// The native LadybugDB Go module (github.com/LadybugDB/go-ladybug) currently
// requires Go >= 1.26. Its early tags are byte-for-byte the Kuzu binding, and
// LadybugDB is an API/Cypher-compatible fork of Kuzu. We therefore build against
// the proven go-kuzu binding and isolate it behind internal/graph so swapping to
// go-ladybug later is a one-file change. fetch-libs.sh materializes a writable
// copy of the module with the prebuilt native library placed where cgo expects it.
replace github.com/kuzudb/go-kuzu => ../third_party/go-kuzu
