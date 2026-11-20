package main

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"time"

	"watcher-pinger/src/addressbook"
	"watcher-pinger/src/nms"
	"watcher-pinger/src/web"
)

// source is where the server takes the network from: Watcher NMS, with the last network fetched kept in the data
// file, or the data file alone.
type source struct {
	nms      *nms.Client
	file     string
	interval time.Duration
}

func newSource(cfg Config) *source {
	s := &source{file: cfg.DataFile, interval: cfg.NMSInterval}
	if cfg.NMSURL != "" {
		s.nms = nms.New(cfg.NMSURL, cfg.NMSToken)
	}
	return s
}

// label says on the page where the data come from.
func (s *source) label() string {
	if s.nms != nil {
		return s.nms.URL
	}
	return s.file
}

// load reads the network anew: from Watcher NMS where it is set up, keeping a copy in the data file, otherwise from
// the file.
func (s *source) load(ctx context.Context) (*addressbook.Book, error) {
	if s.nms == nil {
		return addressbook.Load(s.file)
	}
	book, body, err := s.nms.Fetch(ctx)
	if err != nil {
		return nil, err
	}
	if s.file != "" {
		if err := keep(s.file, body); err != nil {
			log.Printf("The network fetched is not kept in %s: %v", s.file, err)
		}
	}
	return book, nil
}

// first reads the network at the start. A Watcher NMS out of reach then leaves the copy kept last time, so that the
// server pings what it pinged before rather than nothing.
func (s *source) first(ctx context.Context) (*addressbook.Book, error) {
	book, err := s.load(ctx)
	if err == nil || s.nms == nil || s.file == "" {
		return book, err
	}
	log.Printf("%v; starting from the copy in %s", err, s.file)
	return addressbook.Load(s.file)
}

// refresh fetches the network from Watcher NMS at every interval until ctx ends, through the page, so that it never
// runs beside a reload asked for on the page and overwrites a newer network. A fetch that fails leaves the network as
// it was until the next one.
func (s *source) refresh(ctx context.Context, page *web.Server) {
	if s.nms == nil {
		return
	}
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if _, err := page.ReloadNow(ctx); err != nil {
			log.Println(err)
		}
	}
}

// keep writes the file whole or not at all, so that a start never reads half of it.
func keep(path string, body []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
