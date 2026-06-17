/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package bdls

import "github.com/hyperledger/fabric-lib-go/common/metrics"

var (
	clusterSizeOpts = metrics.GaugeOpts{
		Namespace:    "consensus",
		Subsystem:    "bdls",
		Name:         "cluster_size",
		Help:         "Number of participants in the BDLS consensus group for this channel.",
		LabelNames:   []string{"channel"},
		StatsdFormat: "%{#fqname}.%{channel}",
	}
	committedBlockNumberOpts = metrics.GaugeOpts{
		Namespace:    "consensus",
		Subsystem:    "bdls",
		Name:         "committed_block_number",
		Help:         "The number of the latest BDLS-finalised block on this channel.",
		LabelNames:   []string{"channel"},
		StatsdFormat: "%{#fqname}.%{channel}",
	}
	isLeaderOpts = metrics.GaugeOpts{
		Namespace:    "consensus",
		Subsystem:    "bdls",
		Name:         "is_leader",
		Help:         "1 if this node is the leader of the current BDLS round, else 0.",
		LabelNames:   []string{"channel"},
		StatsdFormat: "%{#fqname}.%{channel}",
	}
	leaderIDOpts = metrics.GaugeOpts{
		Namespace:    "consensus",
		Subsystem:    "bdls",
		Name:         "leader_id",
		Help:         "The participant id of the current BDLS leader for the latest committed block.",
		LabelNames:   []string{"channel"},
		StatsdFormat: "%{#fqname}.%{channel}",
	}
	proposalFailuresOpts = metrics.CounterOpts{
		Namespace:    "consensus",
		Subsystem:    "bdls",
		Name:         "proposal_failures",
		Help:         "Count of proposal submission / marshal failures surfaced by the chain run-loop.",
		LabelNames:   []string{"channel"},
		StatsdFormat: "%{#fqname}.%{channel}",
	}
)

// Metrics bundles the BDLS consenter's observable counters/gauges. The shape
// mirrors orderer/consensus/smartbft/metrics.go so dashboards built for one
// consenter translate cleanly to the other.
type Metrics struct {
	ClusterSize          metrics.Gauge
	CommittedBlockNumber metrics.Gauge
	IsLeader             metrics.Gauge
	LeaderID             metrics.Gauge
	ProposalFailures     metrics.Counter
}

// NewMetrics constructs a Metrics wired to the supplied provider.
func NewMetrics(p metrics.Provider) *Metrics {
	return &Metrics{
		ClusterSize:          p.NewGauge(clusterSizeOpts),
		CommittedBlockNumber: p.NewGauge(committedBlockNumberOpts),
		IsLeader:             p.NewGauge(isLeaderOpts),
		LeaderID:             p.NewGauge(leaderIDOpts),
		ProposalFailures:     p.NewCounter(proposalFailuresOpts),
	}
}
