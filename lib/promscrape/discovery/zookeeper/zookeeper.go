package zookeeper

import (
	"flag"
	"fmt"
	"time"

	"github.com/VictoriaMetrics/VictoriaMetrics/lib/promscrape/discoveryutil"
	"github.com/VictoriaMetrics/VictoriaMetrics/lib/promutil"
)

// SDCheckInterval defines interval for targets refresh.
var SDCheckInterval = flag.Duration("promscrape.kubernetesSDCheckInterval", 30*time.Second, "Interval for checking for changes in Kubernetes API server. "+
	"This works only if kubernetes_sd_configs is configured in '-promscrape.config' file. "+
	"See https://docs.victoriametrics.com/victoriametrics/sd_configs/#kubernetes_sd_configs for details")

// ServersetSDConfig is the configuration for Twitter serversets in Zookeeper based discovery.
type ServersetSDConfig struct {
	Servers []string      `yaml:"servers"`
	Paths   []string      `yaml:"paths"`
	Timeout time.Duration `yaml:"timeout,omitempty"`
}

func (sdc *ServersetSDConfig) GetLabels(baseDir string) ([]*promutil.Labels, error) {
	cfg, err := getAPIConfigForServerset(sdc)
	if err != nil {
		return nil, fmt.Errorf("cannot get API config: %w", err)
	}

	ms := cfg.zkWatcher.sources
	ret := make([]*promutil.Labels, 0, len(ms))
	for _, v := range ms {
		ret = append(ret, v)
	}
	return ret, nil
}

func (sdc *ServersetSDConfig) MustStop() {
	v := configMap.Delete(sdc)
	if v != nil {
		// v can be nil if GetLabels wasn't called yet.
		cfg := v.(*apiConfig)
		cfg.mustStop()
	}
	return
}

// apiConfig contains config for API server.
type apiConfig struct {
	zkWatcher *zookeeperWatcher
	c         *discoveryutil.Client
}

func (ac *apiConfig) mustStop() {
	ac.zkWatcher.mustStop()
}

var configMap = discoveryutil.NewConfigMap()

func getAPIConfigForServerset(sdc *ServersetSDConfig) (*apiConfig, error) {
	v, err := configMap.Get(sdc, func() (any, error) { return newAPIConfigForServerset(sdc) })
	if err != nil {
		return nil, err
	}
	return v.(*apiConfig), nil
}

func newAPIConfigForServerset(sdc *ServersetSDConfig) (*apiConfig, error) {
	zkWatcher, err := newZookeeperWatcher(sdc.Servers, sdc.Timeout, sdc.Paths, parseServersetMember)
	if err != nil {
		return nil, err
	}

	return &apiConfig{
		zkWatcher: zkWatcher,
	}, nil
}
