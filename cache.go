package infreqdb

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/boltdb/bolt"
	"github.com/goamz/goamz/s3"
)

type cachepartition struct {
	*sync.RWMutex
	db           *bolt.DB
	fname        string
	lastModified time.Time
}

func (cp *cachepartition) view(fn func(*bolt.Tx) error) error {
	//Locking... so we when we close bolt.DB there are no reads inflight
	cp.RLock()
	defer cp.RUnlock()
	if cp.db == nil {
		return fmt.Errorf("DB is nil")
	}
	return cp.db.View(fn)
}

func (cp *cachepartition) get(bucket, key []byte) (v []byte, err error) {
	err = cp.view(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucket)
		if b == nil {
			return fmt.Errorf("Bucket %s not found", bucket)
		}
		v = b.Get(key)
		if v == nil {
			return fmt.Errorf("Key %v not found in bucket %v", key, bucket)
		}
		return nil
	})
	return
}

func (cp *cachepartition) close() error {
	//Lock forever... no more reads here...
	//Lock waits for all readers to finish...
	cp.Lock()
	defer os.Remove(cp.fname)
	if cp.db != nil {
		return cp.db.Close()
	}
	return nil
}

func newcachepartition(key string, bucket *s3.Bucket) (*cachepartition, error) {
	cp := &cachepartition{RWMutex: &sync.RWMutex{}}
	//Download file from s3
	//GetResponse just to be able to read Last-Modified
	resp, err := bucket.GetResponse(key)
	if err != nil {
		return nil, err
	}
	//Populate last-modified from header
	cp.lastModified, err = http.ParseTime(resp.Header.Get("last-modified"))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	//uncompress
	gzrd, err := gzip.NewReader(resp.Body)
	if err != nil {
		return nil, err
	}
	defer gzrd.Close()
	tmpfile, err := ioutil.TempFile("", "infreqdb-")
	if err != nil {
		return nil, err
	}
	_, err = io.Copy(tmpfile, gzrd)
	cp.fname = tmpfile.Name()
	tmpfile.Close()
	if err != nil {
		os.Remove(cp.fname)
		return nil, err
	}
	cp.db, err = bolt.Open(cp.fname, os.ModeExclusive, nil)
	if err != nil {
		os.Remove(cp.fname)
		return nil, err
	}
	return cp, nil
}

func upLoadCachePartition(key, fname string, bucket *s3.Bucket) error {
	var network bytes.Buffer
	//compress..
	//Yikes in memory
	gzrw := gzip.NewWriter(&network)
	f, err := os.Open(fname)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(gzrw, f)
	if err != nil {
		return err
	}
	gzrw.Close()
	return bucket.Put(key, network.Bytes(), "TODO", "", s3.Options{})
}
