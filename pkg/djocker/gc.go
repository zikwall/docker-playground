package djocker

import (
	"context"
	"log"
	"sort"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/client"
)

type gc struct {
	cli              *client.Client
	triggerFrequency time.Duration
	containerTTL     time.Duration
	imagesCount      int
}

func (g *gc) gc(ctx context.Context) {
	ticker := time.NewTicker(g.triggerFrequency)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			err := g.pruneResources(ctx)
			if err != nil {
				log.Println(err)
				continue
			}
		}
	}
}

func (g *gc) pruneResources(ctx context.Context) error {
	count, spaceReclaimed, err := g.pruneContainers(ctx)
	if err != nil {
		return err
	}
	log.Println("containers", count, spaceReclaimed)
	count, spaceReclaimed, err = g.pruneImages(ctx)
	if err != nil {
		return err
	}
	log.Println("images", count, spaceReclaimed)
	return nil
}

func (g *gc) pruneContainers(ctx context.Context) (uint, uint64, error) {
	out, err := g.cli.ContainersPrune(ctx, playgroundFilters)
	if err != nil {
		return 0, 0, err
	}
	var (
		count          uint
		spaceReclaimed uint64
	)
	count += uint(len(out.ContainersDeleted))
	spaceReclaimed += out.SpaceReclaimed
	if g.containerTTL > 0 {
		containers, err := g.cli.ContainerList(ctx, types.ContainerListOptions{
			Size:    true,
			All:     true,
			Limit:   -1,
			Filters: playgroundFilters,
		})
		if err != nil {
			return 0, 0, err
		}
		for _, container := range containers {
			deadline := time.Unix(container.Created, 0).Add(g.containerTTL)
			if time.Now().Before(deadline) {
				continue
			}
			err := g.cli.ContainerRemove(ctx, container.ID, types.ContainerRemoveOptions{
				RemoveVolumes: true,
				Force:         true,
			})
			if err != nil {
				log.Println(err)
				continue
			}

			count++
			spaceReclaimed += uint64(container.SizeRw)
		}
	}
	return count, spaceReclaimed, nil
}

func (g *gc) pruneImages(ctx context.Context) (uint, uint64, error) {
	var (
		count          uint
		spaceReclaimed uint64
	)
	images, err := g.cli.ImageList(ctx, types.ImageListOptions{
		All: true,
	})
	if err != nil {
		return 0, 0, err
	}
	detailed := make([]types.ImageInspect, 0, len(images))
	for _, c := range images {
		inspect, _, err := g.cli.ImageInspectWithRaw(ctx, c.ID)
		if err != nil {
			continue
		}
		detailed = append(detailed, inspect)
	}
	// drop N least recently tagged images.
	sort.Slice(detailed, func(i, j int) bool {
		return detailed[i].Metadata.LastTagTime.Before(detailed[j].Metadata.LastTagTime)
	})
	if len(detailed) > g.imagesCount {
		for _, img := range images {
			ok := true
			for _, tag := range img.RepoTags {
				_, err = g.cli.ImageRemove(ctx, tag, types.ImageRemoveOptions{
					PruneChildren: true,
				})
				if err != nil {
					ok = false
					continue
				}
			}
			if !ok {
				continue
			}
			count++
			spaceReclaimed += uint64(img.Size)
		}
	}
	return count, spaceReclaimed, nil
}

func newGC(cli *client.Client) *gc {
	return &gc{cli: cli}
}
