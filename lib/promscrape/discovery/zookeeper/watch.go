package zookeeper

import (
	"context"
	"time"

	"github.com/VictoriaMetrics/VictoriaMetrics/lib/promutil"
	"github.com/go-zookeeper/zk"
)

type zookeeperWatcher struct {
	conn *zk.Conn

	sources map[string]*promutil.Labels

	updates     chan ZookeeperTreeCacheEvent
	pathUpdates []chan ZookeeperTreeCacheEvent

	treeCaches []*ZookeeperTreeCache

	parse func(data []byte, path string) (*promutil.Labels, error)

	cancel    context.CancelFunc
	ctx       context.Context
	stoppedCh chan struct{}
}

func newZookeeperWatcher(srvs []string, timeout time.Duration, paths []string, pf func(data []byte, path string) (*promutil.Labels, error)) (*zookeeperWatcher, error) {
	zl := zookeeperWatcherLogger{}

	conn, _, err := zk.Connect(srvs, timeout, func(c *zk.Conn) {
		c.SetLogger(zl)
	})
	if err != nil {
		return nil, err
	}

	updates := make(chan ZookeeperTreeCacheEvent)
	ctx, cancel := context.WithCancel(context.Background())

	sd := &zookeeperWatcher{
		stoppedCh: make(chan struct{}),
		conn:      conn,
		updates:   updates,
		sources:   make(map[string]*promutil.Labels),
		parse:     pf,
		cancel:    cancel,
		ctx:       ctx,
	}

	for _, path := range paths {
		pathUpdate := make(chan ZookeeperTreeCacheEvent)
		sd.pathUpdates = append(sd.pathUpdates, pathUpdate)
		sd.treeCaches = append(sd.treeCaches, NewZookeeperTreeCache(conn, path, pathUpdate))
	}
	go func() {
		sd.watchForNodeUpdate()
		close(sd.stoppedCh)
	}()

	return sd, nil
}

func (z *zookeeperWatcher) mustStop() {
	z.cancel()
	<-z.stoppedCh
}

func (z *zookeeperWatcher) watchForNodeUpdate() {
	defer func() {
		for _, tc := range z.treeCaches {
			tc.Stop()
		}
		for _, pathUpdate := range z.pathUpdates {
			// Drain event channel in case the treecache leaks goroutines otherwise.
			for range pathUpdate {

			}
		}
		z.conn.Close()
		z.stoppedCh <- struct{}{}
	}()

	for _, pathUpdate := range z.pathUpdates {
		go func(update chan ZookeeperTreeCacheEvent) {
			for event := range update {
				select {
				case z.updates <- event:
				case <-z.ctx.Done():
					return
				}
			}
		}(pathUpdate)
	}

	for {
		select {
		case <-z.ctx.Done():
			return
		case event := <-z.updates:
			if event.Data != nil {
				labelSet, err := z.parse(*event.Data, event.Path)
				if err == nil {
					z.sources[event.Path] = labelSet
				} else {
					delete(z.sources, event.Path)
				}
			} else {
				delete(z.sources, event.Path)
			}
		}
	}
}
