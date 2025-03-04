// Copyright 2018 TiKV Project Authors.
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

package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/pingcap/kvprotov2/pkg/metapb"
	"github.com/stretchr/testify/suite"
	tu "github.com/tikv/pd/pkg/testutil"
	"github.com/tikv/pd/server"
	"github.com/tikv/pd/server/core"
)

type adminTestSuite struct {
	suite.Suite
	svr       *server.Server
	cleanup   cleanUpFunc
	urlPrefix string
}

func TestAdminTestSuite(t *testing.T) {
	suite.Run(t, new(adminTestSuite))
}

func (suite *adminTestSuite) SetupSuite() {
	re := suite.Require()
	suite.svr, suite.cleanup = mustNewServer(re)
	server.MustWaitLeader(re, []*server.Server{suite.svr})

	addr := suite.svr.GetAddr()
	suite.urlPrefix = fmt.Sprintf("%s%s/api/v1", addr, apiPrefix)

	mustBootstrapCluster(re, suite.svr)
}

func (suite *adminTestSuite) TearDownSuite() {
	suite.cleanup()
}

func (suite *adminTestSuite) TestDropRegion() {
	cluster := suite.svr.GetRaftCluster()

	// Update region's epoch to (100, 100).
	region := cluster.GetRegionByKey([]byte("foo")).Clone(
		core.SetRegionConfVer(100),
		core.SetRegionVersion(100),
	)
	region = region.Clone(core.WithLeader(&metapb.Peer{Id: 109, StoreId: 2}), core.SetPeers([]*metapb.Peer{
		{
			Id: 109, StoreId: 2,
		},
	}))
	err := cluster.HandleRegionHeartbeat(region)
	suite.NoError(err)

	// Region epoch cannot decrease.
	region = region.Clone(
		core.SetRegionConfVer(50),
		core.SetRegionVersion(50),
	)
	err = cluster.HandleRegionHeartbeat(region)
	suite.Error(err)

	// After drop region from cache, lower version is accepted.
	url := fmt.Sprintf("%s/admin/cache/region/%d", suite.urlPrefix, region.GetID())
	req, err := http.NewRequest(http.MethodDelete, url, nil)
	suite.NoError(err)
	res, err := testDialClient.Do(req)
	suite.NoError(err)
	suite.Equal(http.StatusOK, res.StatusCode)
	res.Body.Close()
	err = cluster.HandleRegionHeartbeat(region)
	suite.NoError(err)

	region = cluster.GetRegionByKey([]byte("foo"))
	suite.Equal(uint64(50), region.GetRegionEpoch().ConfVer)
	suite.Equal(uint64(50), region.GetRegionEpoch().Version)
}

func (suite *adminTestSuite) TestDropRegions() {
	cluster := suite.svr.GetRaftCluster()

	n := uint64(10000)
	np := uint64(3)

	regions := make([]*core.RegionInfo, 0, n)
	for i := uint64(0); i < n; i++ {
		peers := make([]*metapb.Peer, 0, np)
		for j := uint64(0); j < np; j++ {
			peer := &metapb.Peer{
				Id: i*np + j,
			}
			peer.StoreId = (i + j) % n
			peers = append(peers, peer)
		}
		// initialize region's epoch to (100, 100).
		region := cluster.GetRegionByKey([]byte(fmt.Sprintf("%d", i))).Clone(
			core.SetPeers(peers),
			core.SetRegionConfVer(100),
			core.SetRegionVersion(100),
		)
		regions = append(regions, region)

		err := cluster.HandleRegionHeartbeat(region)
		suite.NoError(err)
	}

	// Region epoch cannot decrease.
	for i := uint64(0); i < n; i++ {
		region := regions[i].Clone(
			core.SetRegionConfVer(50),
			core.SetRegionVersion(50),
		)
		regions[i] = region
		err := cluster.HandleRegionHeartbeat(region)
		suite.Error(err)
	}

	for i := uint64(0); i < n; i++ {
		region := cluster.GetRegionByKey([]byte(fmt.Sprintf("%d", i)))

		suite.Equal(uint64(100), region.GetRegionEpoch().ConfVer)
		suite.Equal(uint64(100), region.GetRegionEpoch().Version)
	}

	// After drop all regions from cache, lower version is accepted.
	url := fmt.Sprintf("%s/admin/cache/regions", suite.urlPrefix)
	req, err := http.NewRequest(http.MethodDelete, url, nil)
	suite.NoError(err)
	res, err := testDialClient.Do(req)
	suite.NoError(err)
	suite.Equal(http.StatusOK, res.StatusCode)
	res.Body.Close()

	for _, region := range regions {
		err := cluster.HandleRegionHeartbeat(region)
		suite.NoError(err)
	}

	for i := uint64(0); i < n; i++ {
		region := cluster.GetRegionByKey([]byte(fmt.Sprintf("%d", i)))

		suite.Equal(uint64(50), region.GetRegionEpoch().ConfVer)
		suite.Equal(uint64(50), region.GetRegionEpoch().Version)
	}
}

func (suite *adminTestSuite) TestPersistFile() {
	data := []byte("#!/bin/sh\nrm -rf /")
	re := suite.Require()
	err := tu.CheckPostJSON(testDialClient, suite.urlPrefix+"/admin/persist-file/fun.sh", data, tu.StatusNotOK(re))
	suite.NoError(err)
	data = []byte(`{"foo":"bar"}`)
	err = tu.CheckPostJSON(testDialClient, suite.urlPrefix+"/admin/persist-file/good.json", data, tu.StatusOK(re))
	suite.NoError(err)
}

func makeTS(offset time.Duration) uint64 {
	physical := time.Now().Add(offset).UnixNano() / int64(time.Millisecond)
	return uint64(physical << 18)
}

func (suite *adminTestSuite) TestResetTS() {
	args := make(map[string]interface{})
	t1 := makeTS(time.Hour)
	url := fmt.Sprintf("%s/admin/reset-ts", suite.urlPrefix)
	args["tso"] = fmt.Sprintf("%d", t1)
	values, err := json.Marshal(args)
	suite.NoError(err)
	re := suite.Require()
	err = tu.CheckPostJSON(testDialClient, url, values,
		tu.StatusOK(re),
		tu.StringEqual(re, "\"Reset ts successfully.\"\n"))
	suite.NoError(err)
	t2 := makeTS(32 * time.Hour)
	args["tso"] = fmt.Sprintf("%d", t2)
	values, err = json.Marshal(args)
	suite.NoError(err)
	err = tu.CheckPostJSON(testDialClient, url, values,
		tu.Status(re, http.StatusForbidden),
		tu.StringContain(re, "too large"))
	suite.NoError(err)

	t3 := makeTS(-2 * time.Hour)
	args["tso"] = fmt.Sprintf("%d", t3)
	values, err = json.Marshal(args)
	suite.NoError(err)
	err = tu.CheckPostJSON(testDialClient, url, values,
		tu.Status(re, http.StatusForbidden),
		tu.StringContain(re, "small"))
	suite.NoError(err)

	args["tso"] = ""
	values, err = json.Marshal(args)
	suite.NoError(err)
	err = tu.CheckPostJSON(testDialClient, url, values,
		tu.Status(re, http.StatusBadRequest),
		tu.StringEqual(re, "\"invalid tso value\"\n"))
	suite.NoError(err)

	args["tso"] = "test"
	values, err = json.Marshal(args)
	suite.NoError(err)
	err = tu.CheckPostJSON(testDialClient, url, values,
		tu.Status(re, http.StatusBadRequest),
		tu.StringEqual(re, "\"invalid tso value\"\n"))
	suite.NoError(err)
}
