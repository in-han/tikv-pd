// Copyright 2016 TiKV Project Authors.
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

package cluster_test

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/pingcap/kvprotov2/pkg/metapb"
	"github.com/pingcap/kvprotov2/pkg/pdpb"
	"github.com/stretchr/testify/require"
	"github.com/tikv/pd/pkg/testutil"
	"github.com/tikv/pd/server/core"
	"github.com/tikv/pd/tests"
)

func TestValidRequestRegion(t *testing.T) {
	re := require.New(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cluster, err := tests.NewTestCluster(ctx, 1)
	defer cluster.Destroy()
	re.NoError(err)

	err = cluster.RunInitialServers()
	re.NoError(err)

	cluster.WaitLeader()
	leaderServer := cluster.GetServer(cluster.GetLeader())
	grpcPDClient := testutil.MustNewGrpcClient(re, leaderServer.GetAddr())
	clusterID := leaderServer.GetClusterID()
	bootstrapCluster(re, clusterID, grpcPDClient)
	rc := leaderServer.GetRaftCluster()

	r1 := core.NewRegionInfo(&metapb.Region{
		Id:       1,
		StartKey: []byte(""),
		EndKey:   []byte("a"),
		Peers: []*metapb.Peer{{
			Id:      1,
			StoreId: 1,
		}},
		RegionEpoch: &metapb.RegionEpoch{ConfVer: 2, Version: 2},
	}, &metapb.Peer{
		Id:      1,
		StoreId: 1,
	})
	err = rc.HandleRegionHeartbeat(r1)
	re.NoError(err)
	r2 := &metapb.Region{Id: 2, StartKey: []byte("a"), EndKey: []byte("b")}
	re.Error(rc.ValidRequestRegion(r2))
	r3 := &metapb.Region{Id: 1, StartKey: []byte(""), EndKey: []byte("a"), RegionEpoch: &metapb.RegionEpoch{ConfVer: 1, Version: 2}}
	re.Error(rc.ValidRequestRegion(r3))
	r4 := &metapb.Region{Id: 1, StartKey: []byte(""), EndKey: []byte("a"), RegionEpoch: &metapb.RegionEpoch{ConfVer: 2, Version: 1}}
	re.Error(rc.ValidRequestRegion(r4))
	r5 := &metapb.Region{Id: 1, StartKey: []byte(""), EndKey: []byte("a"), RegionEpoch: &metapb.RegionEpoch{ConfVer: 2, Version: 2}}
	re.NoError(rc.ValidRequestRegion(r5))
	rc.Stop()
}

func TestAskSplit(t *testing.T) {
	re := require.New(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cluster, err := tests.NewTestCluster(ctx, 1)
	defer cluster.Destroy()
	re.NoError(err)

	err = cluster.RunInitialServers()
	re.NoError(err)

	cluster.WaitLeader()
	leaderServer := cluster.GetServer(cluster.GetLeader())
	grpcPDClient := testutil.MustNewGrpcClient(re, leaderServer.GetAddr())
	clusterID := leaderServer.GetClusterID()
	bootstrapCluster(re, clusterID, grpcPDClient)
	rc := leaderServer.GetRaftCluster()
	opt := rc.GetOpts()
	opt.SetSplitMergeInterval(time.Hour)
	regions := rc.GetRegions()

	req := &pdpb.AskSplitRequest{
		Header: &pdpb.RequestHeader{
			ClusterId: clusterID,
		},
		Region: regions[0].GetMeta(),
	}

	_, err = rc.HandleAskSplit(req)
	re.NoError(err)

	req1 := &pdpb.AskBatchSplitRequest{
		Header: &pdpb.RequestHeader{
			ClusterId: clusterID,
		},
		Region:     regions[0].GetMeta(),
		SplitCount: 10,
	}

	_, err = rc.HandleAskBatchSplit(req1)
	re.NoError(err)
	// test region id whether valid
	opt.SetSplitMergeInterval(time.Duration(0))
	mergeChecker := rc.GetMergeChecker()
	mergeChecker.Check(regions[0])
	re.NoError(err)
}

func TestSuspectRegions(t *testing.T) {
	re := require.New(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cluster, err := tests.NewTestCluster(ctx, 1)
	defer cluster.Destroy()
	re.NoError(err)

	err = cluster.RunInitialServers()
	re.NoError(err)

	cluster.WaitLeader()
	leaderServer := cluster.GetServer(cluster.GetLeader())
	grpcPDClient := testutil.MustNewGrpcClient(re, leaderServer.GetAddr())
	clusterID := leaderServer.GetClusterID()
	bootstrapCluster(re, clusterID, grpcPDClient)
	rc := leaderServer.GetRaftCluster()
	opt := rc.GetOpts()
	opt.SetSplitMergeInterval(time.Hour)
	regions := rc.GetRegions()

	req := &pdpb.AskBatchSplitRequest{
		Header: &pdpb.RequestHeader{
			ClusterId: clusterID,
		},
		Region:     regions[0].GetMeta(),
		SplitCount: 2,
	}
	res, err := rc.HandleAskBatchSplit(req)
	re.NoError(err)
	ids := []uint64{regions[0].GetMeta().GetId(), res.Ids[0].NewRegionId, res.Ids[1].NewRegionId}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	suspects := rc.GetSuspectRegions()
	sort.Slice(suspects, func(i, j int) bool { return suspects[i] < suspects[j] })
	re.Equal(ids, suspects)
}
