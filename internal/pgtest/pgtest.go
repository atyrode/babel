// Package pgtest provisions throwaway PostgreSQL clusters for tests that need
// a real server rather than a fake: transactional DDL, server-authoritative
// time, information_schema reflection, and TLS negotiation are all things a
// stand-in reproduces dishonestly.
//
// It exists because two test binaries need the same provisioning. The
// sharedcatalog suite drives the library directly and can speak plaintext to a
// loopback cluster; the end-to-end suite drives Babel's shipped commands, and
// those build their DSN from a configuration document that rejects
// `sslmode=disable` outright (SPEC.md §9: shared mode always encrypts). A
// CLI-level test therefore needs a cluster that actually serves TLS, which is
// what Options.TLS provisions with a self-signed certificate.
//
// `sslmode=require` encrypts without verifying the certificate, so a self-signed
// pair is sufficient and no trust store is involved. That is deliberately the
// same posture the first real deployment runs under: Clever Cloud's managed
// PostgreSQL presents a self-signed certificate with no subject alternative
// name, so `verify-full` is impossible there (SPEC.md decision 48). A test that
// demanded a verifiable chain would be testing a configuration Babel cannot
// currently deploy.
package pgtest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Options selects what a cluster must serve.
type Options struct {
	// TLS provisions a self-signed certificate and starts the server with
	// ssl=on, which is required by any test that reaches PostgreSQL through
	// Babel's own configuration document.
	TLS bool
}

// Cluster is one running throwaway server. Its lifetime is one test binary; it
// holds only synthetic rows and trusts loopback connections.
type Cluster struct {
	// BaseDSN connects to the cluster's initial `postgres` database as the
	// superuser the harness created. Tests that want isolation create their own
	// databases from it.
	BaseDSN string

	// Host and Port address the server for callers that must build their own
	// connection parameters, as Babel's configuration document does rather than
	// accepting a DSN.
	Host string
	Port int

	// User is the role initdb created, and the one BaseDSN authenticates as.
	User string

	dir     string
	dataDir string
}

// Available reports whether a cluster can be provisioned here, so a caller can
// skip honestly instead of failing on a machine without PostgreSQL.
func Available() bool {
	if _, err := exec.LookPath("initdb"); err != nil {
		return false
	}
	_, err := exec.LookPath("pg_ctl")
	return err == nil
}

// Start provisions and starts a cluster. The returned Cluster must be stopped
// by the caller; Stop is safe to call more than once.
func Start(opts Options) (*Cluster, error) {
	dir, err := os.MkdirTemp("", "babel-pg-*")
	if err != nil {
		return nil, err
	}
	c := &Cluster{
		Host:    "127.0.0.1",
		User:    "babel",
		dir:     dir,
		dataDir: filepath.Join(dir, "data"),
	}

	// Trust auth on loopback with a private data directory. The cluster is
	// unreachable from outside this machine and is destroyed with its temporary
	// directory.
	init := exec.Command("initdb", "-A", "trust", "-U", c.User, "--no-sync", "-D", c.dataDir)
	if out, err := init.CombinedOutput(); err != nil {
		c.Stop()
		return nil, fmt.Errorf("initdb: %v: %s", err, out)
	}

	sslMode := "disable"
	args := []string{"-h", c.Host, "-k", dir}
	if opts.TLS {
		if err := c.writeCertificate(); err != nil {
			c.Stop()
			return nil, err
		}
		// PostgreSQL requires the key to be unreadable by anyone else and owned
		// by the server user, and refuses to start otherwise.
		args = append(args,
			"-c", "ssl=on",
			"-c", "ssl_cert_file="+filepath.Join(c.dataDir, "server.crt"),
			"-c", "ssl_key_file="+filepath.Join(c.dataDir, "server.key"),
		)
		sslMode = "require"
	}

	// A free port is probed rather than derived from the process id: two test
	// binaries provisioning clusters concurrently is the normal case under
	// `go test ./...`, and arithmetic on pids collides silently.
	var startErr error
	for range 3 {
		port, err := freePort()
		if err != nil {
			c.Stop()
			return nil, err
		}
		c.Port = port
		// A fresh slice per attempt: appending to args would reuse its backing
		// array and carry the previous attempt's port into this one.
		full := append(slices.Clone(args), "-p", strconv.Itoa(port))
		start := exec.Command("pg_ctl", "-D", c.dataDir,
			"-o", strings.Join(full, " "), "-l", filepath.Join(dir, "log"), "-w", "start")
		out, err := start.CombinedOutput()
		if err == nil {
			startErr = nil
			break
		}
		startErr = fmt.Errorf("pg_ctl start: %v: %s", err, out)
	}
	if startErr != nil {
		c.Stop()
		return nil, startErr
	}

	c.BaseDSN = fmt.Sprintf("postgres://%s@%s:%d/postgres?sslmode=%s",
		c.User, c.Host, c.Port, sslMode)
	return c, nil
}

// Stop terminates the server and removes its data directory. It is called on
// every failure path in Start, so it tolerates a cluster that never ran.
func (c *Cluster) Stop() {
	if c == nil || c.dir == "" {
		return
	}
	if _, err := os.Stat(filepath.Join(c.dataDir, "postmaster.pid")); err == nil {
		exec.Command("pg_ctl", "-D", c.dataDir, "-m", "immediate", "-w", "stop").Run()
	}
	os.RemoveAll(c.dir)
	c.dir = ""
}

// Log returns the server log, which is the only place a refused startup or a
// rejected TLS handshake explains itself.
func (c *Cluster) Log() string {
	b, err := os.ReadFile(filepath.Join(c.dir, "log"))
	if err != nil {
		return ""
	}
	return string(b)
}

// writeCertificate generates a self-signed pair for the loopback address. The
// common name is unverifiable by design: `sslmode=require` does not check it,
// and requiring a verifiable chain would test a deployment shape Babel does not
// have.
func (c *Cluster) writeCertificate() error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("generate key: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return fmt.Errorf("generate serial: %w", err)
	}
	tmpl := x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "babel-pgtest"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		return fmt.Errorf("create certificate: %w", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.WriteFile(filepath.Join(c.dataDir, "server.crt"), certPEM, 0o600); err != nil {
		return err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return fmt.Errorf("marshal key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return os.WriteFile(filepath.Join(c.dataDir, "server.key"), keyPEM, 0o600)
}

// freePort asks the kernel for an unused loopback port. The listener is closed
// before PostgreSQL binds it, so this narrows the race rather than eliminating
// it; Start retries.
func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}
