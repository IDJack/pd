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
package filter

import (
	"context"
	"testing"
	"time"

	"github.com/pingcap/kvproto/pkg/metapb"
	"github.com/pingcap/kvproto/pkg/pdpb"
	"github.com/stretchr/testify/require"
	"github.com/tikv/pd/pkg/mock/mockcluster"
	"github.com/tikv/pd/server/config"
	"github.com/tikv/pd/server/core"
	"github.com/tikv/pd/server/schedule/placement"
)

func TestDistinctScoreFilter(t *testing.T) {
	re := require.New(t)
	labels := []string{"zone", "rack", "host"}
	allStores := []*core.StoreInfo{
		core.NewStoreInfoWithLabel(1, 1, map[string]string{"zone": "z1", "rack": "r1", "host": "h1"}),
		core.NewStoreInfoWithLabel(2, 1, map[string]string{"zone": "z1", "rack": "r1", "host": "h2"}),
		core.NewStoreInfoWithLabel(3, 1, map[string]string{"zone": "z1", "rack": "r2", "host": "h1"}),
		core.NewStoreInfoWithLabel(4, 1, map[string]string{"zone": "z2", "rack": "r1", "host": "h1"}),
		core.NewStoreInfoWithLabel(5, 1, map[string]string{"zone": "z2", "rack": "r2", "host": "h1"}),
		core.NewStoreInfoWithLabel(6, 1, map[string]string{"zone": "z3", "rack": "r1", "host": "h1"}),
	}

	testCases := []struct {
		stores       []uint64
		source       uint64
		target       uint64
		safeGuardRes bool
		improverRes  bool
	}{
		{[]uint64{1, 2, 3}, 1, 4, true, true},
		{[]uint64{1, 3, 4}, 1, 2, true, false},
		{[]uint64{1, 4, 6}, 4, 2, false, false},
	}
	for _, testCase := range testCases {
		var stores []*core.StoreInfo
		for _, id := range testCase.stores {
			stores = append(stores, allStores[id-1])
		}
		ls := NewLocationSafeguard("", labels, stores, allStores[testCase.source-1])
		li := NewLocationImprover("", labels, stores, allStores[testCase.source-1])
		re.Equal(testCase.safeGuardRes, ls.Target(config.NewTestOptions(), allStores[testCase.target-1]))
		re.Equal(testCase.improverRes, li.Target(config.NewTestOptions(), allStores[testCase.target-1]))
	}
}

func TestLabelConstraintsFilter(t *testing.T) {
	re := require.New(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	opt := config.NewTestOptions()
	testCluster := mockcluster.NewCluster(ctx, opt)
	store := core.NewStoreInfoWithLabel(1, 1, map[string]string{"id": "1"})

	testCases := []struct {
		key    string
		op     string
		values []string
		res    bool
	}{
		{"id", "in", []string{"1"}, true},
		{"id", "in", []string{"2"}, false},
		{"id", "in", []string{"1", "2"}, true},
		{"id", "notIn", []string{"2", "3"}, true},
		{"id", "notIn", []string{"1", "2"}, false},
		{"id", "exists", []string{}, true},
		{"_id", "exists", []string{}, false},
		{"id", "notExists", []string{}, false},
		{"_id", "notExists", []string{}, true},
	}
	for _, testCase := range testCases {
		filter := NewLabelConstaintFilter("", []placement.LabelConstraint{{Key: testCase.key, Op: placement.LabelConstraintOp(testCase.op), Values: testCase.values}})
		re.Equal(testCase.res, filter.Source(testCluster.GetOpts(), store))
	}
}

func TestRuleFitFilter(t *testing.T) {
	re := require.New(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	opt := config.NewTestOptions()
	opt.SetPlacementRuleEnabled(false)
	testCluster := mockcluster.NewCluster(ctx, opt)
	testCluster.SetLocationLabels([]string{"zone"})
	testCluster.SetEnablePlacementRules(true)
	region := core.NewRegionInfo(&metapb.Region{Peers: []*metapb.Peer{
		{StoreId: 1, Id: 1},
		{StoreId: 3, Id: 3},
		{StoreId: 5, Id: 5},
	}}, &metapb.Peer{StoreId: 1, Id: 1})

	testCases := []struct {
		storeID     uint64
		regionCount int
		labels      map[string]string
		sourceRes   bool
		targetRes   bool
	}{
		{1, 1, map[string]string{"zone": "z1"}, true, true},
		{2, 1, map[string]string{"zone": "z1"}, true, true},
		{3, 1, map[string]string{"zone": "z2"}, true, false},
		{4, 1, map[string]string{"zone": "z2"}, true, false},
		{5, 1, map[string]string{"zone": "z3"}, true, false},
		{6, 1, map[string]string{"zone": "z4"}, true, true},
	}
	// Init cluster
	for _, testCase := range testCases {
		testCluster.AddLabelsStore(testCase.storeID, testCase.regionCount, testCase.labels)
	}
	for _, testCase := range testCases {
		filter := newRuleFitFilter("", testCluster.GetBasicCluster(), testCluster.GetRuleManager(), region, 1)
		re.Equal(testCase.sourceRes, filter.Source(testCluster.GetOpts(), testCluster.GetStore(testCase.storeID)))
		re.Equal(testCase.targetRes, filter.Target(testCluster.GetOpts(), testCluster.GetStore(testCase.storeID)))
	}
}

func TestStoreStateFilter(t *testing.T) {
	re := require.New(t)
	filters := []Filter{
		&StoreStateFilter{TransferLeader: true},
		&StoreStateFilter{MoveRegion: true},
		&StoreStateFilter{TransferLeader: true, MoveRegion: true},
		&StoreStateFilter{MoveRegion: true, AllowTemporaryStates: true},
	}
	opt := config.NewTestOptions()
	store := core.NewStoreInfoWithLabel(1, 0, map[string]string{})

	type testCase struct {
		filterIdx int
		sourceRes bool
		targetRes bool
	}

	check := func(store *core.StoreInfo, testCases []testCase) {
		for _, testCase := range testCases {
			re.Equal(testCase.sourceRes, filters[testCase.filterIdx].Source(opt, store))
			re.Equal(testCase.targetRes, filters[testCase.filterIdx].Target(opt, store))
		}
	}

	store = store.Clone(core.SetLastHeartbeatTS(time.Now()))
	testCases := []testCase{
		{2, true, true},
	}
	check(store, testCases)

	// Disconn
	store = store.Clone(core.SetLastHeartbeatTS(time.Now().Add(-5 * time.Minute)))
	testCases = []testCase{
		{0, false, false},
		{1, true, false},
		{2, false, false},
		{3, true, true},
	}
	check(store, testCases)

	// Busy
	store = store.Clone(core.SetLastHeartbeatTS(time.Now())).
		Clone(core.SetStoreStats(&pdpb.StoreStats{IsBusy: true}))
	testCases = []testCase{
		{0, true, false},
		{1, false, false},
		{2, false, false},
		{3, true, true},
	}
	check(store, testCases)
}

func TestIsolationFilter(t *testing.T) {
	re := require.New(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	opt := config.NewTestOptions()
	testCluster := mockcluster.NewCluster(ctx, opt)
	testCluster.SetLocationLabels([]string{"zone", "rack", "host"})
	allStores := []struct {
		storeID     uint64
		regionCount int
		labels      map[string]string
	}{
		{1, 1, map[string]string{"zone": "z1", "rack": "r1", "host": "h1"}},
		{2, 1, map[string]string{"zone": "z1", "rack": "r1", "host": "h1"}},
		{3, 1, map[string]string{"zone": "z1", "rack": "r1", "host": "h2"}},
		{4, 1, map[string]string{"zone": "z1", "rack": "r2", "host": "h1"}},
		{5, 1, map[string]string{"zone": "z1", "rack": "r3", "host": "h1"}},
		{6, 1, map[string]string{"zone": "z2", "rack": "r1", "host": "h1"}},
		{7, 1, map[string]string{"zone": "z3", "rack": "r3", "host": "h1"}},
	}
	for _, store := range allStores {
		testCluster.AddLabelsStore(store.storeID, store.regionCount, store.labels)
	}

	testCases := []struct {
		region         *core.RegionInfo
		isolationLevel string
		sourceRes      []bool
		targetRes      []bool
	}{
		{
			core.NewRegionInfo(&metapb.Region{Peers: []*metapb.Peer{
				{Id: 1, StoreId: 1},
				{Id: 2, StoreId: 6},
			}}, &metapb.Peer{StoreId: 1, Id: 1}),
			"zone",
			[]bool{true, true, true, true, true, true, true},
			[]bool{false, false, false, false, false, false, true},
		},
		{
			core.NewRegionInfo(&metapb.Region{Peers: []*metapb.Peer{
				{Id: 1, StoreId: 1},
				{Id: 2, StoreId: 4},
				{Id: 3, StoreId: 7},
			}}, &metapb.Peer{StoreId: 1, Id: 1}),
			"rack",
			[]bool{true, true, true, true, true, true, true},
			[]bool{false, false, false, false, true, true, false},
		},
		{
			core.NewRegionInfo(&metapb.Region{Peers: []*metapb.Peer{
				{Id: 1, StoreId: 1},
				{Id: 2, StoreId: 4},
				{Id: 3, StoreId: 6},
			}}, &metapb.Peer{StoreId: 1, Id: 1}),
			"host",
			[]bool{true, true, true, true, true, true, true},
			[]bool{false, false, true, false, true, false, true},
		},
	}

	for _, testCase := range testCases {
		filter := NewIsolationFilter("", testCase.isolationLevel, testCluster.GetLocationLabels(), testCluster.GetRegionStores(testCase.region))
		for idx, store := range allStores {
			re.Equal(testCase.sourceRes[idx], filter.Source(testCluster.GetOpts(), testCluster.GetStore(store.storeID)))
			re.Equal(testCase.targetRes[idx], filter.Target(testCluster.GetOpts(), testCluster.GetStore(store.storeID)))
		}
	}
}

func TestPlacementGuard(t *testing.T) {
	re := require.New(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	opt := config.NewTestOptions()
	opt.SetPlacementRuleEnabled(false)
	testCluster := mockcluster.NewCluster(ctx, opt)
	testCluster.SetLocationLabels([]string{"zone"})
	testCluster.AddLabelsStore(1, 1, map[string]string{"zone": "z1"})
	testCluster.AddLabelsStore(2, 1, map[string]string{"zone": "z1"})
	testCluster.AddLabelsStore(3, 1, map[string]string{"zone": "z2"})
	testCluster.AddLabelsStore(4, 1, map[string]string{"zone": "z2"})
	testCluster.AddLabelsStore(5, 1, map[string]string{"zone": "z3"})
	region := core.NewRegionInfo(&metapb.Region{Peers: []*metapb.Peer{
		{StoreId: 1, Id: 1},
		{StoreId: 3, Id: 3},
		{StoreId: 5, Id: 5},
	}}, &metapb.Peer{StoreId: 1, Id: 1})
	store := testCluster.GetStore(1)

	re.IsType(NewLocationSafeguard("", []string{"zone"}, testCluster.GetRegionStores(region), store),
		NewPlacementSafeguard("", testCluster.GetOpts(), testCluster.GetBasicCluster(), testCluster.GetRuleManager(), region, store))
	testCluster.SetEnablePlacementRules(true)
	re.IsType(newRuleFitFilter("", testCluster.GetBasicCluster(), testCluster.GetRuleManager(), region, 1),
		NewPlacementSafeguard("", testCluster.GetOpts(), testCluster.GetBasicCluster(), testCluster.GetRuleManager(), region, store))
}

func BenchmarkCloneRegionTest(b *testing.B) {
	epoch := &metapb.RegionEpoch{
		ConfVer: 1,
		Version: 1,
	}
	region := core.NewRegionInfo(
		&metapb.Region{
			Id:       4,
			StartKey: []byte("x"),
			EndKey:   []byte(""),
			Peers: []*metapb.Peer{
				{Id: 108, StoreId: 4},
			},
			RegionEpoch: epoch,
		},
		&metapb.Peer{Id: 108, StoreId: 4},
		core.SetApproximateSize(50),
		core.SetApproximateKeys(20),
	)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = createRegionForRuleFit(region.GetStartKey(), region.GetEndKey(), region.GetPeers(), region.GetLeader())
	}
}
