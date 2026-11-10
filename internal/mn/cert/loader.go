package cert

import (
	"crypto/tls"
	"fmt"
	"os"
	"sync"
	"time"
)

// Loader serves the certificate currently on disk, reloading it when the files change.
//
// The server terminates TLS itself, so a renewal has to reach the running process without a restart:
// `tls.Config.GetCertificate` asks this loader on every handshake, and a certificate the renewal loop
// just replaced is picked up on the next connection. A file that has not changed is not re-read, so a
// busy server does not pay for this.
type Loader struct {
	certPath string
	keyPath  string

	mu     sync.Mutex
	pair   *tls.Certificate
	stamp  string
	loaded time.Time
}

// NewLoader returns a loader for one certificate/key pair.
func NewLoader(certPath, keyPath string) *Loader {
	return &Loader{certPath: certPath, keyPath: keyPath}
}

// GetCertificate implements tls.Config.GetCertificate.
func (l *Loader) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	return l.current()
}

// current returns the loaded pair, re-reading it when the files changed or nothing is loaded yet.
func (l *Loader) current() (*tls.Certificate, error) {
	stamp, err := l.filesStamp()
	if err != nil {
		// A missing or unreadable file is not a reason to drop a certificate that already works: a
		// renewal that half-finished, or a permissions problem, must not take the server down.
		l.mu.Lock()
		defer l.mu.Unlock()
		if l.pair != nil {
			return l.pair, nil
		}
		return nil, err
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	if l.pair != nil && stamp == l.stamp {
		return l.pair, nil
	}
	pair, err := tls.LoadX509KeyPair(l.certPath, l.keyPath)
	if err != nil {
		if l.pair != nil {
			return l.pair, nil
		}
		return nil, fmt.Errorf("cert: load %s: %w", l.certPath, err)
	}
	l.pair, l.stamp, l.loaded = &pair, stamp, time.Now()
	return l.pair, nil
}

// filesStamp identifies the pair on disk without reading it: both files' size and modification time.
// A renewal rewrites both, so a stamp that did not change means the pair did not change either.
func (l *Loader) filesStamp() (string, error) {
	certInfo, err := os.Stat(l.certPath)
	if err != nil {
		return "", err
	}
	keyInfo, err := os.Stat(l.keyPath)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%d:%d:%d:%d", certInfo.Size(), certInfo.ModTime().UnixNano(),
		keyInfo.Size(), keyInfo.ModTime().UnixNano()), nil
}
