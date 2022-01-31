package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"github.com/araddon/dateparse"
	"github.com/dgraph-io/ristretto"
	"golang.org/x/sync/singleflight"
)

const (
	cacheLoggedInKey   contextKey = "cacheLoggedIn"
	cacheExpirationKey contextKey = "cacheExpiration"
)

type cache struct {
	g singleflight.Group
	c *ristretto.Cache
}

func (a *goBlog) initCache() (err error) {
	a.cache = &cache{}
	if a.cfg.Cache != nil && !a.cfg.Cache.Enable {
		// Cache disabled
		return nil
	}
	a.cache.c, err = ristretto.NewCache(&ristretto.Config{
		NumCounters: 40 * 1000,        // 4000 items when full with 5 KB items -> x10 = 40.000
		MaxCost:     20 * 1000 * 1000, // 20 MB
		BufferItems: 64,               // recommended
	})
	return
}

func cacheLoggedIn(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), cacheLoggedInKey, true)))
	})
}

func (a *goBlog) cacheMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Do checks
		if a.cache.c == nil || !cacheable(r) {
			next.ServeHTTP(w, r)
			return
		}
		// Check login
		if cli, ok := r.Context().Value(cacheLoggedInKey).(bool); ok && cli {
			// Continue caching, but remove login
			setLoggedIn(r, false)
		} else {
			if a.isLoggedIn(r) {
				// Don't cache logged in requests
				next.ServeHTTP(w, r)
				return
			}
		}
		// Search and serve cache
		key := cacheKey(r)
		// Get cache or render it
		cacheInterface, _, _ := a.cache.g.Do(key, func() (interface{}, error) {
			return a.cache.getCache(key, next, r), nil
		})
		ci := cacheInterface.(*cacheItem)
		// copy and set headers
		a.setCacheHeaders(w, ci)
		// check conditional request
		if ifNoneMatchHeader := r.Header.Get("If-None-Match"); ifNoneMatchHeader != "" && ifNoneMatchHeader == ci.eTag {
			// send 304
			w.WriteHeader(http.StatusNotModified)
			return
		}
		if ifModifiedSinceHeader := r.Header.Get("If-Modified-Since"); ifModifiedSinceHeader != "" {
			if t, err := dateparse.ParseAny(ifModifiedSinceHeader); err == nil && t.After(ci.creationTime) {
				// send 304
				w.WriteHeader(http.StatusNotModified)
				return
			}
		}
		// set status code
		w.WriteHeader(ci.code)
		// write cached body
		_, _ = w.Write(ci.body)
	})
}

func cacheable(r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	if r.URL.Query().Get("cache") == "0" || r.URL.Query().Get("cache") == "false" {
		return false
	}
	return true
}

func cacheKey(r *http.Request) string {
	var buf strings.Builder
	// Special cases
	if asRequest, ok := r.Context().Value(asRequestKey).(bool); ok && asRequest {
		buf.WriteString("as-")
	}
	if torUsed, ok := r.Context().Value(torUsedKey).(bool); ok && torUsed {
		buf.WriteString("tor-")
	}
	// Add cache URL
	_, _ = buf.WriteString(r.URL.EscapedPath())
	if q := r.URL.Query(); len(q) > 0 {
		_ = buf.WriteByte('?')
		_, _ = buf.WriteString(q.Encode())
	}
	// Return string
	return buf.String()
}

func (a *goBlog) setCacheHeaders(w http.ResponseWriter, cache *cacheItem) {
	// Copy headers
	for k, v := range cache.header.Clone() {
		w.Header()[k] = v
	}
	// Set cache headers
	w.Header().Set("ETag", cache.eTag)
	w.Header().Set("Last-Modified", cache.creationTime.UTC().Format(http.TimeFormat))
	if w.Header().Get("Cache-Control") == "" {
		if cache.expiration != 0 {
			w.Header().Set("Cache-Control", fmt.Sprintf("public,max-age=%d,stale-while-revalidate=%d", cache.expiration, cache.expiration))
		} else {
			exp := a.cfg.Cache.Expiration
			w.Header().Set("Cache-Control", fmt.Sprintf("public,max-age=%d,s-max-age=%d,stale-while-revalidate=%d", exp, exp/3, exp))
		}
	}
}

type cacheItem struct {
	expiration   int
	creationTime time.Time
	eTag         string
	code         int
	header       http.Header
	body         []byte
}

// Calculate byte size of cache item using size of body and header
func (ci *cacheItem) cost() int64 {
	var headerBuf strings.Builder
	_ = ci.header.Write(&headerBuf)
	headerSize := int64(binary.Size(headerBuf.String()))
	bodySize := int64(binary.Size(ci.body))
	return headerSize + bodySize
}

func (c *cache) getCache(key string, next http.Handler, r *http.Request) (item *cacheItem) {
	if rItem, ok := c.c.Get(key); ok {
		item = rItem.(*cacheItem)
	}
	if item == nil {
		// No cache available
		// Make and use copy of r
		cr := r.Clone(valueOnlyContext{r.Context()})
		// Remove problematic headers
		cr.Header.Del("If-Modified-Since")
		cr.Header.Del("If-Unmodified-Since")
		cr.Header.Del("If-None-Match")
		cr.Header.Del("If-Match")
		cr.Header.Del("If-Range")
		cr.Header.Del("Range")
		// Record request
		recorder := httptest.NewRecorder()
		next.ServeHTTP(recorder, cr)
		// Cache values from recorder
		result := recorder.Result()
		body, _ := io.ReadAll(result.Body)
		_ = result.Body.Close()
		eTag := result.Header.Get("ETag")
		if eTag == "" {
			eTag, _ = getSHA256(bytes.NewReader(body))
		}
		lastMod := time.Now()
		if lm := result.Header.Get("Last-Modified"); lm != "" {
			if parsedTime, te := dateparse.ParseLocal(lm); te == nil {
				lastMod = parsedTime
			}
		}
		// Remove problematic headers
		result.Header.Del("Accept-Ranges")
		result.Header.Del("ETag")
		result.Header.Del("Last-Modified")
		// Create cache item
		exp, _ := cr.Context().Value(cacheExpirationKey).(int)
		item = &cacheItem{
			expiration:   exp,
			creationTime: lastMod,
			eTag:         eTag,
			code:         result.StatusCode,
			header:       result.Header,
			body:         body,
		}
		// Save cache
		if cch := item.header.Get("Cache-Control"); !containsStrings(cch, "no-store", "private", "no-cache") {
			if exp == 0 {
				c.c.Set(key, item, item.cost())
			} else {
				c.c.SetWithTTL(key, item, item.cost(), time.Duration(exp)*time.Second)
			}
		}
	}
	return item
}

func (c *cache) purge() {
	c.c.Clear()
}

func (a *goBlog) defaultCacheExpiration() int {
	if a.cfg.Cache != nil {
		return a.cfg.Cache.Expiration
	}
	return 0
}
