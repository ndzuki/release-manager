import { describe, expect, it } from 'vitest';
import { canonicalDiff, canonicalize } from './valuesCanonical';
import { detectSecretLiteral, suggestedSecretPath, validateSecretRefs } from './valuesValidation';

describe('values canonicalization and validation', () => {
  it('treats equivalent YAML as the same canonical document', () => {
    const first = canonicalize('image:\n  tag: v1\nreplicas: 2\n');
    const second = canonicalize('replicas: 2\nimage: { tag: v1 }\n');

    expect(first.issue).toBeNull();
    expect(second.issue).toBeNull();
    expect(first.document).toBe(second.document);
    expect(canonicalDiff(first.value, second.value)).toEqual({ changes: [], hasChanges: false });
  });

  it('preserves array order and reports one array change', () => {
    const parent = canonicalize('ports: [80, 443]');
    const current = canonicalize('ports: [443, 80]');

    expect(canonicalDiff(current.value, parent.value)).toEqual({
      hasChanges: true,
      changes: [{ path: '.ports', kind: 'array_change', oldValue: [80, 443], newValue: [443, 80] }],
    });
  });

  it('returns YAML source location for invalid input', () => {
    const result = canonicalize('image:\n  tag: [broken');

    expect(result.issue?.code).toBe('invalid_yaml');
    expect(result.issue?.line).toBe(2);
    expect(result.issue?.message).toContain('YAML 语法错误');
  });

  it('rejects secret literals at the source line but accepts references', () => {
    expect(detectSecretLiteral('image: nginx\npassword: my-secret-value')?.line).toBe(2);
    expect(detectSecretLiteral('password: ${ref:vault/database}')).toBeNull();
    expect(detectSecretLiteral('token: "{{ .Values.token }}"')).toBeNull();
  });

  it('validates SecretRef choices and derives a safe path', () => {
    const available = [{ name: 'database-prod', keys: ['password'] }];
    expect(validateSecretRefs([{ id: '1', path: '', name: 'database-prod', key: '' }], available)).toBe('请完成 SecretRef 配置');
    expect(validateSecretRefs([{ id: '1', path: '.secrets.database_prod.password', name: 'database-prod', key: 'password' }], available)).toBeNull();
    expect(suggestedSecretPath('database-prod', 'tls.crt')).toBe('.secrets.database_prod.tls_crt');
  });
});
