package appkey

import (
	"encoding/json"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	bolt "go.etcd.io/bbolt"
)

var bucketName = []byte("appkeys")

// Persister provides durable write-behind storage for an in-memory Store.
// All request-time operations remain on the in-memory Store; the Persister
// only reads from the Store (via List) and writes to bbolt on a timer and
// at shutdown. No bbolt I/O ever occurs on the request hot path.
type Persister struct {
	db       *bolt.DB
	store    *Store
	interval time.Duration
	logger   *slog.Logger
	stop     chan struct{}
	done     chan struct{}
	started  atomic.Bool
	once     sync.Once
}

// NewPersister opens (or creates) the bbolt database at path and loads any
// persisted records into store. The caller must call Start() to begin
// periodic flushing, and Close() to perform a final flush and release the db.
func NewPersister(path string, store *Store, interval time.Duration, logger *slog.Logger) (*Persister, error) {
	db, err := bolt.Open(path, 0600, &bolt.Options{Timeout: 1 * time.Second})
	if err != nil {
		return nil, err
	}

	// Ensure the bucket exists.
	if err := db.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists(bucketName)
		return err
	}); err != nil {
		_ = db.Close()
		return nil, err
	}

	p := &Persister{
		db:       db,
		store:    store,
		interval: interval,
		logger:   logger,
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}

	if err := p.loadAll(); err != nil {
		_ = db.Close()
		return nil, err
	}

	// Register immediate-persist callback so Vend writes to bbolt before
	// returning the key to the caller. Vend is a management API (not hot
	// path), so the synchronous disk write is acceptable.
	store.SetOnVend(p.PersistKey)

	return p, nil
}

// loadAll reads every key from the bbolt bucket and restores them into the
// in-memory store. Called once at startup before the server accepts traffic.
func (p *Persister) loadAll() error {
	return p.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketName)
		return b.ForEach(func(k, v []byte) error {
			var snap UsageSnapshot
			if err := json.Unmarshal(v, &snap); err != nil {
				p.logger.Warn("skipping corrupt app key entry",
					"key", string(k),
					"error", err,
				)
				return nil // skip corrupt entries, don't fail startup
			}
			p.store.Restore(&snap)
			return nil
		})
	})
}

// Start begins the periodic flush goroutine.
func (p *Persister) Start() {
	p.started.Store(true)
	go func() {
		defer close(p.done)
		ticker := time.NewTicker(p.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := p.flush(); err != nil {
					p.logger.Error("app key persistence flush failed", "error", err)
				}
			case <-p.stop:
				return
			}
		}
	}()
}

// Close performs a final flush and closes the bbolt database.
// Safe to call multiple times; safe to call on a nil receiver.
func (p *Persister) Close() error {
	if p == nil {
		return nil
	}
	var err error
	p.once.Do(func() {
		if p.started.Load() {
			close(p.stop)
			<-p.done
		}
		// Final flush to capture any counters updated since the last tick.
		if flushErr := p.flush(); flushErr != nil {
			p.logger.Error("app key persistence final flush failed", "error", flushErr)
			err = flushErr
		}
		if closeErr := p.db.Close(); closeErr != nil {
			p.logger.Error("app key persistence db close failed", "error", closeErr)
			if err == nil {
				err = closeErr
			}
		}
	})
	return err
}

// PersistKey writes a single key snapshot to bbolt. Called synchronously
// from Store.Vend to ensure newly created keys are durable immediately.
func (p *Persister) PersistKey(snap *UsageSnapshot) {
	if err := p.db.Update(func(tx *bolt.Tx) error {
		data, err := json.Marshal(snap)
		if err != nil {
			return err
		}
		return tx.Bucket(bucketName).Put([]byte(snap.Key), data)
	}); err != nil {
		p.logger.Error("failed to persist vended app key",
			"key", snap.Key,
			"error", err,
		)
	}
}

// flush writes all current snapshots to bbolt in a single transaction.
// NOTE: flush only upserts keys present in the in-memory store. It does not
// delete keys from bbolt that are absent from memory. If a Store.Delete method
// is added in the future, flush (or Delete itself) must also remove the key
// from bbolt to prevent zombie keys reappearing on restart.
func (p *Persister) flush() error {
	snapshots := p.store.List()
	return p.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketName)
		for _, snap := range snapshots {
			data, err := json.Marshal(snap)
			if err != nil {
				p.logger.Error("failed to marshal app key snapshot",
					"key", snap.Key,
					"error", err,
				)
				continue
			}
			if err := b.Put([]byte(snap.Key), data); err != nil {
				return err
			}
		}
		return nil
	})
}
