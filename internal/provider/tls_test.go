package provider

import (
	"crypto/tls"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewSecureAPITransportTrustsExplicitCAWithoutDisablingVerification(t *testing.T) {
	t.Parallel()
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)

	caFile := filepath.Join(t.TempDir(), "ca.pem")
	certificate := server.Certificate()
	require.NoError(t, os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Raw}), 0o600))
	transport, err := newSecureAPITransport(caFile)
	require.NoError(t, err)
	client := &http.Client{Transport: transport}

	response, err := client.Get(server.URL)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, response.Body.Close()) })
	require.Equal(t, http.StatusNoContent, response.StatusCode)
	require.False(t, transport.TLSClientConfig.InsecureSkipVerify)
}

func TestNewSecureAPITransportRejectsInvalidBundle(t *testing.T) {
	t.Parallel()
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	require.NoError(t, os.WriteFile(caFile, []byte("not a certificate"), 0o600))
	_, err := newSecureAPITransport(caFile)
	require.ErrorContains(t, err, "no valid PEM certificates")
}

func TestNewSecureAPITransportRejectsUnreadableOrIrregularBundles(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	_, err := newSecureAPITransport(directory)
	require.ErrorContains(t, err, "regular file")
	_, err = newSecureAPITransport(filepath.Join(directory, "missing.pem"))
	require.ErrorContains(t, err, "inspect CA file")
}

func TestNewSecureAPITransportDefaultsToSystemRoots(t *testing.T) {
	t.Parallel()
	transport, err := newSecureAPITransport("")
	require.NoError(t, err)
	require.Nil(t, transport.TLSClientConfig.RootCAs, "nil RootCAs means the operating-system trust store")
	require.Equal(t, uint16(tls.VersionTLS12), transport.TLSClientConfig.MinVersion)
	require.False(t, transport.TLSClientConfig.InsecureSkipVerify)
}
