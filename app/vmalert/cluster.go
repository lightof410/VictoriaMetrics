package main

import (
	"flag"
	"strconv"
	"strings"

	"github.com/VictoriaMetrics/VictoriaMetrics/app/vmalert/config"
	"github.com/VictoriaMetrics/VictoriaMetrics/lib/logger"
)

var (
	clusterMembersCount = flag.Int("vmalert.cluster.membersCount", 1, "The number of members in a cluster of vmalert instances. "+
		"Each member must have a unique -vmalert.cluster.memberNum in the range 0 ... vmalert.cluster.membersCount-1 . "+
		"Each member then evaluates roughly 1/N of all the recording and alerting rules. By default, cluster mode is disabled, "+
		"i.e. a single vmalert instance evaluates all the rules.")
	clusterMemberNum = flag.String("vmalert.cluster.memberNum", "0", "The number of this vmalert instance in the cluster. "+
		"It must be a unique value in the range 0 ... vmalert.cluster.membersCount-1 across vmalert instances in the cluster. "+
		"Can be specified as pod name of Kubernetes StatefulSet - pod-name-Num, where Num is a numeric part of pod name.")
)

// clusterMemberID is the numeric id of the current vmalert instance in the cluster.
// It is obtained by parsing the -vmalert.cluster.memberNum flag.
var clusterMemberID int

// mustInitClusterMemberID parses -vmalert.cluster.memberNum and -vmalert.cluster.membersCount
// and sets clusterMemberID accordingly.
func mustInitClusterMemberID() {
	n, err := parseClusterMemberNum(*clusterMemberNum)
	if err != nil {
		logger.Fatalf("cannot parse -vmalert.cluster.memberNum=%q: %s", *clusterMemberNum, err)
	}
	if *clusterMembersCount < 1 {
		logger.Fatalf("-vmalert.cluster.membersCount can't be lower than 1: got %d", *clusterMembersCount)
	}
	if n < 0 || n >= *clusterMembersCount {
		logger.Fatalf("-vmalert.cluster.memberNum must be in the range [0..%d] according to -vmalert.cluster.membersCount=%d",
			*clusterMembersCount-1, *clusterMembersCount)
	}
	clusterMemberID = n
}

// parseClusterMemberNum extracts the numeric member id from the given value.
// It supports the Kubernetes StatefulSet pod name format - pod-name-Num - where
// Num is the numeric part of the pod name.
func parseClusterMemberNum(s string) (int, error) {
	// special case for Kubernetes StatefulSet, where the pod name is formatted as
	// some-pod-name-1; obtain memberNum from the last segment.
	// https://github.com/VictoriaMetrics/VictoriaMetrics/issues/2359
	if idx := strings.LastIndexByte(s, '-'); idx >= 0 {
		s = s[idx+1:]
	}
	return strconv.Atoi(s)
}

// filterRulesByClusterMember returns only those rules which must be evaluated by
// the vmalert instance with the given memberID in a cluster of membersCount instances.
// Rules are distributed across cluster members using the rule ID hash, so each rule
// is evaluated by exactly one member. When membersCount <= 1 the input slice is
// returned unchanged.
func filterRulesByClusterMember(rules []config.Rule, membersCount, memberID int) []config.Rule {
	if membersCount <= 1 {
		return rules
	}
	dst := make([]config.Rule, 0, len(rules))
	for _, r := range rules {
		if r.ID%uint64(membersCount) == uint64(memberID) {
			dst = append(dst, r)
		}
	}
	return dst
}
