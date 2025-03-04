// Copyright 2019 TiKV Project Authors.
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

package schedule

import (
	"testing"

	"github.com/pingcap/kvprotov2/pkg/metapb"
	"github.com/stretchr/testify/require"
	"github.com/tikv/pd/server/core"
	"github.com/tikv/pd/server/schedule/operator"
)

func TestRandBuckets(t *testing.T) {
	re := require.New(t)
	rb := NewRandBuckets()
	addOperators(rb)
	for i := 0; i < 3; i++ {
		op := rb.GetOperator()
		re.NotNil(op)
	}
	re.Nil(rb.GetOperator())
}

func addOperators(wop WaitingOperator) {
	op := operator.NewTestOperator(uint64(1), &metapb.RegionEpoch{}, operator.OpRegion, []operator.OpStep{
		operator.RemovePeer{FromStore: uint64(1)},
	}...)
	wop.PutOperator(op)
	op = operator.NewTestOperator(uint64(2), &metapb.RegionEpoch{}, operator.OpRegion, []operator.OpStep{
		operator.RemovePeer{FromStore: uint64(2)},
	}...)
	op.SetPriorityLevel(core.HighPriority)
	wop.PutOperator(op)
	op = operator.NewTestOperator(uint64(3), &metapb.RegionEpoch{}, operator.OpRegion, []operator.OpStep{
		operator.RemovePeer{FromStore: uint64(3)},
	}...)
	op.SetPriorityLevel(core.LowPriority)
	wop.PutOperator(op)
}

func TestListOperator(t *testing.T) {
	re := require.New(t)
	rb := NewRandBuckets()
	addOperators(rb)
	re.Len(rb.ListOperator(), 3)
}

func TestRandomBucketsWithMergeRegion(t *testing.T) {
	re := require.New(t)
	rb := NewRandBuckets()
	descs := []string{"merge-region", "admin-merge-region", "random-merge"}
	for j := 0; j < 100; j++ {
		// adds operators
		desc := descs[j%3]
		op := operator.NewTestOperator(uint64(1), &metapb.RegionEpoch{}, operator.OpRegion|operator.OpMerge, []operator.OpStep{
			operator.MergeRegion{
				FromRegion: &metapb.Region{
					Id:          1,
					StartKey:    []byte{},
					EndKey:      []byte{},
					RegionEpoch: &metapb.RegionEpoch{}},
				ToRegion: &metapb.Region{Id: 2,
					StartKey:    []byte{},
					EndKey:      []byte{},
					RegionEpoch: &metapb.RegionEpoch{}},
				IsPassive: false,
			},
		}...)
		op.SetDesc(desc)
		rb.PutOperator(op)
		op = operator.NewTestOperator(uint64(2), &metapb.RegionEpoch{}, operator.OpRegion|operator.OpMerge, []operator.OpStep{
			operator.MergeRegion{
				FromRegion: &metapb.Region{
					Id:          1,
					StartKey:    []byte{},
					EndKey:      []byte{},
					RegionEpoch: &metapb.RegionEpoch{}},
				ToRegion: &metapb.Region{Id: 2,
					StartKey:    []byte{},
					EndKey:      []byte{},
					RegionEpoch: &metapb.RegionEpoch{}},
				IsPassive: true,
			},
		}...)
		op.SetDesc(desc)
		rb.PutOperator(op)
		op = operator.NewTestOperator(uint64(3), &metapb.RegionEpoch{}, operator.OpRegion, []operator.OpStep{
			operator.RemovePeer{FromStore: uint64(3)},
		}...)
		op.SetDesc("testOperatorHigh")
		op.SetPriorityLevel(core.HighPriority)
		rb.PutOperator(op)

		for i := 0; i < 2; i++ {
			op := rb.GetOperator()
			re.NotNil(op)
		}
		re.Nil(rb.GetOperator())
	}
}
