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

package statistics

import (
	"math/rand"
	"sort"
	"testing"
	"time"

	"github.com/docker/go-units"
	"github.com/pingcap/kvprotov2/pkg/metapb"
	"github.com/stretchr/testify/require"
	"github.com/tikv/pd/pkg/movingaverage"
	"github.com/tikv/pd/server/core"
)

func TestStoreTimeUnsync(t *testing.T) {
	re := require.New(t)
	cache := NewHotPeerCache(Write)
	intervals := []uint64{120, 60}
	for _, interval := range intervals {
		region := buildRegion(Write, 3, interval)
		checkAndUpdate(re, cache, region, 3)
		{
			stats := cache.RegionStats(0)
			re.Len(stats, 3)
			for _, s := range stats {
				re.Len(s, 1)
			}
		}
	}
}

type operator int

const (
	transferLeader operator = iota
	movePeer
	addReplica
	removeReplica
)

type testCacheCase struct {
	kind       RWType
	operator   operator
	expect     int
	actionType ActionType
}

func TestCache(t *testing.T) {
	re := require.New(t)
	tests := []*testCacheCase{
		{Read, transferLeader, 3, Update},
		{Read, movePeer, 4, Remove},
		{Read, addReplica, 4, Update},
		{Write, transferLeader, 3, Remove},
		{Write, movePeer, 4, Remove},
		{Write, addReplica, 4, Remove},
	}
	for _, test := range tests {
		defaultSize := map[RWType]int{
			Read:  3, // all peers
			Write: 3, // all peers
		}
		cache := NewHotPeerCache(test.kind)
		region := buildRegion(test.kind, 3, 60)
		checkAndUpdate(re, cache, region, defaultSize[test.kind])
		checkHit(re, cache, region, test.kind, Add) // all peers are new

		srcStore, region := schedule(re, test.operator, region, 10)
		res := checkAndUpdate(re, cache, region, test.expect)
		checkHit(re, cache, region, test.kind, Update) // hit cache
		if test.expect != defaultSize[test.kind] {
			checkOp(re, res, srcStore, test.actionType)
		}
	}
}

func orderingPeers(cache *hotPeerCache, region *core.RegionInfo) []*metapb.Peer {
	var peers []*metapb.Peer
	for _, peer := range region.GetPeers() {
		if cache.getOldHotPeerStat(region.GetID(), peer.StoreId) != nil {
			peers = append([]*metapb.Peer{peer}, peers...)
		} else {
			peers = append(peers, peer)
		}
	}
	return peers
}

func checkFlow(cache *hotPeerCache, region *core.RegionInfo, peers []*metapb.Peer) (res []*HotPeerStat) {
	reportInterval := region.GetInterval()
	interval := reportInterval.GetEndTimestamp() - reportInterval.GetStartTimestamp()
	res = append(res, cache.collectExpiredItems(region)...)
	for _, peer := range peers {
		peerInfo := core.NewPeerInfo(peer, region.GetLoads(), interval)
		item := cache.checkPeerFlow(peerInfo, region)
		if item != nil {
			res = append(res, item)
		}
	}
	return res
}

func updateFlow(cache *hotPeerCache, res []*HotPeerStat) []*HotPeerStat {
	for _, p := range res {
		cache.updateStat(p)
	}
	return res
}

type check func(re *require.Assertions, cache *hotPeerCache, region *core.RegionInfo, expect ...int) (res []*HotPeerStat)

func checkAndUpdate(re *require.Assertions, cache *hotPeerCache, region *core.RegionInfo, expect ...int) (res []*HotPeerStat) {
	res = checkFlow(cache, region, region.GetPeers())
	if len(expect) != 0 {
		re.Len(res, expect[0])
	}
	return updateFlow(cache, res)
}

// Check and update peers in the specified order that old item that he items that have not expired come first, and the items that have expired come second.
// This order is also similar to the previous version. By the way the order in now version is random.
func checkAndUpdateWithOrdering(re *require.Assertions, cache *hotPeerCache, region *core.RegionInfo, expect ...int) (res []*HotPeerStat) {
	res = checkFlow(cache, region, orderingPeers(cache, region))
	if len(expect) != 0 {
		re.Len(res, expect[0])
	}
	return updateFlow(cache, res)
}

func checkAndUpdateSkipOne(re *require.Assertions, cache *hotPeerCache, region *core.RegionInfo, expect ...int) (res []*HotPeerStat) {
	res = checkFlow(cache, region, region.GetPeers()[1:])
	if len(expect) != 0 {
		re.Len(res, expect[0])
	}
	return updateFlow(cache, res)
}

func checkHit(re *require.Assertions, cache *hotPeerCache, region *core.RegionInfo, kind RWType, actionType ActionType) {
	var peers []*metapb.Peer
	if kind == Read {
		peers = []*metapb.Peer{region.GetLeader()}
	} else {
		peers = region.GetPeers()
	}
	for _, peer := range peers {
		item := cache.getOldHotPeerStat(region.GetID(), peer.StoreId)
		re.NotNil(item)
		re.Equal(actionType, item.actionType)
	}
}

func checkOp(re *require.Assertions, ret []*HotPeerStat, storeID uint64, actionType ActionType) {
	for _, item := range ret {
		if item.StoreID == storeID {
			re.Equal(actionType, item.actionType)
			return
		}
	}
}

// checkIntervalSum checks whether the interval sum of the peers are different.
func checkIntervalSum(cache *hotPeerCache, region *core.RegionInfo) bool {
	var intervalSums []int
	for _, peer := range region.GetPeers() {
		oldItem := cache.getOldHotPeerStat(region.GetID(), peer.StoreId)
		if oldItem != nil {
			intervalSums = append(intervalSums, int(oldItem.getIntervalSum()))
		}
	}
	sort.Ints(intervalSums)
	return intervalSums[0] != intervalSums[len(intervalSums)-1]
}

// checkIntervalSumContinuous checks whether the interval sum of the peer is continuous.
func checkIntervalSumContinuous(re *require.Assertions, intervalSums map[uint64]int, rets []*HotPeerStat, interval uint64) {
	for _, ret := range rets {
		if ret.actionType == Remove {
			delete(intervalSums, ret.StoreID)
			continue
		}
		new := int(ret.getIntervalSum() / 1000000000)
		if ret.source == direct {
			if old, ok := intervalSums[ret.StoreID]; ok {
				re.Equal((old+int(interval))%RegionHeartBeatReportInterval, new)
			}
		}
		intervalSums[ret.StoreID] = new
	}
}

func schedule(re *require.Assertions, operator operator, region *core.RegionInfo, targets ...uint64) (srcStore uint64, _ *core.RegionInfo) {
	switch operator {
	case transferLeader:
		_, newLeader := pickFollower(region)
		return region.GetLeader().StoreId, region.Clone(core.WithLeader(newLeader))
	case movePeer:
		re.Len(targets, 1)
		index, _ := pickFollower(region)
		srcStore := region.GetPeers()[index].StoreId
		region := region.Clone(core.WithAddPeer(&metapb.Peer{Id: targets[0]*10 + 1, StoreId: targets[0]}))
		region = region.Clone(core.WithRemoveStorePeer(srcStore))
		return srcStore, region
	case addReplica:
		re.Len(targets, 1)
		region := region.Clone(core.WithAddPeer(&metapb.Peer{Id: targets[0]*10 + 1, StoreId: targets[0]}))
		return 0, region
	case removeReplica:
		if len(targets) == 0 {
			index, _ := pickFollower(region)
			srcStore = region.GetPeers()[index].StoreId
		} else {
			srcStore = targets[0]
		}
		region = region.Clone(core.WithRemoveStorePeer(srcStore))
		return srcStore, region

	default:
		return 0, nil
	}
}

func pickFollower(region *core.RegionInfo) (index int, peer *metapb.Peer) {
	var dst int
	meta := region.GetMeta()

	for index, peer := range meta.Peers {
		if peer.StoreId == region.GetLeader().StoreId {
			continue
		}
		dst = index
		if rand.Intn(2) == 0 {
			break
		}
	}
	return dst, meta.Peers[dst]
}

func buildRegion(kind RWType, peerCount int, interval uint64) *core.RegionInfo {
	peers := newPeers(peerCount,
		func(i int) uint64 { return uint64(10000 + i) },
		func(i int) uint64 { return uint64(i) })
	meta := &metapb.Region{
		Id:          1000,
		Peers:       peers,
		StartKey:    []byte(""),
		EndKey:      []byte(""),
		RegionEpoch: &metapb.RegionEpoch{ConfVer: 6, Version: 6},
	}

	leader := meta.Peers[rand.Intn(3)]

	switch kind {
	case Read:
		return core.NewRegionInfo(
			meta,
			leader,
			core.SetReportInterval(interval),
			core.SetReadBytes(10*units.MiB*interval),
			core.SetReadKeys(10*units.MiB*interval),
			core.SetReadQuery(1024*interval),
		)
	case Write:
		return core.NewRegionInfo(
			meta,
			leader,
			core.SetReportInterval(interval),
			core.SetWrittenBytes(10*units.MiB*interval),
			core.SetWrittenKeys(10*units.MiB*interval),
			core.SetWrittenQuery(1024*interval),
		)
	default:
		return nil
	}
}

type genID func(i int) uint64

func newPeers(n int, pid genID, sid genID) []*metapb.Peer {
	peers := make([]*metapb.Peer, 0, n)
	for i := 1; i <= n; i++ {
		peer := &metapb.Peer{
			Id: pid(i),
		}
		peer.StoreId = sid(i)
		peers = append(peers, peer)
	}
	return peers
}

func TestUpdateHotPeerStat(t *testing.T) {
	re := require.New(t)
	cache := NewHotPeerCache(Read)
	// we statistic read peer info from store heartbeat rather than region heartbeat
	m := RegionHeartBeatReportInterval / StoreHeartBeatReportInterval

	// skip interval=0
	newItem := &HotPeerStat{actionType: Update, thresholds: []float64{0.0, 0.0, 0.0}, Kind: Read}
	newItem = cache.updateHotPeerStat(nil, newItem, nil, []float64{0.0, 0.0, 0.0}, 0)
	re.Nil(newItem)

	// new peer, interval is larger than report interval, but no hot
	newItem = &HotPeerStat{actionType: Update, thresholds: []float64{1.0, 1.0, 1.0}, Kind: Read}
	newItem = cache.updateHotPeerStat(nil, newItem, nil, []float64{0.0, 0.0, 0.0}, 10*time.Second)
	re.Nil(newItem)

	// new peer, interval is less than report interval
	newItem = &HotPeerStat{actionType: Update, thresholds: []float64{0.0, 0.0, 0.0}, Kind: Read}
	newItem = cache.updateHotPeerStat(nil, newItem, nil, []float64{60.0, 60.0, 60.0}, 4*time.Second)
	re.NotNil(newItem)
	re.Equal(0, newItem.HotDegree)
	re.Equal(0, newItem.AntiCount)
	// sum of interval is less than report interval
	oldItem := newItem
	newItem = cache.updateHotPeerStat(nil, newItem, oldItem, []float64{60.0, 60.0, 60.0}, 4*time.Second)
	re.Equal(0, newItem.HotDegree)
	re.Equal(0, newItem.AntiCount)
	// sum of interval is larger than report interval, and hot
	oldItem = newItem
	newItem = cache.updateHotPeerStat(nil, newItem, oldItem, []float64{60.0, 60.0, 60.0}, 4*time.Second)
	re.Equal(1, newItem.HotDegree)
	re.Equal(2*m, newItem.AntiCount)
	// sum of interval is less than report interval
	oldItem = newItem
	newItem = cache.updateHotPeerStat(nil, newItem, oldItem, []float64{60.0, 60.0, 60.0}, 4*time.Second)
	re.Equal(1, newItem.HotDegree)
	re.Equal(2*m, newItem.AntiCount)
	// sum of interval is larger than report interval, and hot
	oldItem = newItem
	newItem = cache.updateHotPeerStat(nil, newItem, oldItem, []float64{60.0, 60.0, 60.0}, 10*time.Second)
	re.Equal(2, newItem.HotDegree)
	re.Equal(2*m, newItem.AntiCount)
	// sum of interval is larger than report interval, and cold
	oldItem = newItem
	newItem.thresholds = []float64{10.0, 10.0, 10.0}
	newItem = cache.updateHotPeerStat(nil, newItem, oldItem, []float64{60.0, 60.0, 60.0}, 10*time.Second)
	re.Equal(1, newItem.HotDegree)
	re.Equal(2*m-1, newItem.AntiCount)
	// sum of interval is larger than report interval, and cold
	for i := 0; i < 2*m-1; i++ {
		oldItem = newItem
		newItem = cache.updateHotPeerStat(nil, newItem, oldItem, []float64{60.0, 60.0, 60.0}, 10*time.Second)
	}
	re.Less(newItem.HotDegree, 0)
	re.Equal(0, newItem.AntiCount)
	re.Equal(Remove, newItem.actionType)
}

func TestThresholdWithUpdateHotPeerStat(t *testing.T) {
	re := require.New(t)
	byteRate := minHotThresholds[RegionReadBytes] * 2
	expectThreshold := byteRate * HotThresholdRatio
	testMetrics(re, 120., byteRate, expectThreshold)
	testMetrics(re, 60., byteRate, expectThreshold)
	testMetrics(re, 30., byteRate, expectThreshold)
	testMetrics(re, 17., byteRate, expectThreshold)
	testMetrics(re, 1., byteRate, expectThreshold)
}

func testMetrics(re *require.Assertions, interval, byteRate, expectThreshold float64) {
	cache := NewHotPeerCache(Read)
	storeID := uint64(1)
	re.GreaterOrEqual(byteRate, minHotThresholds[RegionReadBytes])
	for i := uint64(1); i < TopNN+10; i++ {
		var oldItem *HotPeerStat
		for {
			thresholds := cache.calcHotThresholds(storeID)
			newItem := &HotPeerStat{
				Kind:       cache.kind,
				StoreID:    storeID,
				RegionID:   i,
				actionType: Update,
				thresholds: thresholds,
				Loads:      make([]float64, DimLen),
			}
			newItem.Loads[RegionReadBytes] = byteRate
			newItem.Loads[RegionReadKeys] = 0
			oldItem = cache.getOldHotPeerStat(i, storeID)
			if oldItem != nil && oldItem.rollingLoads[RegionReadBytes].isHot(thresholds[RegionReadBytes]) == true {
				break
			}
			item := cache.updateHotPeerStat(nil, newItem, oldItem, []float64{byteRate * interval, 0.0, 0.0}, time.Duration(interval)*time.Second)
			cache.updateStat(item)
		}
		thresholds := cache.calcHotThresholds(storeID)
		if i < TopNN {
			re.Equal(minHotThresholds[RegionReadBytes], thresholds[RegionReadBytes])
		} else {
			re.Equal(expectThreshold, thresholds[RegionReadBytes])
		}
	}
}

func TestRemoveFromCache(t *testing.T) {
	re := require.New(t)
	peerCount := 3
	interval := uint64(5)
	checkers := []check{checkAndUpdate, checkAndUpdateWithOrdering}
	for _, checker := range checkers {
		cache := NewHotPeerCache(Write)
		region := buildRegion(Write, peerCount, interval)
		// prepare
		intervalSums := make(map[uint64]int)
		for i := 1; i <= 200; i++ {
			rets := checker(re, cache, region)
			checkIntervalSumContinuous(re, intervalSums, rets, interval)
		}
		// make the interval sum of peers are different
		checkAndUpdateSkipOne(re, cache, region)
		checkIntervalSum(cache, region)
		// check whether cold cache is cleared
		var isClear bool
		intervalSums = make(map[uint64]int)
		region = region.Clone(core.SetWrittenBytes(0), core.SetWrittenKeys(0), core.SetWrittenQuery(0))
		for i := 1; i <= 200; i++ {
			rets := checker(re, cache, region)
			checkIntervalSumContinuous(re, intervalSums, rets, interval)
			if len(cache.storesOfRegion[region.GetID()]) == 0 {
				isClear = true
				break
			}
		}
		re.True(isClear)
	}
}

func TestRemoveFromCacheRandom(t *testing.T) {
	re := require.New(t)
	peerCounts := []int{3, 5}
	intervals := []uint64{120, 60, 10, 5}
	checkers := []check{checkAndUpdate, checkAndUpdateWithOrdering}
	for _, peerCount := range peerCounts {
		for _, interval := range intervals {
			for _, checker := range checkers {
				cache := NewHotPeerCache(Write)
				region := buildRegion(Write, peerCount, interval)

				target := uint64(10)
				intervalSums := make(map[uint64]int)
				step := func(i int) {
					tmp := uint64(0)
					if i%5 == 0 {
						tmp, region = schedule(re, removeReplica, region)
					}
					rets := checker(re, cache, region)
					checkIntervalSumContinuous(re, intervalSums, rets, interval)
					if i%5 == 0 {
						_, region = schedule(re, addReplica, region, target)
						target = tmp
					}
				}

				// prepare with random move peer to make the interval sum of peers are different
				for i := 1; i < 150; i++ {
					step(i)
					if i > 150 && checkIntervalSum(cache, region) {
						break
					}
				}
				if interval < RegionHeartBeatReportInterval {
					re.True(checkIntervalSum(cache, region))
				}
				re.Len(cache.storesOfRegion[region.GetID()], peerCount)

				// check whether cold cache is cleared
				var isClear bool
				intervalSums = make(map[uint64]int)
				region = region.Clone(core.SetWrittenBytes(0), core.SetWrittenKeys(0), core.SetWrittenQuery(0))
				for i := 1; i < 200; i++ {
					step(i)
					if len(cache.storesOfRegion[region.GetID()]) == 0 {
						isClear = true
						break
					}
				}
				re.True(isClear)
			}
		}
	}
}

func checkCoolDown(re *require.Assertions, cache *hotPeerCache, region *core.RegionInfo, expect bool) {
	item := cache.getOldHotPeerStat(region.GetID(), region.GetLeader().GetStoreId())
	re.Equal(expect, item.IsNeedCoolDownTransferLeader(3))
}

func TestCoolDownTransferLeader(t *testing.T) {
	re := require.New(t)
	cache := NewHotPeerCache(Read)
	region := buildRegion(Read, 3, 60)

	moveLeader := func() {
		_, region = schedule(re, movePeer, region, 10)
		checkAndUpdate(re, cache, region)
		checkCoolDown(re, cache, region, false)
		_, region = schedule(re, transferLeader, region, 10)
		checkAndUpdate(re, cache, region)
		checkCoolDown(re, cache, region, true)
	}
	transferLeader := func() {
		_, region = schedule(re, transferLeader, region)
		checkAndUpdate(re, cache, region)
		checkCoolDown(re, cache, region, true)
	}
	movePeer := func() {
		_, region = schedule(re, movePeer, region, 10)
		checkAndUpdate(re, cache, region)
		checkCoolDown(re, cache, region, false)
	}
	addReplica := func() {
		_, region = schedule(re, addReplica, region, 10)
		checkAndUpdate(re, cache, region)
		checkCoolDown(re, cache, region, false)
	}
	removeReplica := func() {
		_, region = schedule(re, removeReplica, region, 10)
		checkAndUpdate(re, cache, region)
		checkCoolDown(re, cache, region, false)
	}
	testCases := []func(){moveLeader, transferLeader, movePeer, addReplica, removeReplica}
	for _, testCase := range testCases {
		cache = NewHotPeerCache(Read)
		region = buildRegion(Read, 3, 60)
		for i := 1; i <= 200; i++ {
			checkAndUpdate(re, cache, region)
		}
		checkCoolDown(re, cache, region, false)
		testCase()
	}
}

// See issue #4510
func TestCacheInherit(t *testing.T) {
	re := require.New(t)
	cache := NewHotPeerCache(Read)
	region := buildRegion(Read, 3, 10)
	// prepare
	for i := 1; i <= 200; i++ {
		checkAndUpdate(re, cache, region)
	}
	// move peer
	newStoreID := uint64(10)
	_, region = schedule(re, addReplica, region, newStoreID)
	checkAndUpdate(re, cache, region)
	newStoreID, region = schedule(re, removeReplica, region)
	rets := checkAndUpdate(re, cache, region)
	for _, ret := range rets {
		if ret.actionType != Remove {
			flow := ret.GetLoads()[RegionReadBytes]
			re.Equal(float64(region.GetBytesRead()/ReadReportInterval), flow)
		}
	}
	// new flow
	newFlow := region.GetBytesRead() * 10
	region = region.Clone(core.SetReadBytes(newFlow))
	for i := 1; i <= 200; i++ {
		checkAndUpdate(re, cache, region)
	}
	// move peer
	_, region = schedule(re, addReplica, region, newStoreID)
	checkAndUpdate(re, cache, region)
	_, region = schedule(re, removeReplica, region)
	rets = checkAndUpdate(re, cache, region)
	for _, ret := range rets {
		if ret.actionType != Remove {
			flow := ret.GetLoads()[RegionReadBytes]
			re.Equal(float64(newFlow/ReadReportInterval), flow)
		}
	}
}

type testMovingAverageCase struct {
	report []float64
	expect []float64
}

func checkMovingAverage(re *require.Assertions, testCase *testMovingAverageCase) {
	interval := 1 * time.Second
	tm := movingaverage.NewTimeMedian(DefaultAotSize, DefaultWriteMfSize, interval)
	var results []float64
	for _, data := range testCase.report {
		tm.Add(data, interval)
		results = append(results, tm.Get())
	}
	re.Equal(testCase.expect, results)
}

func TestUnstableData(t *testing.T) {
	re := require.New(t)
	testCases := []*testMovingAverageCase{
		{
			report: []float64{1, 1, 1, 1, 1, 1, 1, 1, 1, 1},
			expect: []float64{1, 1, 1, 1, 1, 1, 1, 1, 1, 1},
		},
		{
			report: []float64{0, 0, 0, 0, 0, 1, 0, 0, 0, 0},
			expect: []float64{0, 0, 0, 0, 0, 0, 0, 0, 0, 0},
		},
		{
			report: []float64{0, 0, 0, 0, 0, 1, 1, 0, 0, 0},
			expect: []float64{0, 0, 0, 0, 0, 0, 0, 0, 0, 0},
		},
		{
			report: []float64{0, 0, 0, 0, 1, 1, 1, 0, 0, 0},
			expect: []float64{0, 0, 0, 0, 0, 0, 1, 1, 1, 0},
		},
		{
			report: []float64{0, 0, 0, 0, 0, 1, 0, 1, 0, 0},
			expect: []float64{0, 0, 0, 0, 0, 0, 0, 0, 0, 0},
		},
		{
			report: []float64{0, 0, 0, 0, 0, 1, 0, 1, 0, 1},
			expect: []float64{0, 0, 0, 0, 0, 0, 0, 0, 0, 1},
		},
	}
	for _, testCase := range testCases {
		checkMovingAverage(re, testCase)
	}
}

func BenchmarkCheckRegionFlow(b *testing.B) {
	cache := NewHotPeerCache(Read)
	region := buildRegion(Read, 3, 10)
	peerInfos := make([]*core.PeerInfo, 0)
	for _, peer := range region.GetPeers() {
		peerInfo := core.NewPeerInfo(peer, region.GetLoads(), 10)
		peerInfos = append(peerInfos, peerInfo)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		items := make([]*HotPeerStat, 0)
		for _, peerInfo := range peerInfos {
			item := cache.checkPeerFlow(peerInfo, region)
			if item != nil {
				items = append(items, item)
			}
		}
		for _, ret := range items {
			cache.updateStat(ret)
		}
	}
}
