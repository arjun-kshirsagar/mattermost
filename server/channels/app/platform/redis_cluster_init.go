//go:build oss_cluster
// +build oss_cluster

package platform

import "github.com/mattermost/mattermost/server/v8/einterfaces"

// The platform looks for `clusterInterface` during bootstrap.
// We inject our Redis implementation.
func init() {
	clusterInterface = func(ps *PlatformService) einterfaces.ClusterInterface {
		return NewRedisCluster(ps)
	}
}
