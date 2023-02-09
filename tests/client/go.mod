module github.com/tikv/pdv9/tests/client

go 1.16

require (
	github.com/gogo/protobuf v1.3.2
	github.com/golang/protobuf v1.5.2 // indirect
	github.com/pingcap/failpoint v0.0.0-20210918120811-547c13e3eb00
	github.com/pingcap/kvproto v0.0.0-20220510035547-0e2f26c0a46a
	github.com/stretchr/testify v1.7.0
	github.com/tikv/pdv9 v0.0.0-00010101000000-000000000000
	github.com/tikv/pdv9/client v0.0.0-00010101000000-000000000000
	go.etcd.io/etcd v0.5.0-alpha.5.0.20191023171146-3cf2f69b5738
	go.uber.org/goleak v1.1.12
	google.golang.org/grpc v1.43.0
)

replace (
	github.com/tikv/pdv9 => ../../
	github.com/tikv/pdv9/client => ../../client
)

// reset grpc and protobuf deps in order to import client and server at the same time
replace (
	github.com/golang/protobuf v1.5.2 => github.com/golang/protobuf v1.3.4
	google.golang.org/grpc v1.43.0 => google.golang.org/grpc v1.26.0
	google.golang.org/protobuf v1.26.0 => github.com/golang/protobuf v1.3.4
)
