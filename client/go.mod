module github.com/tikv/pdv9/client

go 1.16

require (
	github.com/opentracing/opentracing-go v1.2.0
	github.com/pingcap/errors v0.11.5-0.20211224045212-9687c2b0f87c
	github.com/pingcap/failpoint v0.0.0-20210918120811-547c13e3eb00
	github.com/pingcap/kvprotov9 v0.0.0-00010101000000-000000000000
	github.com/pingcap/log v1.1.1-0.20221110025148-ca232912c9f3
	github.com/prometheus/client_golang v1.11.0
	github.com/stretchr/testify v1.8.1
	github.com/tikv/pdv9/client v0.0.0-20230209081000-162778e2ff61
	go.uber.org/goleak v1.1.11
	go.uber.org/zap v1.20.0
	google.golang.org/grpc v1.51.0
)

replace github.com/pingcap/kvprotov9 => /tmp/submodule/kvproto-5.3.0
