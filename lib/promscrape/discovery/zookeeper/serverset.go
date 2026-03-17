package zookeeper

import (
	"encoding/json"
	"fmt"
	"net"
	"strconv"

	"github.com/VictoriaMetrics/VictoriaMetrics/lib/prompb"
	"github.com/VictoriaMetrics/VictoriaMetrics/lib/promutil"
	"github.com/grafana/regexp"
	"github.com/prometheus/common/model"
)

const (
	serversetLabelPrefix         = "__meta_" + "serverset_"
	serversetStatusLabel         = serversetLabelPrefix + "status"
	serversetPathLabel           = serversetLabelPrefix + "path"
	serversetEndpointLabelPrefix = serversetLabelPrefix + "endpoint"
	serversetShardLabel          = serversetLabelPrefix + "shard"
)

type serversetMember struct {
	ServiceEndpoint     serversetEndpoint
	AdditionalEndpoints map[string]serversetEndpoint
	Status              string `json:"status"`
	Shard               int    `json:"shard"`
}

type serversetEndpoint struct {
	Host string
	Port int
}

func parseServersetMember(data []byte, path string) (*promutil.Labels, error) {
	member := serversetMember{}

	if err := json.Unmarshal(data, &member); err != nil {
		return nil, fmt.Errorf("error unmarshaling serverset member %q: %w", path, err)
	}

	labels := make(map[string]string)
	labels[serversetPathLabel] = path
	labels[model.AddressLabel] =
		net.JoinHostPort(member.ServiceEndpoint.Host, strconv.Itoa(member.ServiceEndpoint.Port))

	labels[serversetEndpointLabelPrefix+"_host"] = member.ServiceEndpoint.Host
	labels[serversetEndpointLabelPrefix+"_port"] = strconv.Itoa(member.ServiceEndpoint.Port)

	for name, endpoint := range member.AdditionalEndpoints {
		cleanName := SanitizeLabelName(name)
		labels[serversetEndpointLabelPrefix+"_host_"+cleanName] =
			endpoint.Host
		labels[serversetEndpointLabelPrefix+"_port_"+cleanName] =
			strconv.Itoa(endpoint.Port)
	}

	labels[serversetStatusLabel] = member.Status
	labels[serversetShardLabel] = strconv.Itoa(member.Shard)

	ret := make([]prompb.Label, 0, len(labels))
	for k, v := range labels {
		ret = append(ret, prompb.Label{Name: k, Value: v})
	}
	return &promutil.Labels{Labels: ret}, nil
}

var invalidLabelCharRE = regexp.MustCompile(`[^a-zA-Z0-9_]`)

// SanitizeLabelName replaces anything that doesn't match
// client_label.LabelNameRE with an underscore.
// Note: this does not handle all Prometheus label name restrictions (such as
// not starting with a digit 0-9), and hence should only be used if the label
// name is prefixed with a known valid string.
func SanitizeLabelName(name string) string {
	return invalidLabelCharRE.ReplaceAllString(name, "_")
}
