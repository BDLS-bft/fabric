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
	commitPipelineDurationOpts = metrics.HistogramOpts{
		Namespace:    "consensus",
		Subsystem:    "bdls",
		Name:         "commit_pipeline_duration",
		Help:         "Time from detecting a BDLS-finalised block to completing ledger commit.",
		LabelNames:   []string{"channel"},
		StatsdFormat: "%{#fqname}.%{channel}",
	}
	consensusFinalityDurationOpts = metrics.HistogramOpts{
		Namespace:    "consensus",
		Subsystem:    "bdls",
		Name:         "consensus_finality_duration",
		Help:         "Time from submitting a local BDLS proposal to observing the matching BDLS decision.",
		LabelNames:   []string{"channel"},
		StatsdFormat: "%{#fqname}.%{channel}",
	}
	blockSignatureDurationOpts = metrics.HistogramOpts{
		Namespace:    "consensus",
		Subsystem:    "bdls",
		Name:         "block_signature_duration",
		Help:         "Time spent exchanging and collecting Fabric BFT block metadata signatures.",
		LabelNames:   []string{"channel"},
		StatsdFormat: "%{#fqname}.%{channel}",
	}
	ledgerWriteDurationOpts = metrics.HistogramOpts{
		Namespace:    "consensus",
		Subsystem:    "bdls",
		Name:         "ledger_write_duration",
		Help:         "Time spent writing a BDLS-finalised block to the local ledger.",
		LabelNames:   []string{"channel"},
		StatsdFormat: "%{#fqname}.%{channel}",
	}
)

// Metrics bundles the BDLS consenter's observable counters/gauges. The shape
// mirrors orderer/consensus/smartbft/metrics.go so dashboards built for one
// consenter translate cleanly to the other.
type Metrics struct {
	ClusterSize               metrics.Gauge
	CommittedBlockNumber      metrics.Gauge
	IsLeader                  metrics.Gauge
	LeaderID                  metrics.Gauge
	ProposalFailures          metrics.Counter
	CommitPipelineDuration    metrics.Histogram
	ConsensusFinalityDuration metrics.Histogram
	BlockSignatureDuration    metrics.Histogram
	LedgerWriteDuration       metrics.Histogram
}

// NewMetrics constructs a Metrics wired to the supplied provider.
func NewMetrics(p metrics.Provider) *Metrics {
	return &Metrics{
		ClusterSize:               p.NewGauge(clusterSizeOpts),
		CommittedBlockNumber:      p.NewGauge(committedBlockNumberOpts),
		IsLeader:                  p.NewGauge(isLeaderOpts),
		LeaderID:                  p.NewGauge(leaderIDOpts),
		ProposalFailures:          p.NewCounter(proposalFailuresOpts),
		CommitPipelineDuration:    p.NewHistogram(commitPipelineDurationOpts),
		ConsensusFinalityDuration: p.NewHistogram(consensusFinalityDurationOpts),
		BlockSignatureDuration:    p.NewHistogram(blockSignatureDurationOpts),
		LedgerWriteDuration:       p.NewHistogram(ledgerWriteDurationOpts),
	}
}
