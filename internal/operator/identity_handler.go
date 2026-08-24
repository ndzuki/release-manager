package operator

import (
	"crypto/x509"
	"net/http"
)

func NewCertificateIdentityHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/operator.v1.OperatorService/CommandStream" &&
			request.URL.Path != "/operator.v1.OperatorService/RenewCertificate" {
			next.ServeHTTP(response, request)
			return
		}
		identity, err := CertIdentityFromRequest(request)
		if err != nil {
			http.Error(response, "client certificate required", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(response, request.WithContext(WithCertificateIdentity(request.Context(), identity)))
	})
}

func verifiedClientCertificate(request *http.Request) *x509.Certificate {
	if request == nil || request.TLS == nil || len(request.TLS.VerifiedChains) == 0 || len(request.TLS.VerifiedChains[0]) == 0 {
		return nil
	}
	return request.TLS.VerifiedChains[0][0]
}
