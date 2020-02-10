package newrelicEvents

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// 950kb (newrelic is 1MB max
// no sane person would have a single 50kb message???
// TODO: allow crazy things, because we are in a crazy world
const maxSize = 950000

///////////////////////////////////////////////////////////////////////////

type dataStore struct {
	*sync.Mutex
	Data string
}

///////////////////////////////////////////////////////////////////////////

func New(AccountID string, License string) *Newrelic {
	return &Newrelic{
		Poster: StandardPost(http.DefaultClient),
		URL:    fmt.Sprintf("https://insights-collector.newrelic.com/v1/accounts/%s/events", AccountID),
		data: dataStore{
			Mutex: &sync.Mutex{},
			Data:  "",
		},
		license: License,
	}
}

///////////////////////////////////////////////////////////////////////////

type Newrelic struct {
	Poster func(req *http.Request) error

	data    dataStore
	URL     string
	license string
}

// RecordEvent will add the event to the queue of events that is thread safe, you can go RecordEvent
func (n *Newrelic) RecordEvent(Name string, in map[string]interface{}) error {
	if Name == "" {
		return errors.New("No Event Name")
	}
	if in == nil {
		return errors.New("data is nil")
	}
	in["eventType"] = Name
	n.data.Lock()
	defer n.data.Unlock()
	leaderKey := ""
	if len(n.data.Data) > 0 {
		leaderKey = ","
	}
	marshledData, err := json.Marshal(in)
	if err != nil {
		return err
	}
	n.data.Data += fmt.Sprintf("%s%s", leaderKey, marshledData)

	if len(n.data.Data) > maxSize {
		// copy data into function so we can safely reuse the memory incase post is Async
		err = n._Post(n.data.Data)
		n.data.Data = ""
	}
	return err
}

// _Post is in charge of building the http Request and passing it on to the designated poster
func (n *Newrelic) _Post(data string) error {
	// wrap the hand made json array correctly for posting (don't know a faster way to perform this logic)
	data = fmt.Sprintf("[%s]", data)
	r, w := io.Pipe()
	defer r.Close()
	defer w.Close()
	// reduce memory buffer usage by syncing through a channel as the content is read
	// to perform the request
	go func() {
		zipper := gzip.NewWriter(w)
		zipper.Write([]byte(data))
		zipper.Flush()
		w.Close()
		zipper.Close()
	}()
	req, err := http.NewRequest("POST", n.URL, r)
	if err != nil {
		return err
	}
	req.Header.Add("Content-Type", "application/json")
	req.Header.Add("X-Insert-Key", n.license)
	req.Header.Add("Content-Encoding", "gzip")
	return n.Poster(req)
}

///////////////////////////////////////////////////////////////////////////

// Sync performs a force Post to newrelic disregarding waiting for max buffer size
func (n *Newrelic) Sync() error {
	n.data.Lock()
	defer n.data.Unlock()
	return n._Post(n.data.Data)
}

///////////////////////////////////////////////////////////////////////////

func StandardPost(client *http.Client) func(*http.Request) error {
	return func(req *http.Request) error {
		ctx, canFunc := context.WithTimeout(context.Background(), time.Second*30)
		defer canFunc()
		req = req.WithContext(ctx)
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return fmt.Errorf("Bad Response: %d - %s", resp.StatusCode, resp.Status)
		}
		return nil
	}
}

///////////////////////////////////////////////////////////////////////////

func AsyncPost(ctx context.Context, client http.Client, errorLog io.Writer) func(*http.Request) error {
	return func(req *http.Request) error {
		req = req.WithContext(ctx)
		go func() {
			resp, err := client.Do(req)
			if err != nil {
				errorLog.Write([]byte(fmt.Sprintf("Failed to send web request: %s\n", err)))
				return
			}
			defer resp.Body.Close()
			if resp.StatusCode != 200 {
				errorLog.Write([]byte(fmt.Sprintf("Bad Response: %d - %s\n", resp.StatusCode, resp.Status)))
			}
			return
		}()
		return nil
	}
}
