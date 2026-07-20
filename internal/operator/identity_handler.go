package operator

import (
	"crypto/x509"
	"net/http"
)

func NewCertificateIdentityHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/operator.v1.OperatorService/CommandStream" {
			next.ServeHTTP(w, r)
			return
		}

		certificate := verifiedClientCertificate(r)
		if certificate == nil {
			http.Error(w, "client certificate required", http.StatusUnauthorized)
			return
		}

		ctx := WithCertificateIdentity(r.Context(), certificate.SerialNumber.String())
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func verifiedClientCertificate(r *http.Request) *x509.Certificate {
	if r.TLS == nil || len(r.TLS.VerifiedChains) == 0 || len(r.TLS.VerifiedChains[0]) == 0 {
		return nil
	}
	return r.TLS.VerifiedChains[0][0]
}
