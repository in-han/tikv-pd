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

package operator

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pingcap/kvprotov2/pkg/metapb"
	"github.com/stretchr/testify/suite"
	"github.com/tikv/pd/pkg/mock/mockcluster"
	"github.com/tikv/pd/server/config"
	"github.com/tikv/pd/server/core"
	"github.com/tikv/pd/server/core/storelimit"
)

type operatorTestSuite struct {
	suite.Suite

	cluster *mockcluster.Cluster
	ctx     context.Context
	cancel  context.CancelFunc
}

func TestOperatorTestSuite(t *testing.T) {
	suite.Run(t, new(operatorTestSuite))
}

func (suite *operatorTestSuite) SetupTest() {
	cfg := config.NewTestOptions()
	suite.ctx, suite.cancel = context.WithCancel(context.Background())
	suite.cluster = mockcluster.NewCluster(suite.ctx, cfg)
	suite.cluster.SetMaxMergeRegionSize(2)
	suite.cluster.SetMaxMergeRegionKeys(2)
	suite.cluster.SetLabelPropertyConfig(config.LabelPropertyConfig{
		config.RejectLeader: {{Key: "reject", Value: "leader"}},
	})
	stores := map[uint64][]string{
		1: {}, 2: {}, 3: {}, 4: {}, 5: {}, 6: {},
		7: {"reject", "leader"},
		8: {"reject", "leader"},
	}
	for storeID, labels := range stores {
		suite.cluster.PutStoreWithLabels(storeID, labels...)
	}
}

func (suite *operatorTestSuite) TearDownTest() {
	suite.cancel()
}

func (suite *operatorTestSuite) newTestRegion(regionID uint64, leaderPeer uint64, peers ...[2]uint64) *core.RegionInfo {
	var (
		region metapb.Region
		leader *metapb.Peer
	)
	region.Id = regionID
	for i := range peers {
		peer := &metapb.Peer{
			Id:      peers[i][1],
			StoreId: peers[i][0],
		}
		region.Peers = append(region.Peers, peer)
		if peer.GetId() == leaderPeer {
			leader = peer
		}
	}
	regionInfo := core.NewRegionInfo(&region, leader, core.SetApproximateSize(50), core.SetApproximateKeys(50))
	return regionInfo
}

func (suite *operatorTestSuite) TestOperatorStep() {
	region := suite.newTestRegion(1, 1, [2]uint64{1, 1}, [2]uint64{2, 2})
	suite.False(TransferLeader{FromStore: 1, ToStore: 2}.IsFinish(region))
	suite.True(TransferLeader{FromStore: 2, ToStore: 1}.IsFinish(region))
	suite.False(AddPeer{ToStore: 3, PeerID: 3}.IsFinish(region))
	suite.True(AddPeer{ToStore: 1, PeerID: 1}.IsFinish(region))
	suite.False(RemovePeer{FromStore: 1}.IsFinish(region))
	suite.True(RemovePeer{FromStore: 3}.IsFinish(region))
}

func (suite *operatorTestSuite) newTestOperator(regionID uint64, kind OpKind, steps ...OpStep) *Operator {
	return NewTestOperator(regionID, &metapb.RegionEpoch{}, kind, steps...)
}

func (suite *operatorTestSuite) checkSteps(op *Operator, steps []OpStep) {
	suite.Len(steps, op.Len())
	for i := range steps {
		suite.Equal(steps[i], op.Step(i))
	}
}

func (suite *operatorTestSuite) TestOperator() {
	region := suite.newTestRegion(1, 1, [2]uint64{1, 1}, [2]uint64{2, 2})
	// addPeer1, transferLeader1, removePeer3
	steps := []OpStep{
		AddPeer{ToStore: 1, PeerID: 1},
		TransferLeader{FromStore: 3, ToStore: 1},
		RemovePeer{FromStore: 3},
	}
	op := suite.newTestOperator(1, OpAdmin|OpLeader|OpRegion, steps...)
	suite.Equal(core.HighPriority, op.GetPriorityLevel())
	suite.checkSteps(op, steps)
	op.Start()
	suite.Nil(op.Check(region))

	suite.Equal(SUCCESS, op.Status())
	SetOperatorStatusReachTime(op, STARTED, time.Now().Add(-SlowOperatorWaitTime-time.Second))
	suite.False(op.CheckTimeout())

	// addPeer1, transferLeader1, removePeer2
	steps = []OpStep{
		AddPeer{ToStore: 1, PeerID: 1},
		TransferLeader{FromStore: 2, ToStore: 1},
		RemovePeer{FromStore: 2},
	}
	op = suite.newTestOperator(1, OpLeader|OpRegion, steps...)
	suite.checkSteps(op, steps)
	op.Start()
	suite.Equal(RemovePeer{FromStore: 2}, op.Check(region))
	suite.Equal(int32(2), atomic.LoadInt32(&op.currentStep))
	suite.False(op.CheckTimeout())
	SetOperatorStatusReachTime(op, STARTED, op.GetStartTime().Add(-FastOperatorWaitTime-time.Second))
	suite.False(op.CheckTimeout())
	op.stepsTime[op.currentStep-1] = op.GetReachTimeOf(STARTED).Unix()
	SetOperatorStatusReachTime(op, STARTED, op.GetStartTime().Add(-SlowOperatorWaitTime-time.Second))
	suite.True(op.CheckTimeout())
	res, err := json.Marshal(op)
	suite.NoError(err)
	suite.Len(res, len(op.String())+2)

	// check short timeout for transfer leader only operators.
	steps = []OpStep{TransferLeader{FromStore: 2, ToStore: 1}}
	op = suite.newTestOperator(1, OpLeader, steps...)
	op.Start()
	suite.False(op.CheckTimeout())
	SetOperatorStatusReachTime(op, STARTED, op.GetStartTime().Add(-FastOperatorWaitTime-time.Second))
	suite.True(op.CheckTimeout())

	// case2: check timeout operator will return false not panic.
	op = NewTestOperator(1, &metapb.RegionEpoch{}, OpRegion, TransferLeader{ToStore: 1, FromStore: 4})
	op.currentStep = 1
	suite.True(op.status.To(STARTED))
	suite.True(op.status.To(TIMEOUT))
	suite.False(op.CheckSuccess())
	suite.False(op.CheckTimeout())
}

func (suite *operatorTestSuite) TestInfluence() {
	region := suite.newTestRegion(1, 1, [2]uint64{1, 1}, [2]uint64{2, 2})
	opInfluence := OpInfluence{StoresInfluence: make(map[uint64]*StoreInfluence)}
	storeOpInfluence := opInfluence.StoresInfluence
	storeOpInfluence[1] = &StoreInfluence{}
	storeOpInfluence[2] = &StoreInfluence{}

	AddPeer{ToStore: 2, PeerID: 2}.Influence(opInfluence, region)
	suite.Equal(StoreInfluence{
		LeaderSize:  0,
		LeaderCount: 0,
		RegionSize:  50,
		RegionCount: 1,
		StepCost:    map[storelimit.Type]int64{storelimit.AddPeer: 1000},
	}, *storeOpInfluence[2])

	TransferLeader{FromStore: 1, ToStore: 2}.Influence(opInfluence, region)
	suite.Equal(StoreInfluence{
		LeaderSize:  -50,
		LeaderCount: -1,
		RegionSize:  0,
		RegionCount: 0,
		StepCost:    nil,
	}, *storeOpInfluence[1])
	suite.Equal(StoreInfluence{
		LeaderSize:  50,
		LeaderCount: 1,
		RegionSize:  50,
		RegionCount: 1,
		StepCost:    map[storelimit.Type]int64{storelimit.AddPeer: 1000},
	}, *storeOpInfluence[2])

	RemovePeer{FromStore: 1}.Influence(opInfluence, region)
	suite.Equal(StoreInfluence{
		LeaderSize:  -50,
		LeaderCount: -1,
		RegionSize:  -50,
		RegionCount: -1,
		StepCost:    map[storelimit.Type]int64{storelimit.RemovePeer: 1000},
	}, *storeOpInfluence[1])
	suite.Equal(StoreInfluence{
		LeaderSize:  50,
		LeaderCount: 1,
		RegionSize:  50,
		RegionCount: 1,
		StepCost:    map[storelimit.Type]int64{storelimit.AddPeer: 1000},
	}, *storeOpInfluence[2])

	MergeRegion{IsPassive: false}.Influence(opInfluence, region)
	suite.Equal(StoreInfluence{
		LeaderSize:  -50,
		LeaderCount: -1,
		RegionSize:  -50,
		RegionCount: -1,
		StepCost:    map[storelimit.Type]int64{storelimit.RemovePeer: 1000},
	}, *storeOpInfluence[1])
	suite.Equal(StoreInfluence{
		LeaderSize:  50,
		LeaderCount: 1,
		RegionSize:  50,
		RegionCount: 1,
		StepCost:    map[storelimit.Type]int64{storelimit.AddPeer: 1000},
	}, *storeOpInfluence[2])

	MergeRegion{IsPassive: true}.Influence(opInfluence, region)
	suite.Equal(StoreInfluence{
		LeaderSize:  -50,
		LeaderCount: -2,
		RegionSize:  -50,
		RegionCount: -2,
		StepCost:    map[storelimit.Type]int64{storelimit.RemovePeer: 1000},
	}, *storeOpInfluence[1])
	suite.Equal(StoreInfluence{
		LeaderSize:  50,
		LeaderCount: 1,
		RegionSize:  50,
		RegionCount: 0,
		StepCost:    map[storelimit.Type]int64{storelimit.AddPeer: 1000},
	}, *storeOpInfluence[2])
}

func (suite *operatorTestSuite) TestOperatorKind() {
	suite.Equal("replica,leader", (OpLeader | OpReplica).String())
	suite.Equal("unknown", OpKind(0).String())
	k, err := ParseOperatorKind("region,leader")
	suite.NoError(err)
	suite.Equal(OpRegion|OpLeader, k)
	_, err = ParseOperatorKind("leader,region")
	suite.NoError(err)
	_, err = ParseOperatorKind("foobar")
	suite.Error(err)
}

func (suite *operatorTestSuite) TestCheckSuccess() {
	{
		steps := []OpStep{
			AddPeer{ToStore: 1, PeerID: 1},
			TransferLeader{FromStore: 2, ToStore: 1},
			RemovePeer{FromStore: 2},
		}
		op := suite.newTestOperator(1, OpLeader|OpRegion, steps...)
		suite.Equal(CREATED, op.Status())
		suite.False(op.CheckSuccess())
		suite.True(op.Start())
		suite.False(op.CheckSuccess())
		op.currentStep = int32(len(op.steps))
		suite.True(op.CheckSuccess())
		suite.True(op.CheckSuccess())
	}
	{
		steps := []OpStep{
			AddPeer{ToStore: 1, PeerID: 1},
			TransferLeader{FromStore: 2, ToStore: 1},
			RemovePeer{FromStore: 2},
		}
		op := suite.newTestOperator(1, OpLeader|OpRegion, steps...)
		op.currentStep = int32(len(op.steps))
		suite.Equal(CREATED, op.Status())
		suite.False(op.CheckSuccess())
		suite.True(op.Start())
		suite.True(op.CheckSuccess())
		suite.True(op.CheckSuccess())
	}
}

func (suite *operatorTestSuite) TestCheckTimeout() {
	{
		steps := []OpStep{
			AddPeer{ToStore: 1, PeerID: 1},
			TransferLeader{FromStore: 2, ToStore: 1},
			RemovePeer{FromStore: 2},
		}
		op := suite.newTestOperator(1, OpLeader|OpRegion, steps...)
		suite.Equal(CREATED, op.Status())
		suite.True(op.Start())
		op.currentStep = int32(len(op.steps))
		suite.False(op.CheckTimeout())
		suite.Equal(SUCCESS, op.Status())
	}
	{
		steps := []OpStep{
			AddPeer{ToStore: 1, PeerID: 1},
			TransferLeader{FromStore: 2, ToStore: 1},
			RemovePeer{FromStore: 2},
		}
		op := suite.newTestOperator(1, OpLeader|OpRegion, steps...)
		suite.Equal(CREATED, op.Status())
		suite.True(op.Start())
		op.currentStep = int32(len(op.steps))
		SetOperatorStatusReachTime(op, STARTED, time.Now().Add(-SlowOperatorWaitTime))
		suite.False(op.CheckTimeout())
		suite.Equal(SUCCESS, op.Status())
	}
}

func (suite *operatorTestSuite) TestStart() {
	steps := []OpStep{
		AddPeer{ToStore: 1, PeerID: 1},
		TransferLeader{FromStore: 2, ToStore: 1},
		RemovePeer{FromStore: 2},
	}
	op := suite.newTestOperator(1, OpLeader|OpRegion, steps...)
	suite.Equal(0, op.GetStartTime().Nanosecond())
	suite.Equal(CREATED, op.Status())
	suite.True(op.Start())
	suite.NotEqual(0, op.GetStartTime().Nanosecond())
	suite.Equal(STARTED, op.Status())
}

func (suite *operatorTestSuite) TestCheckExpired() {
	steps := []OpStep{
		AddPeer{ToStore: 1, PeerID: 1},
		TransferLeader{FromStore: 2, ToStore: 1},
		RemovePeer{FromStore: 2},
	}
	op := suite.newTestOperator(1, OpLeader|OpRegion, steps...)
	suite.False(op.CheckExpired())
	suite.Equal(CREATED, op.Status())
	SetOperatorStatusReachTime(op, CREATED, time.Now().Add(-OperatorExpireTime))
	suite.True(op.CheckExpired())
	suite.Equal(EXPIRED, op.Status())
}

func (suite *operatorTestSuite) TestCheck() {
	{
		region := suite.newTestRegion(2, 2, [2]uint64{1, 1}, [2]uint64{2, 2})
		steps := []OpStep{
			AddPeer{ToStore: 1, PeerID: 1},
			TransferLeader{FromStore: 2, ToStore: 1},
			RemovePeer{FromStore: 2},
		}
		op := suite.newTestOperator(2, OpLeader|OpRegion, steps...)
		suite.True(op.Start())
		suite.NotNil(op.Check(region))

		suite.Equal(STARTED, op.Status())
		region = suite.newTestRegion(1, 1, [2]uint64{1, 1})
		suite.Nil(op.Check(region))

		suite.Equal(SUCCESS, op.Status())
	}
	{
		region := suite.newTestRegion(1, 1, [2]uint64{1, 1}, [2]uint64{2, 2})
		steps := []OpStep{
			AddPeer{ToStore: 1, PeerID: 1},
			TransferLeader{FromStore: 2, ToStore: 1},
			RemovePeer{FromStore: 2},
		}
		op := suite.newTestOperator(1, OpLeader|OpRegion, steps...)
		suite.True(op.Start())
		suite.NotNil(op.Check(region))
		suite.Equal(STARTED, op.Status())
		op.stepsTime[op.currentStep-1] = time.Now().Add(-SlowOperatorWaitTime).Unix()
		suite.NotNil(op.Check(region))
		suite.Equal(TIMEOUT, op.Status())
	}
	{
		region := suite.newTestRegion(1, 1, [2]uint64{1, 1}, [2]uint64{2, 2})
		steps := []OpStep{
			AddPeer{ToStore: 1, PeerID: 1},
			TransferLeader{FromStore: 2, ToStore: 1},
			RemovePeer{FromStore: 2},
		}
		op := suite.newTestOperator(1, OpLeader|OpRegion, steps...)
		suite.True(op.Start())
		suite.NotNil(op.Check(region))
		suite.Equal(STARTED, op.Status())
		op.status.setTime(STARTED, time.Now().Add(-SlowOperatorWaitTime))
		region = suite.newTestRegion(1, 1, [2]uint64{1, 1})
		suite.Nil(op.Check(region))
		suite.Equal(SUCCESS, op.Status())
	}
}

func (suite *operatorTestSuite) TestSchedulerKind() {
	testData := []struct {
		op     *Operator
		expect OpKind
	}{
		{
			op:     suite.newTestOperator(1, OpAdmin|OpMerge|OpRegion),
			expect: OpAdmin,
		}, {
			op:     suite.newTestOperator(1, OpMerge|OpLeader|OpRegion),
			expect: OpMerge,
		}, {
			op:     suite.newTestOperator(1, OpReplica|OpRegion),
			expect: OpReplica,
		}, {
			op:     suite.newTestOperator(1, OpSplit|OpRegion),
			expect: OpSplit,
		}, {
			op:     suite.newTestOperator(1, OpRange|OpRegion),
			expect: OpRange,
		}, {
			op:     suite.newTestOperator(1, OpHotRegion|OpLeader|OpRegion),
			expect: OpHotRegion,
		}, {
			op:     suite.newTestOperator(1, OpRegion|OpLeader),
			expect: OpRegion,
		}, {
			op:     suite.newTestOperator(1, OpLeader),
			expect: OpLeader,
		},
	}
	for _, v := range testData {
		suite.Equal(v.expect, v.op.SchedulerKind())
	}
}

func (suite *operatorTestSuite) TestOpStepTimeout() {
	testData := []struct {
		step       []OpStep
		regionSize int64
		start      time.Time
		expect     bool
	}{
		{
			// case1: 10GB region will have 60,000s to executor.
			step:       []OpStep{AddLearner{}, AddPeer{}},
			regionSize: 10 * 1000,
			start:      time.Now().Add(-(time.Second*(6*10*1000) + time.Second)),
			expect:     true,
		},
		{
			step:       []OpStep{AddLearner{}, AddPeer{}},
			regionSize: 10 * 1000,
			start:      time.Now().Add(-(time.Second*(6*10*1000) - time.Second)),
			expect:     false,
		}, {
			// case2: 10MB region will have at least SlowOperatorWaitTime(10min) to executor.
			step:       []OpStep{AddLearner{}, AddPeer{}},
			regionSize: 10,
			start:      time.Now().Add(-(SlowOperatorWaitTime + time.Second)),
			expect:     true,
		}, {
			step:       []OpStep{AddLearner{}, AddPeer{}},
			regionSize: 10,
			start:      time.Now().Add(-(time.Second*(6*10) - time.Second)),
			expect:     false,
		}, {
			// case3:  10GB region will have 1000s to executor for RemovePeer, TransferLeader, SplitRegion, PromoteLearner.
			step:       []OpStep{RemovePeer{}, TransferLeader{}, SplitRegion{}, PromoteLearner{}},
			start:      time.Now().Add(-(time.Second*(1000) + time.Second)),
			regionSize: 10 * 1000,
			expect:     true,
		}, {
			step:       []OpStep{RemovePeer{}, TransferLeader{}, SplitRegion{}, PromoteLearner{}},
			start:      time.Now().Add(-(time.Second*(1000) - time.Second)),
			regionSize: 10 * 1000,
			expect:     false,
		}, {
			// case4: 10MB will have at lease FastOperatorWaitTime(10s) to executor for RemovePeer, TransferLeader, SplitRegion, PromoteLearner.
			step:       []OpStep{RemovePeer{}, TransferLeader{}, SplitRegion{}, PromoteLearner{}},
			start:      time.Now().Add(-(FastOperatorWaitTime + time.Second)),
			regionSize: 10,
			expect:     true,
		}, {
			step:       []OpStep{RemovePeer{}, TransferLeader{}, SplitRegion{}, PromoteLearner{}},
			start:      time.Now().Add(-(FastOperatorWaitTime - time.Second)),
			regionSize: 10,
			expect:     false,
		}, {
			// case5: 10GB region will have 1000*3 for ChangePeerV2Enter, ChangePeerV2Leave.
			step: []OpStep{ChangePeerV2Enter{PromoteLearners: []PromoteLearner{{}, {}}},
				ChangePeerV2Leave{PromoteLearners: []PromoteLearner{{}, {}}}},
			start:      time.Now().Add(-(time.Second*(3000) + time.Second)),
			regionSize: 10 * 1000,
			expect:     true,
		}, {
			step: []OpStep{ChangePeerV2Enter{PromoteLearners: []PromoteLearner{{}, {}}},
				ChangePeerV2Leave{PromoteLearners: []PromoteLearner{{}, {}}}},
			start:      time.Now().Add(-(time.Second*(3000) - time.Second)),
			regionSize: 10 * 1000,
			expect:     false,
		}, {
			//case6: 10GB region will have 1000*10s for ChangePeerV2Enter, ChangePeerV2Leave.
			step:       []OpStep{MergeRegion{}},
			start:      time.Now().Add(-(time.Second*(10000) + time.Second)),
			regionSize: 10 * 1000,
			expect:     true,
		}, {
			step:       []OpStep{MergeRegion{}},
			start:      time.Now().Add(-(time.Second*(10000) - time.Second)),
			regionSize: 10 * 1000,
			expect:     false,
		},
	}
	for _, v := range testData {
		for _, step := range v.step {
			suite.Equal(step.Timeout(v.start, v.regionSize), v.expect)
		}
	}
}

func (suite *operatorTestSuite) TestRecord() {
	operator := suite.newTestOperator(1, OpLeader, AddLearner{ToStore: 1, PeerID: 1}, RemovePeer{FromStore: 1, PeerID: 1})
	now := time.Now()
	time.Sleep(time.Second)
	ob := operator.Record(now)
	suite.Equal(now, ob.FinishTime)
	suite.Greater(ob.duration.Seconds(), time.Second.Seconds())
}
