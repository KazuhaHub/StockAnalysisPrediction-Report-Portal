package app

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"math"
	"strconv"
	"sync"
	"time"
)

type forecastViewerWeek struct {
	key  string
	salt [32]byte
	seen map[[32]byte]struct{}
	base int64 // persisted count at this process's first observation, never a running flush sum
}

// Only aggregate integers survive a week boundary or persistence failure. Neither usernames,
// salts nor HMACs leave this process; restarts intentionally make counts an upper bound.
type forecastViewers struct {
	mu      sync.Mutex
	week    string // monotonic ISO-week watermark, including an idle rollover
	current *forecastViewerWeek
	pending map[string]int64
}

func forecastViewerWeekKey(now time.Time) string {
	y, w := now.UTC().ISOWeek()
	return fmt.Sprintf("forecast_viewers_%04d-W%02d", y, w)
}

func (c *forecastViewers) rollLocked(key string) {
	if key > c.week {
		c.week = key
	}
	if c.current == nil || c.current.key >= key {
		return
	}
	if c.pending == nil {
		c.pending = map[string]int64{}
	}
	c.pending[c.current.key] = c.current.base + int64(len(c.current.seen))
	c.current.salt = [32]byte{}
	c.current.seen = nil
	c.current = nil
}

func (c *forecastViewers) observe(st *Store, user string, now time.Time) error {
	if user == "" {
		return nil
	}
	key := forecastViewerWeekKey(now)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.week > key {
		return nil
	} // an older request finishing after rollover
	c.rollLocked(key)
	if c.current == nil {
		var raw string
		err := st.queryRow("SELECT v FROM meta WHERE k=?", key).Scan(&raw)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		base := int64(0)
		if err == nil {
			var parseErr error
			base, parseErr = strconv.ParseInt(raw, 10, 64)
			if parseErr != nil || base < 0 {
				return errors.New("invalid weekly viewer count")
			}
		}
		week := &forecastViewerWeek{key: key, base: base, seen: map[[32]byte]struct{}{}}
		if _, err := rand.Read(week.salt[:]); err != nil {
			return err
		}
		c.current = week
	}
	mac := hmac.New(sha256.New, c.current.salt[:])
	mac.Write([]byte(user))
	var digest [32]byte
	copy(digest[:], mac.Sum(nil))
	if _, exists := c.current.seen[digest]; !exists {
		if c.current.base > math.MaxInt64-int64(len(c.current.seen))-1 {
			return errors.New("weekly viewer count overflow")
		}
		c.current.seen[digest] = struct{}{}
	}
	return nil
}

func (c *forecastViewers) flush(st *Store, now time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.rollLocked(forecastViewerWeekKey(now))
	var result error
	for key, count := range c.pending {
		if err := st.SetSetting(key, strconv.FormatInt(count, 10)); err != nil {
			result = err
		} else {
			delete(c.pending, key)
		}
	}
	if c.current != nil {
		count := c.current.base + int64(len(c.current.seen))
		if err := st.SetSetting(c.current.key, strconv.FormatInt(count, 10)); err != nil {
			result = err
		}
	}
	return result
}

func (s *Server) forecastViewerLoop() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	next := time.Now().UTC().Truncate(time.Hour).Add(time.Hour)
	for now := range ticker.C {
		if !s.forecastViewers.flushDue(now, next) {
			continue
		}
		if err := s.forecastViewers.flush(s.st, now); err != nil {
			log.Print("forecast viewer count flush failed")
		}
		next = now.UTC().Truncate(time.Hour).Add(time.Hour)
	}
}

func (c *forecastViewers) flushDue(now, next time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	weekEnded := c.current != nil && c.current.key < forecastViewerWeekKey(now)
	return !now.Before(next) || weekEnded
}
