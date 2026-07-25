package testutil

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"

	"github.com/memohai/connect-it/packages/core/providerkit"
)

const testProviderHost = "provider.test"

// ProviderServer exposes a local HTTPS handler through the exact resolver,
// DNS/IP pinning, TLS verification, origin and bounded-response path used in
// production. No package-level transport or public network is involved.
type ProviderServer struct {
	Server  *httptest.Server
	Factory *providerkit.Factory
	BaseURL string
	Origin  string
}

func NewProviderServer(t testing.TB, handler http.Handler) *ProviderServer {
	t.Helper()
	if handler == nil {
		t.Fatal("test provider handler must not be nil")
	}

	certificate, roots := testProviderCertificate(t, testProviderHost)
	server := httptest.NewUnstartedServer(handler)
	server.TLS = &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{certificate},
	}
	_, port, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		server.Close()
		t.Fatalf("test provider listener: %v", err)
	}
	server.StartTLS()
	t.Cleanup(server.Close)

	target := server.Listener.Addr().String()
	dialer := &net.Dialer{}
	factory := providerkit.NewFactory(
		providerkit.WithRootCAs(roots),
		providerkit.WithResolver(publicResolver{}),
		providerkit.WithDialContext(func(
			ctx context.Context,
			network string,
			_ string,
		) (net.Conn, error) {
			return dialer.DialContext(ctx, network, target)
		}),
	)
	origin := "https://" + net.JoinHostPort(testProviderHost, port)
	return &ProviderServer{
		Server:  server,
		Factory: factory,
		BaseURL: origin,
		Origin:  origin,
	}
}

type publicResolver struct{}

func (publicResolver) LookupNetIP(
	context.Context,
	string,
	string,
) ([]netip.Addr, error) {
	return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
}

func testProviderCertificate(
	t testing.TB,
	hostname string,
) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	now := time.Now()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "connect-it test CA"},
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	caDER, err := x509.CreateCertificate(
		rand.Reader,
		caTemplate,
		caTemplate,
		&caKey.PublicKey,
		caKey,
	)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}

	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leafTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: hostname},
		DNSNames:     []string{hostname, "*." + hostname},
		NotBefore:    now.Add(-time.Minute),
		NotAfter:     now.Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	leafDER, err := x509.CreateCertificate(
		rand.Reader,
		leafTemplate,
		ca,
		&leafKey.PublicKey,
		caKey,
	)
	if err != nil {
		t.Fatal(err)
	}
	leafKeyDER, err := x509.MarshalPKCS8PrivateKey(leafKey)
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := tls.X509KeyPair(
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: leafKeyDER}),
	)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	return certificate, roots
}
