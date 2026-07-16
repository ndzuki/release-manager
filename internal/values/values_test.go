package values

import (
	"bytes"
	"testing"
)

func TestCanonicalize_EquivalentYAMLProducesIdenticalJSON(t *testing.T) {
	yamlA := []byte(`
key1: value1
key2:
  sub: hello
`)
	yamlB := []byte(`
key2:
  sub: hello
key1: value1
`)

	a, err := Canonicalize(yamlA)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Canonicalize(yamlB)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(a, b) {
		t.Errorf("canonical forms differ:\nA: %s\nB: %s", a, b)
	}
}

func TestCanonicalize_DifferentValuesProduceDifferentJSON(t *testing.T) {
	yamlA := []byte(`key: valueA`)
	yamlB := []byte(`key: valueB`)

	a, err := Canonicalize(yamlA)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Canonicalize(yamlB)
	if err != nil {
		t.Fatal(err)
	}

	if bytes.Equal(a, b) {
		t.Error("expected different canonical forms for different values")
	}
}

func TestCanonicalize_EmptyYAML(t *testing.T) {
	out, err := Canonicalize([]byte(``))
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != `{}` {
		t.Errorf("expected {}, got %s", out)
	}
}

func TestCanonicalize_NullYAML(t *testing.T) {
	out, err := Canonicalize([]byte(`null`))
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != `null` {
		t.Errorf("expected null, got %s", out)
	}
}

func TestCanonicalize_InvalidYAML(t *testing.T) {
	_, err := Canonicalize([]byte(`: bad`))
	if err == nil {
		t.Fatal("expected error for invalid YAML")
	}
	if !IsYAMLError(err) {
		t.Errorf("expected ErrInvalidYAML, got %v", err)
	}
}

func TestDigest_Consistent(t *testing.T) {
	canonical := []byte(`{"a":1,"b":2}`)
	d1 := Digest(canonical)
	d2 := Digest(canonical)
	if d1 != d2 {
		t.Error("digest not consistent")
	}
}

func TestDigest_DifferentInputDifferentDigest(t *testing.T) {
	d1 := Digest([]byte(`{"a":1}`))
	d2 := Digest([]byte(`{"a":2}`))
	if d1 == d2 {
		t.Error("expected different digests")
	}
}

func TestValidate_SecretDetection(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr error
	}{
		{"password literal", `password: mysecret123`, ErrSecretLiteral},
		{"secret literal", `secret: abc`, ErrSecretLiteral},
		{"api_key literal", `api_key: sk-12345`, ErrSecretLiteral},
		{"apikey literal", `apikey: 12345`, ErrSecretLiteral},
		{"token literal", `token: eyJhbGci`, ErrSecretLiteral},
		{"access_key literal", `access_key: AKIAIOSFODNN7EXAMPLE`, ErrSecretLiteral},
		{"AWS key ID", `aws_key: AKIAIOSFODNN7EXAMPLE`, ErrSecretLiteral},
		{"password ref ok", `password: ${ref:vault}`, nil},
		{"empty password ok", `password: ""`, nil},
		{"null password ok", `password: null`, nil},
		{"template ref ok", `token: "{{ .Values.token }}"`, nil},
		{"clean values", `replicas: 3`, nil},
		{"nested clean", "image:\n  tag: v1.0", nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Validate([]byte(tt.input), 0)
			if tt.wantErr != nil {
				if err != tt.wantErr {
					t.Errorf("expected %v, got %v", tt.wantErr, err)
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
			}
		})
	}
}

func TestValidate_SizeLimit(t *testing.T) {
	small := []byte(`key: value`)
	_, err := Validate(small, 5)
	if err != ErrSizeExceeded {
		t.Errorf("expected ErrSizeExceeded, got %v", err)
	}

	_, err = Validate(small, 100)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestValidate_CanonicalAndDigest(t *testing.T) {
	result, err := Validate([]byte(`key: value`), 0)
	if err != nil {
		t.Fatal(err)
	}
	if result.Digest == "" {
		t.Error("expected non-empty digest")
	}
	if len(result.Canonical) == 0 {
		t.Error("expected non-empty canonical")
	}
}

func TestCanonicalize_BOM(t *testing.T) {
	input := append([]byte{0xEF, 0xBB, 0xBF}, []byte(`key: value`)...)
	out, err := Canonicalize(input)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != `{"key":"value"}` {
		t.Errorf("expected {'key':'value'}, got %s", out)
	}
}

func TestCanonicalize_JSONInput(t *testing.T) {
	out, err := Canonicalize([]byte(`{"key":"value"}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != `{"key":"value"}` {
		t.Errorf("expected {'key':'value'}, got %s", out)
	}
}

// AC-018-03: Array replacement.
func TestCanonicalize_ArrayReplacement(t *testing.T) {
	yamlA := []byte(`ports: [80, 443]`)
	yamlB := []byte(`ports: [8080]`)

	a, err := Canonicalize(yamlA)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Canonicalize(yamlB)
	if err != nil {
		t.Fatal(err)
	}

	if string(b) != `{"ports":[8080]}` {
		t.Errorf("expected single-element array, got %s", b)
	}
	if bytes.Equal(a, b) {
		t.Error("expected different arrays")
	}
}
