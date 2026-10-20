package landing

import (
	"example.com/exacc-log-shipper/internal/config"
	"example.com/exacc-log-shipper/internal/securefile"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

type Store interface {
	Put(key string, data []byte) error
	Get(key string) ([]byte, error)
	List(prefix string) ([]string, error)
}

func Open(s config.Storage) (Store, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	if s.Backend == "filesystem" {
		return &local{root: s.RootDir}, nil
	}
	return openS3(s)
}
func ValidKey(k string) error {
	if k == "" || strings.HasPrefix(k, "/") || strings.Contains(k, "\\") || strings.Contains(k, "//") || path.Clean(k) != k || strings.HasPrefix(k, "../") || strings.Contains(k, "/../") || strings.HasSuffix(k, "/..") || strings.ContainsAny(k, "\x00\r\n") {
		return fmt.Errorf("unsafe storage object key: %q", k)
	}
	return nil
}

type local struct{ root string }

func (l *local) path(k string) (string, error) {
	if err := ValidKey(k); err != nil {
		return "", err
	}
	return filepath.Join(l.root, filepath.FromSlash(k)), nil
}
func (l *local) Put(k string, data []byte) error {
	p, err := l.path(k)
	if err != nil {
		return err
	}
	return securefile.Write(p, data, 0600)
}
func (l *local) Get(k string) ([]byte, error) {
	p, err := l.path(k)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(p)
}
func (l *local) List(prefix string) ([]string, error) {
	var result []string
	err := filepath.WalkDir(l.root, func(p string, entry fs.DirEntry, e error) error {
		if os.IsNotExist(e) {
			return nil
		}
		if e != nil {
			return e
		}
		if entry.IsDir() || !entry.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(l.root, p)
		if err != nil {
			return err
		}
		key := filepath.ToSlash(rel)
		if strings.HasPrefix(key, prefix) {
			result = append(result, key)
		}
		return nil
	})
	if os.IsNotExist(err) {
		return []string{}, nil
	}
	sort.Strings(result)
	return result, err
}
