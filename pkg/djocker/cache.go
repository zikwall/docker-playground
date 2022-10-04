package djocker

import (
	"context"
	"log"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zikwall/docker-playground/pkg/metrics"
)

type Cache interface {
	Find(repo, tag string) (Image, bool)
}

type Hub interface {
	Tags(ctx context.Context, repository string) ([]ImageTag, error)
}

type Repository struct {
	Name string `yaml:"name"`
}

type Image struct {
	Image string
	Tag   string

	OS           string
	Architecture string
	Digest       string

	PushedAt time.Time
}

func NewImage(image, tag string) Image {
	return Image{
		Image:        image,
		Tag:          tag,
		OS:           runtime.GOOS,
		Architecture: runtime.GOARCH,
	}
}

type cache struct {
	ctx            context.Context
	hub            Hub
	metric         metrics.Metrics
	mutex          *sync.RWMutex
	images         map[string]Image
	repositories   []Repository
	expirationTime time.Duration
	updatedAt      time.Time
	updating       int32
}

func (c *cache) Find(repo, tag string) (Image, bool) {
	defer func() {
		go c.asyncRunCheck(c.ctx)
	}()

	c.mutex.RLock()
	image, ok := c.images[pair(repo, tag)]
	c.mutex.RUnlock()
	return image, ok
}

func (c *cache) asyncRunCheck(ctx context.Context) {
	c.mutex.RLock()
	if time.Since(c.updatedAt) < c.expirationTime {
		c.mutex.RUnlock()
		return
	}
	c.mutex.RUnlock()

	if !atomic.CompareAndSwapInt32(&c.updating, 0, 1) {
		return
	}
	defer atomic.StoreInt32(&c.updating, 0)
	select {
	case <-ctx.Done():
		return
	default:
	}

	func() {
		images, err := c.getRepositories(ctx, c.repositories)
		if err != nil {
			return
		}
		c.mutex.Lock()
		c.updatedAt = time.Now()
		c.images = images
		c.mutex.Unlock()
	}()
}

func (c *cache) backgroundCheck(ctx context.Context) {
	ticker := time.NewTicker(c.expirationTime)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.asyncRunCheck(ctx)
		}
	}
}

func (c *cache) getRepositories(ctx context.Context, repositories []Repository) (map[string]Image, error) {
	wg := &sync.WaitGroup{}
	imagesMap := make([][]Image, len(c.images))
	for i := range repositories {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			images, err := c.getImages(ctx, repositories[i].Name)
			if err != nil {
				log.Println(err)
				return
			}
			imagesMap[i] = images
		}()
	}
	wg.Wait()

	var merged map[string]Image
	for _, images := range imagesMap {
		for _, image := range images {
			merged[pair(image.Image, image.Tag)] = image
		}
	}
	return merged, nil
}

func (c *cache) getImages(ctx context.Context, repository string) ([]Image, error) {
	tags, err := c.hub.Tags(ctx, repository)
	if err != nil {
		return nil, err
	}
	var images []Image
	for i := range tags {
		for _, image := range tags[i].Images {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			default:
			}
			if !isUsable(image.OS, image.Architecture) {
				continue
			}
			images = append(images, Image{
				Image:        repository,
				Tag:          tags[i].Name,
				OS:           image.OS,
				Architecture: image.Architecture,
				Digest:       image.Digest,
				PushedAt:     image.LastPushed,
			})
		}
	}
	sort.Slice(images, func(i, j int) bool {
		return images[i].PushedAt.After(images[j].PushedAt)
	})
	return images, nil
}

func NewCache(ctx context.Context, hub Hub, metric metrics.Metrics, repositories []Repository) (Cache, error) {
	c := &cache{
		mutex:        &sync.RWMutex{},
		repositories: repositories,
		hub:          hub,
		metric:       metric,
	}
	var err error
	c.images, err = c.getRepositories(ctx, repositories)
	if err != nil {
		return nil, err
	}
	go c.backgroundCheck(ctx)
	return c, nil
}
