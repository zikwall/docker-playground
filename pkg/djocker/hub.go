package djocker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

const (
	url = "https://hub.docker.com/v2"
)

type Client struct {
	cli *http.Client
}

func (c *Client) Tags(ctx context.Context, repository string) ([]ImageTag, error) {
	nextURL := fmt.Sprintf("%s/repositories/%s/tags/", url, repository)
	var tags []ImageTag
	for {
		resp, err := c.getTags(ctx, nextURL)
		if err != nil {
			return nil, err
		}
		tags = append(tags, resp.Results...)
		if resp.Next == nil {
			break
		}
		nextURL = *resp.Next
	}
	return tags, nil
}

func (c *Client) getTags(ctx context.Context, url string) (*GetImageTagsResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Second*5)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", url, http.NoBody)
	if err != nil {
		return nil, err
	}
	r, err := c.cli.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = r.Body.Close()
	}()
	response := &GetImageTagsResponse{}
	err = json.NewDecoder(r.Body).Decode(response)
	if err != nil {
		return nil, err
	}
	return response, nil
}

func NewHubClient() *Client {
	return &Client{
		cli: http.DefaultClient,
	}
}
