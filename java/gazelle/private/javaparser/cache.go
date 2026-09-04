package javaparser

import (
	"bytes"
	"compress/flate"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	pb "github.com/bazel-contrib/rules_jvm/java/gazelle/private/javaparser/proto/gazelle/java/javaparser/v0"
	"google.golang.org/protobuf/proto"
)

var cacheCompressors = sync.Pool{New: func() any {
	writer, _ := flate.NewWriter(io.Discard, flate.DefaultCompression)
	return writer
}}

type cacheKey [sha256.Size]byte
type cacheEntry struct {
	key  cacheKey
	used int64
	data []byte
}
type parseCache struct {
	dir, root, identity string
	limit               int64
	mu                  sync.Mutex
	entries             map[cacheKey]cacheEntry
	started             time.Time
	lastKeepAlive       atomic.Int64
	hits, misses        atomic.Uint64
}

const cacheHeaderSize = 4 + sha256.Size
const cacheEntryHeaderSize = sha256.Size + 8 + 4

func openParseCache(dir, root, identity string, budget int64) (*parseCache, error) {
	if budget/2 < cacheHeaderSize {
		return nil, fmt.Errorf("parser cache budget is too small")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	c := &parseCache{dir: dir, root: root, identity: identity, limit: budget / 2, started: time.Now()}
	c.entries = c.read()
	return c, nil
}

// Hash the bytes, not timestamps: checkouts and editors can preserve file times.
func (c *parseCache) key(request *pb.ParsePackageRequest) (cacheKey, error) {
	var key cacheKey
	if len(request.Files) == 0 {
		return key, fmt.Errorf("empty parser request")
	}
	h := sha256.New()
	for _, s := range []string{c.identity, request.Rel} {
		_ = binary.Write(h, binary.LittleEndian, uint64(len(s)))
		_, _ = io.WriteString(h, s)
	}
	for _, name := range request.Files {
		_ = binary.Write(h, binary.LittleEndian, uint64(len(name)))
		_, _ = io.WriteString(h, name)
		f, err := os.Open(filepath.Join(c.root, request.Rel, name))
		if err != nil {
			return key, err
		}
		content := sha256.New()
		_, err = io.Copy(content, f)
		closeErr := f.Close()
		if err != nil {
			return key, err
		}
		if closeErr != nil {
			return key, closeErr
		}
		_, _ = h.Write(content.Sum(nil))
	}
	copy(key[:], h.Sum(nil))
	return key, nil
}

func (c *parseCache) get(key cacheKey) (*pb.Package, bool) {
	c.mu.Lock()
	entry, ok := c.entries[key]
	if ok {
		entry.used = time.Now().UnixNano()
		c.entries[key] = entry
	}
	c.mu.Unlock()
	if ok && len(entry.data) <= 64<<20 {
		result := new(pb.Package)
		if proto.Unmarshal(entry.data, result) == nil {
			c.hits.Add(1)
			return result, true
		}
	}
	c.misses.Add(1)
	return nil, false
}

func (c *parseCache) put(key cacheKey, metadata *pb.Package) {
	data, err := proto.Marshal(metadata)
	if err != nil || len(data) > 64<<20 {
		return
	}
	entry := cacheEntry{key: key, used: time.Now().UnixNano(), data: data}
	if int64(cacheHeaderSize+cacheEntryHeaderSize+len(data)) > c.limit {
		if _, ok := encodeCache([]cacheEntry{entry}, c.limit); !ok {
			return
		}
	}
	c.mu.Lock()
	c.entries[key] = entry
	c.mu.Unlock()
}

// Bound decompression independently of the disk budget.
const maxCacheExpandedSize = 256 << 20

func (c *parseCache) read() map[cacheKey]cacheEntry {
	entries := make(map[cacheKey]cacheEntry)
	f, err := os.Open(filepath.Join(c.dir, "metadata"))
	if err != nil {
		return entries
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, c.limit+1))
	if err != nil || int64(len(data)) > c.limit || len(data) < cacheHeaderSize || string(data[:4]) != "JPC2" {
		return entries
	}
	packed := data[cacheHeaderSize:]
	checksum := sha256.Sum256(packed)
	if !bytes.Equal(data[4:cacheHeaderSize], checksum[:]) {
		return entries
	}
	reader := flate.NewReader(bytes.NewReader(packed))
	defer reader.Close()
	payload, err := io.ReadAll(io.LimitReader(reader, maxCacheExpandedSize+1))
	if err != nil || len(payload) > maxCacheExpandedSize {
		return entries
	}
	for len(payload) > 0 {
		if len(payload) < cacheEntryHeaderSize {
			return make(map[cacheKey]cacheEntry)
		}
		var key cacheKey
		copy(key[:], payload[:sha256.Size])
		used := int64(binary.LittleEndian.Uint64(payload[sha256.Size:]))
		size := uint64(binary.LittleEndian.Uint32(payload[sha256.Size+8:]))
		payload = payload[cacheEntryHeaderSize:]
		if size > uint64(len(payload)) {
			return make(map[cacheKey]cacheEntry)
		}
		entries[key] = cacheEntry{key: key, used: used, data: payload[:size]}
		payload = payload[size:]
	}
	return entries
}

// One compression stream shares repeated class names across package boundaries.
func encodeCache(entries []cacheEntry, limit int64) ([]byte, bool) {
	var packed bytes.Buffer
	writer := cacheCompressors.Get().(*flate.Writer)
	defer cacheCompressors.Put(writer)
	writer.Reset(&packed)
	defer writer.Close()
	var expanded int64
	for _, entry := range entries {
		expanded += int64(cacheEntryHeaderSize + len(entry.data))
		if expanded > maxCacheExpandedSize {
			return nil, false
		}
		_, _ = writer.Write(entry.key[:])
		_ = binary.Write(writer, binary.LittleEndian, uint64(entry.used))
		_ = binary.Write(writer, binary.LittleEndian, uint32(len(entry.data)))
		_, _ = writer.Write(entry.data)
		if int64(cacheHeaderSize+packed.Len()) > limit {
			return nil, false
		}
	}
	if writer.Close() != nil || int64(cacheHeaderSize+packed.Len()) > limit {
		return nil, false
	}
	checksum := sha256.Sum256(packed.Bytes())
	result := append([]byte("JPC2"), checksum[:]...)
	return append(result, packed.Bytes()...), true
}

func (c *parseCache) flush() error {
	// A single writer also bounds temporary storage. Crashes release the OS lock.
	unlock, locked, err := lockCache(filepath.Join(c.dir, "lock"))
	if err != nil || !locked {
		return err
	}
	defer unlock()
	c.mu.Lock()
	defer c.mu.Unlock()
	entries := c.read()
	for key, entry := range c.entries {
		if old, ok := entries[key]; !ok || entry.used > old.used {
			entries[key] = entry
		}
	}
	ordered := make([]cacheEntry, 0, len(entries))
	for _, entry := range entries {
		ordered = append(ordered, entry)
	}
	slices.SortFunc(ordered, func(a, b cacheEntry) int {
		if a.used > b.used {
			return -1
		}
		if a.used < b.used {
			return 1
		}
		return bytes.Compare(a.key[:], b.key[:])
	})
	var data []byte
	for {
		var fits bool
		data, fits = encodeCache(ordered, c.limit)
		if fits {
			break
		}
		// Drop the oldest entries in chunks to bound recompression work.
		if len(ordered) == 0 {
			return fmt.Errorf("parser cache budget is too small")
		}
		ordered = ordered[:len(ordered)*9/10]
	}
	// A smaller configured budget must also bound replacement of an older snapshot.
	path := filepath.Join(c.dir, "metadata")
	if info, err := os.Stat(path); err == nil && info.Size() > c.limit {
		if err := os.Remove(path); err != nil {
			return err
		}
	}
	tmp := filepath.Join(c.dir, "metadata.tmp")
	defer os.Remove(tmp)
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	c.entries = make(map[cacheKey]cacheEntry, len(ordered))
	for _, entry := range ordered {
		c.entries[entry.key] = entry
	}
	return nil
}

// EnableCache is optional: an unavailable cache must not prevent generation.
func (r *Runner) EnableCache(root, dir string, budget int64) {
	if budget == 0 {
		return
	}
	cache, err := r.openCache(root, dir, budget)
	if err != nil {
		r.logger.Debug().Err(err).Msg("parser cache unavailable; parsing normally")
		return
	}
	r.cache = cache
}

func (r Runner) openCache(root, dir string, budget int64) (*parseCache, error) {
	if dir == "" {
		base, err := os.UserCacheDir()
		if err != nil {
			return nil, err
		}
		dir = filepath.Join(base, "gazelle-jvm")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	identity, err := r.rpc.CacheIdentity(ctx, &pb.CacheIdentityRequest{})
	if err != nil {
		return nil, err
	}
	if identity.GetIdentity() == "" {
		return nil, fmt.Errorf("empty parser identity")
	}
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	f, err := os.Open(executable)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return nil, err
	}
	return openParseCache(dir, root, fmt.Sprintf("%s:%x", identity.GetIdentity(), h.Sum(nil)), budget)
}

func (r *Runner) FlushCache() {
	if r.cache == nil {
		return
	}
	if err := r.cache.flush(); err != nil {
		r.logger.Debug().Err(err).Msg("could not save parser cache")
	}
	r.logger.Info().Uint64("hits", r.cache.hits.Load()).Uint64("misses", r.cache.misses.Load()).Msg("parser cache")
}

func (r Runner) parseOnDemand(ctx context.Context, request *pb.ParsePackageRequest) (*pb.Package, error) {
	if r.cache == nil {
		return r.parseUncached(ctx, request)
	}
	key, keyErr := r.cache.key(request)
	if keyErr == nil {
		if metadata, ok := r.cache.get(key); ok {
			if err := r.keepParserAlive(ctx); err != nil {
				return nil, err
			}
			return metadata, nil
		}
	}
	metadata, err := r.parseUncached(ctx, request)
	if err == nil && keyErr == nil {
		// Do not publish metadata under a fingerprint taken before an editor changed the inputs.
		if after, e := r.cache.key(request); e == nil && after == key {
			r.cache.put(key, metadata)
		}
	}
	return metadata, err
}

func (r Runner) parseBatch(ctx context.Context, batch []*pb.ParsePackageRequest) (*pb.ParseJavaPackagesResponse, error) {
	if r.cache == nil {
		return r.parseBatchUncached(ctx, batch)
	}
	result := &pb.ParseJavaPackagesResponse{Packages: make(map[string]*pb.Package)}
	var missing []*pb.ParsePackageRequest
	keys := make(map[string]cacheKey)
	for _, request := range batch {
		key, err := r.cache.key(request)
		if err == nil {
			if metadata, ok := r.cache.get(key); ok {
				result.Packages[request.Rel] = metadata
				continue
			}
			keys[request.Rel] = key
		}
		missing = append(missing, request)
	}
	if len(missing) == 0 {
		if err := r.keepParserAlive(ctx); err != nil {
			return nil, err
		}
		return result, nil
	}
	response, err := r.parseBatchUncached(ctx, missing)
	if err != nil {
		return nil, err
	}
	result.Errors = response.GetErrors()
	for _, request := range missing {
		metadata := response.GetPackages()[request.Rel]
		if metadata == nil {
			continue
		}
		result.Packages[request.Rel] = metadata
		if before, ok := keys[request.Rel]; ok {
			if after, e := r.cache.key(request); e == nil && after == before {
				r.cache.put(before, metadata)
			}
		}
	}
	return result, nil
}
