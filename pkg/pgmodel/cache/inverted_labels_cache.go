package cache

import (
	"fmt"

	"github.com/timescale/promscale/pkg/clockcache"
)

type LabelInfo struct {
	LabelID int32
	Pos     int32
}

type LabelKey struct {
	MetricName, Name, Value string
}

func NewLabelKey(metricName, name, value string) LabelKey {
	return LabelKey{MetricName: metricName, Name: name, Value: value}
}

func (lk LabelKey) len() int {
	return len(lk.MetricName) + len(lk.Name) + len(lk.Value)
}

func NewLabelInfo(lableID, pos int32) LabelInfo {
	return LabelInfo{LabelID: lableID, Pos: pos}
}

func (li LabelInfo) len() int {
	return 8
}

// Label key-pair -> (id,pos) cache
// Used when creating series to avoid DB calls for labels
type InvertedLabelsCache struct {
	cache *clockcache.Cache
}

// Cache is thread-safe
func NewInvertedLablesCache(size uint64) (*InvertedLabelsCache, error) {
	if size <= 0 {
		return nil, fmt.Errorf("labels cache size must be > 0")
	}
	cache := clockcache.WithMetrics("inverted_labels", "metric", size)
	return &InvertedLabelsCache{cache}, nil
}

func (c *InvertedLabelsCache) GetLabelsId(key LabelKey) (LabelInfo, bool) {
	id, found := c.cache.Get(key)
	if found {
		return id.(LabelInfo), found
	}
	return LabelInfo{}, false
}

func (c *InvertedLabelsCache) Put(key LabelKey, val LabelInfo) bool {
	_, added := c.cache.Insert(key, val, uint64(key.len())+uint64(val.len())+17)
	return added
}
