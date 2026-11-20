// Package nms fetches the network from Watcher NMS: the file its Pinger page hands out, under a key of the machine
// role watcher-pinger.
package nms

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"watcher-pinger/src/addressbook"
)

// Path is where Watcher NMS hands the network out to the role.
const Path = "/api/access-points/pinger.json"

// maxSize bounds what is read: the network of a few thousand addresses takes well under a megabyte.
const maxSize = 64 << 20

// Client asks one Watcher NMS.
type Client struct {
	// URL is the address of Watcher NMS, https://nms.example.com say
	URL   string
	Token string
	HTTP  *http.Client
}

// New asks the Watcher NMS at the given address with the given key. A redirect is not followed: the key would go
// with it, to the same host over plain HTTP as well.
func New(url, token string) *Client {
	return &Client{
		URL:   strings.TrimRight(url, "/"),
		Token: token,
		HTTP: &http.Client{
			Timeout: time.Minute,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

// Fetch returns the network, and the file as it came, to be kept.
func (c *Client) Fetch(ctx context.Context) (*addressbook.Book, []byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL+Path, nil)
	if err != nil {
		return nil, nil, err
	}
	request.Header.Set("Authorization", "Bearer "+c.Token)
	request.Header.Set("Accept", "application/json")

	response, err := c.HTTP.Do(request)
	if err != nil {
		return nil, nil, fmt.Errorf("the Watcher NMS at %s: %w", c.URL, err)
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("the Watcher NMS at %s answered %s", c.URL+Path, response.Status)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxSize))
	if err != nil {
		return nil, nil, fmt.Errorf("the Watcher NMS at %s: %w", c.URL+Path, err)
	}
	book, err := addressbook.Parse(bytes.NewReader(body))
	if err != nil {
		return nil, nil, fmt.Errorf("the Watcher NMS at %s handed out no network: %w", c.URL+Path, err)
	}
	return book, body, nil
}
