package zookeeper

import "github.com/VictoriaMetrics/VictoriaMetrics/lib/logger"

type zookeeperWatcherLogger struct{}

func (zl zookeeperWatcherLogger) Printf(format string, v ...interface{}) {
	logger.Infof(format, v...)
}
