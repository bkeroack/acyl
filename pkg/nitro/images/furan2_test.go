package images

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"testing"
	"time"

	"github.com/dollarshaveclub/acyl/pkg/ghclient"
	"github.com/dollarshaveclub/acyl/pkg/metrics"
	"github.com/dollarshaveclub/acyl/pkg/persistence"
	"github.com/dollarshaveclub/furan/v2/pkg/generated/furanrpc"
	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

type FakeFuran2Server struct {
	status *furanrpc.BuildStatusResponse
}

var _ furanrpc.FuranExecutorServer = &FakeFuran2Server{}

func (ffs *FakeFuran2Server) StartBuild(context.Context, *furanrpc.BuildRequest) (*furanrpc.BuildRequestResponse, error) {
	return &furanrpc.BuildRequestResponse{
		BuildId: uuid.Must(uuid.NewRandom()).String(),
	}, nil
}

func (ffs *FakeFuran2Server) MonitorBuild(*furanrpc.BuildStatusRequest, furanrpc.FuranExecutor_MonitorBuildServer) error {
	return nil
}

func (ffs *FakeFuran2Server) GetBuildStatus(context.Context, *furanrpc.BuildStatusRequest) (*furanrpc.BuildStatusResponse, error) {
	return ffs.status, nil
}

func (ffs *FakeFuran2Server) CancelBuild(context.Context, *furanrpc.BuildCancelRequest) (*furanrpc.BuildCancelResponse, error) {
	return &furanrpc.BuildCancelResponse{}, nil
}

func (ffs *FakeFuran2Server) ListBuilds(context.Context, *furanrpc.ListBuildsRequest) (*furanrpc.ListBuildsResponse, error) {
	return &furanrpc.ListBuildsResponse{}, nil
}

func (ffs *FakeFuran2Server) GetBuildEvents(_ context.Context, req *furanrpc.BuildStatusRequest) (*furanrpc.BuildEventsResponse, error) {
	return &furanrpc.BuildEventsResponse{
		BuildId:      req.BuildId,
		CurrentState: furanrpc.BuildState_SUCCESS,
		Messages:     []string{"foo", "bar", "baz"},
	}, nil
}

func randomTLSCert() (*tls.Certificate, error) {
	bits := 4096
	privateKey, err := rsa.GenerateKey(rand.Reader, bits)
	if err != nil {
		return nil, fmt.Errorf("rsa.GenerateKey: %w", err)
	}

	tpl := x509.Certificate{
		SerialNumber:          big.NewInt(1),
		DNSNames:              []string{"foobar.com"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore:             time.Now().UTC(),
		NotAfter:              time.Now().UTC().AddDate(2, 0, 0),
		BasicConstraintsValid: true,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
	}
	derCert, err := x509.CreateCertificate(rand.Reader, &tpl, &tpl, &privateKey.PublicKey, privateKey)
	if err != nil {
		return nil, fmt.Errorf("x509.CreateCertificate: %w", err)
	}

	pemCert := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: derCert,
	})

	pemKey := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(privateKey),
	})

	cert, err := tls.X509KeyPair(pemCert, pemKey)
	if err != nil {
		return nil, fmt.Errorf("X509KeyPair: %w", err)
	}

	return &cert, nil
}

func TestFuran2ImageBackendBuildImage(t *testing.T) {
	cert, err := randomTLSCert()
	if err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	s := grpc.NewServer(grpc.Creds(credentials.NewServerTLSFromCert(cert)))
	furanrpc.RegisterFuranExecutorServer(s, &FakeFuran2Server{
		status: &furanrpc.BuildStatusResponse{
			State: furanrpc.BuildState_SUCCESS,
		},
	})
	go func() {
		s.Serve(l)
	}()
	defer s.Stop()

	dl := persistence.NewFakeDataLayer()
	mc := &metrics.FakeCollector{}
	rc := &ghclient.FakeRepoAppClient{}

	fbb, err := NewFuran2BuilderBackend(l.Addr().String(), "asdf", 1, true, dl, rc, mc)
	if err != nil {
		t.Fatalf("error creating backend: %v", err)
	}

	err = fbb.BuildImage(context.Background(), "something-random", "acme-foo", "acme/foo", "quay.io/foo/bar", "master", BuildOptions{})
	if err != nil {
		t.Fatalf("failed: %v", err)
	}
}
