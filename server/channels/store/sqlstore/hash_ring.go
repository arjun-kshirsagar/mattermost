// server/channels/store/sqlstore/hash_ring.go
package sqlstore

import (
	"hash/fnv"
)

type HashRing struct {
	nodes []string
}

func NewHashRing(nodes []string) *HashRing {
	return &HashRing{nodes: nodes}
}

func (h *HashRing) Get(key string) string {
	if len(h.nodes) == 0 {
		return ""
	}
	hash := fnv.New32a()
	hash.Write([]byte(key))
	idx := int(hash.Sum32()) % len(h.nodes)
	return h.nodes[idx]
}