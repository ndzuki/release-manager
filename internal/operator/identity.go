package operator

import (
	"context"
	"crypto/x509"
	"fmt"
	"net/http"
	"strings"

	"github.com/ndzuki/release-manager/internal/operator/ca"
)

type certificateIdentity struct {
	OperatorName string
	CustomerID   string
	ClusterID    string
	DNSName      string
	Serial       string
}

type identityContextKey struct{}

func WithCertificateIdentity(ctx context.Context, identity certificateIdentity) context.Context {
	return context.WithValue(ctx, identityContextKey{}, identity)
}

func certificateIdentityFromContext(ctx context.Context) (certificateIdentity, bool) {
	identity, ok := ctx.Value(identityContextKey{}).(certificateIdentity)
	return identity, ok
}

func CertIdentityFromRequest(request *http.Request) (certificateIdentity, error) {
	certificate := verifiedClientCertificate(request)
	if certificate == nil {
		return certificateIdentity{}, fmt.Errorf("verified client certificate is required")
	}
	return certIdentity(certificate)
}

func certIdentity(certificate *x509.Certificate) (certificateIdentity, error) {
	if certificate == nil || len(certificate.DNSNames) == 0 {
		return certificateIdentity{}, fmt.Errorf("client certificate SAN is required")
	}
	var parsed certificateIdentity
	for _, dnsName := range certificate.DNSNames {
		labels := strings.Split(strings.ToLower(strings.TrimSpace(dnsName)), ".")
		if len(labels) != 3 || labels[2] != "rm" {
			continue
		}
		if err := validateDNSLabel(labels[0]); err != nil {
			continue
		}
		if err := validateDNSLabel(labels[1]); err != nil {
			continue
		}
		parsed = certificateIdentity{
			OperatorName: certificate.Subject.CommonName,
			ClusterID:    labels[0], CustomerID: labels[1], DNSName: strings.Join(labels, "."),
			Serial: ca.CertSerial(certificate),
		}
		break
	}
	if parsed.DNSName == "" || parsed.OperatorName == "" || parsed.Serial == "" {
		return certificateIdentity{}, fmt.Errorf("client certificate identity is invalid")
	}
	return parsed, nil
}
