module kgai

go 1.24

require github.com/kuzudb/go-kuzu v0.11.2

require (
	github.com/aws/aws-sdk-go-v2 v1.32.2
	github.com/aws/aws-sdk-go-v2/config v1.28.0
	github.com/aws/aws-sdk-go-v2/service/s3 v1.66.0
	github.com/google/uuid v1.6.0
)

require (
	github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream v1.7.8 // indirect
	github.com/aws/aws-sdk-go-v2/credentials v1.17.41 // indirect
	github.com/aws/aws-sdk-go-v2/feature/ec2/imds v1.16.17 // indirect
	github.com/aws/aws-sdk-go-v2/internal/configsources v1.3.21 // indirect
	github.com/aws/aws-sdk-go-v2/internal/endpoints/v2 v2.6.21 // indirect
	github.com/aws/aws-sdk-go-v2/internal/ini v1.8.1 // indirect
	github.com/aws/aws-sdk-go-v2/internal/v4a v1.3.21 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/accept-encoding v1.12.0 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/checksum v1.4.2 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/presigned-url v1.12.2 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/s3shared v1.18.2 // indirect
	github.com/aws/aws-sdk-go-v2/service/sso v1.24.2 // indirect
	github.com/aws/aws-sdk-go-v2/service/ssooidc v1.28.2 // indirect
	github.com/aws/aws-sdk-go-v2/service/sts v1.32.2 // indirect
	github.com/aws/smithy-go v1.24.2 // indirect
	github.com/shopspring/decimal v1.4.0 // indirect
)

// The native LadybugDB Go module (github.com/LadybugDB/go-ladybug) currently
// requires Go >= 1.26. Its early tags are byte-for-byte the Kuzu binding, and
// LadybugDB is an API/Cypher-compatible fork of Kuzu. We therefore build against
// the proven go-kuzu binding and isolate it behind internal/graph so swapping to
// go-ladybug later is a one-file change. fetch-libs.sh materializes a writable
// copy of the module with the prebuilt native library placed where cgo expects it.
replace github.com/kuzudb/go-kuzu => ../third_party/go-kuzu
