// Copyright 2021 TiKV Project Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//go:build tso_full_test || tso_function_test
// +build tso_full_test tso_function_test

package tso_test

import (
	"context"
	"testing"

	"github.com/pingcap/failpoint"
	"github.com/pingcap/kvprotov2/pkg/pdpb"
	"github.com/stretchr/testify/require"
	"github.com/tikv/pd/pkg/grpcutil"
	"github.com/tikv/pd/pkg/testutil"
	"github.com/tikv/pd/server/config"
	"github.com/tikv/pd/tests"
)

func TestLoadTimestamp(t *testing.T) {
	re := require.New(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dcLocationConfig := map[string]string{
		"pd1": "dc-1",
		"pd2": "dc-2",
		"pd3": "dc-3",
	}
	dcLocationNum := len(dcLocationConfig)
	cluster, err := tests.NewTestCluster(ctx, dcLocationNum, func(conf *config.Config, serverName string) {
		conf.EnableLocalTSO = true
		conf.Labels[config.ZoneLabel] = dcLocationConfig[serverName]
	})
	defer cluster.Destroy()
	re.NoError(err)
	re.NoError(cluster.RunInitialServers())

	cluster.WaitAllLeaders(re, dcLocationConfig)

	lastTSMap := requestLocalTSOs(re, cluster, dcLocationConfig)

	re.NoError(failpoint.Enable("github.com/tikv/pd/server/tso/systemTimeSlow", `return(true)`))

	// Reboot the cluster.
	re.NoError(cluster.StopAll())
	re.NoError(cluster.RunInitialServers())

	cluster.WaitAllLeaders(re, dcLocationConfig)

	// Re-request the Local TSOs.
	newTSMap := requestLocalTSOs(re, cluster, dcLocationConfig)
	for dcLocation, newTS := range newTSMap {
		lastTS, ok := lastTSMap[dcLocation]
		re.True(ok)
		// The new physical time of TSO should be larger even if the system time is slow.
		re.Greater(newTS.GetPhysical()-lastTS.GetPhysical(), int64(0))
	}

	failpoint.Disable("github.com/tikv/pd/server/tso/systemTimeSlow")
}

func requestLocalTSOs(re *require.Assertions, cluster *tests.TestCluster, dcLocationConfig map[string]string) map[string]*pdpb.Timestamp {
	dcClientMap := make(map[string]pdpb.PDClient)
	tsMap := make(map[string]*pdpb.Timestamp)
	leaderServer := cluster.GetServer(cluster.GetLeader())
	for _, dcLocation := range dcLocationConfig {
		pdName := leaderServer.GetAllocatorLeader(dcLocation).GetName()
		dcClientMap[dcLocation] = testutil.MustNewGrpcClient(re, cluster.GetServer(pdName).GetAddr())
	}
	for _, dcLocation := range dcLocationConfig {
		req := &pdpb.TsoRequest{
			Header:     testutil.NewRequestHeader(leaderServer.GetClusterID()),
			Count:      tsoCount,
			DcLocation: dcLocation,
		}
		ctx, cancel := context.WithCancel(context.Background())
		ctx = grpcutil.BuildForwardContext(ctx, cluster.GetServer(leaderServer.GetAllocatorLeader(dcLocation).GetName()).GetAddr())
		tsMap[dcLocation] = testGetTimestamp(re, ctx, dcClientMap[dcLocation], req)
		cancel()
	}
	return tsMap
}
