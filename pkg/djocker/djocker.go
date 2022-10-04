package djocker

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"

	"github.com/zikwall/docker-playground/pkg/metrics"
)

var playgroundFilters = filters.NewArgs(filters.Arg("label", "docker-playground"))

type Docker interface {
	PullImage(ctx context.Context, state State) error
	CheckImage(ctx context.Context, state State) (bool, error)
	RunContainer(ctx context.Context, state State) (string, error)
	RemoveContainer(ctx context.Context, state State) error
	Executor
}

type Executor interface {
	ExecCommand(ctx context.Context, state State) (string, error)
}

type State struct {
	Config      *container.HostConfig
	Version     string
	Image       string
	RunID       string
	containerID string
	CMD         []string
}

func (s *State) WithID(id string) {
	s.containerID = id
}

type docker struct {
	cli    *client.Client
	cache  Cache
	metric metrics.Metrics
}

func (d *docker) PullImage(ctx context.Context, state State) error {
	img, found := d.cache.Find(state.Image, state.Version)
	if !found {
		return errors.New("version not found")
	}
	ok, err := d.CheckImage(ctx, state)
	if err != nil {
		return err
	}
	if ok {
		return nil
	}
	out, err := d.cli.ImagePull(ctx, pair(img.Image, state.Version), types.ImagePullOptions{})
	if err != nil {
		return err
	}
	// todo send to channel for websocket API
	// create channel := make(chan Event, 1)
	// for {
	//   read > write > receive err > write and close
	// }
	// return channel
	_, err = io.ReadAll(out)
	if err != nil {
		return err
	}
	return nil
}

func (d *docker) CheckImage(ctx context.Context, state State) (bool, error) {
	_, _, err := d.cli.ImageInspectWithRaw(ctx, state.Image)
	if err == nil {
		return true, nil
	}
	return false, err
}

func (d *docker) RunContainer(ctx context.Context, state State) (string, error) {
	config := &container.Config{
		Image: state.Image,
		Labels: map[string]string{
			"version": state.Version,
			"run_id":  state.RunID,
		},
	}
	runtimeConfig := state.Config
	if runtimeConfig.Resources.Memory == 0 {
		runtimeConfig.Resources.Memory = 100 * 1024 * 1024
	}
	if runtimeConfig.Resources.NanoCPUs == 0 {
		runtimeConfig.Resources.NanoCPUs = 1
	}
	if runtimeConfig.Resources.CpusetCpus == "" {
		runtimeConfig.Resources.CpusetCpus = "0,1"
	}
	i, err := d.cli.ContainerCreate(ctx, config, runtimeConfig, nil, nil, "")
	if err != nil {
		return "", err
	}
	if err = d.cli.ContainerStart(ctx, i.ID, types.ContainerStartOptions{}); err != nil {
		return "", err
	}
	return i.ID, nil
}

func (d *docker) RemoveContainer(ctx context.Context, state State) error {
	return d.cli.ContainerRemove(ctx, state.containerID, types.ContainerRemoveOptions{
		RemoveVolumes: true,
		Force:         true,
	})
}

func (d *docker) ExecCommand(ctx context.Context, state State) (string, error) {
	var (
		id  string
		err error
	)
	if err = d.PullImage(ctx, state); err != nil {
		return "", err
	}
	id, err = d.RunContainer(ctx, state)
	if err != nil {
		return "", err
	}
	state.WithID(id)

	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
		case <-done:
		}
		defer func() {
			if err = d.RemoveContainer(ctx, state); err != nil {
				log.Println(err)
			}
		}()
	}()
	output, err := d.execCommand(ctx, state)
	if err != nil {
		return "", err
	}
	return output, nil
}

func (d *docker) execCommand(ctx context.Context, state State) (string, error) {
	exec, err := d.cli.ContainerExecCreate(ctx, state.containerID, types.ExecConfig{
		AttachStderr: true,
		AttachStdout: true,
		Cmd:          state.CMD,
	})
	if err != nil {
		return "", err
	}
	resp, err := d.cli.ContainerExecAttach(ctx, exec.ID, types.ExecStartCheck{})
	if err != nil {
		return "", err
	}
	defer resp.Close()
	var (
		outBuf, errBuf bytes.Buffer
		outputDone     = make(chan error)
	)
	go func() {
		_, err = stdcopy.StdCopy(&outBuf, &errBuf, resp.Reader)
		outputDone <- err
	}()
	select {
	case err = <-outputDone:
		if err != nil {
			return "", err
		}

	case <-ctx.Done():
		return "", ctx.Err()
	}
	stderr := errBuf.String()
	if stderr == "" {
		return outBuf.String(), nil
	}
	return outBuf.String() + "\n" + stderr, nil
}

func New(url string, cache Cache, metric metrics.Metrics) (Docker, error) {
	opts, err := clientOpts(url)
	if err != nil {
		return nil, err
	}
	cli, err := client.NewClientWithOpts(opts...)
	if err != nil {
		return nil, err
	}
	d := &docker{
		cli:    cli,
		cache:  cache,
		metric: metric,
	}
	return d, nil
}
